package cache

import (
	"bytes"
	"container/list"
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	value        []byte
	expiresAt    time.Time
	orderElement *list.Element
}

const memoryCleanupSampleSize = 16

// Memory is a concurrency-safe, process-local Store. Its zero value is ready
// for use. Expired entries are removed when read and through bounded cleanup
// during writes and expired-read escalation.
//
// Without a size bound the cache holds whatever fits its TTLs: reclamation is
// driven by traffic and rate-limited to a sample per operation, so a burst of
// long-lived keys stays resident until they expire. WithMaxEntries turns that
// into a budget, evicting the least recently used key when a write would exceed
// it.
//
// The bound changes how reads are tracked. Unbounded, a read takes a shared lock
// and the ordering list is only a cleanup cursor. Bounded, a read has to record
// that it happened — otherwise eviction cannot tell hot keys from cold ones — so
// it takes the exclusive lock. That is the cost of a bound, and it is the reason
// the bound is opt-in rather than a default.
type Memory struct {
	mu         sync.RWMutex
	entries    map[string]memoryEntry
	order      list.List
	now        func() time.Time
	maxEntries int

	// cursor is where the next cleanup sample starts. Nil means the front, which
	// is also where a sample wraps back to.
	cursor *list.Element
}

// MemoryOption configures the in-memory driver.
type MemoryOption func(*Memory)

// WithMaxEntries caps how many entries the cache holds. A write that would
// exceed the cap evicts the least recently used key first.
//
// A bound and a claim do not mix. Claim and Once depend on a key staying present
// for its lease; eviction can drop one early, and the work it was guarding then
// runs a second time. A cache used for idempotency keys, run-once jobs, or leases
// should be left unbounded — with RegisterMemory to reclaim expired keys — and a
// bounded one kept for data that is merely expensive to recompute.
//
// A non-positive value means no cap, which is the default.
func WithMaxEntries(entries int) MemoryOption {
	return func(m *Memory) {
		if entries > 0 {
			m.maxEntries = entries
		}
	}
}

// NewMemory creates an empty process-local cache.
func NewMemory(options ...MemoryOption) *Memory {
	memory := &Memory{
		entries: make(map[string]memoryEntry),
		now:     time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(memory)
		}
	}
	return memory
}

// Get returns a copy of the cached value.
func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}

	if m.maxEntries > 0 {
		// A bounded cache has to know what is hot, and recording a read is a
		// write. Unbounded, the shared lock below is kept.
		return m.getAndPromote(key)
	}

	m.mu.RLock()
	now := m.currentTime()
	entry, ok := m.entries[key]
	if !ok {
		m.mu.RUnlock()
		return nil, ErrMiss
	}
	if entry.expiresAt.IsZero() || now.Before(entry.expiresAt) {
		m.mu.RUnlock()
		return bytes.Clone(entry.value), nil
	}
	m.mu.RUnlock()

	// Upgrade to an exclusive lock only for an expired candidate. The entry
	// must be checked again because a concurrent Set may have replaced it.
	m.mu.Lock()
	now = m.currentTime()
	entry, ok = m.entries[key]
	if !ok {
		m.purgeSampleLocked(now)
		m.mu.Unlock()
		return nil, ErrMiss
	}
	if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
		m.deleteLocked(key, entry)
		m.purgeSampleLocked(now)
		m.mu.Unlock()
		return nil, ErrMiss
	}
	m.purgeSampleLocked(now)
	m.mu.Unlock()
	return bytes.Clone(entry.value), nil
}

// Set stores a copy of value. A zero TTL keeps the value until it is deleted.
func (m *Memory) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateTTL(ttl); err != nil {
		return err
	}

	cloned := bytes.Clone(value)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]memoryEntry)
	}
	now := m.currentTime()
	m.purgeSampleLocked(now)
	entry := memoryEntry{value: cloned}
	if ttl > 0 {
		entry.expiresAt = now.Add(ttl)
	}
	if previous, ok := m.entries[key]; ok {
		entry.orderElement = previous.orderElement
		// Overwriting is a use, so the key goes to the warm end. Done in both
		// modes: unbounded the list is only where the cleanup cursor walks, and
		// insertion order has no meaning there worth preserving.
		m.order.MoveToBack(entry.orderElement)
	} else {
		entry.orderElement = m.order.PushBack(key)
	}
	m.entries[key] = entry
	m.evictLocked()
	return nil
}

// Delete removes a key. Missing and expired keys are accepted.
func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}

	m.mu.Lock()
	if entry, ok := m.entries[key]; ok {
		m.deleteLocked(key, entry)
	}
	m.mu.Unlock()
	return nil
}

// PurgeExpired removes all entries whose TTL has elapsed and returns the
// number removed. Normal writes and reads of expired entries already perform
// bounded cleanup; this method is useful before inspecting memory use or after
// a long idle period.
func (m *Memory) PurgeExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.currentTime()
	return m.purgeExpiredLocked(now)
}

// purgeSampleLocked reclaims expired entries from a bounded sample.
//
// The sample is a window that walks the ordering list on its own cursor, so
// successive calls continue where the last one stopped and every resident key is
// visited within a bounded number of operations.
//
// An earlier version had no cursor and rewound to the front each time. Unbounded
// it compensated by rotating each visited entry to the back, which swept the list
// over time; bounded it could not rotate, because that would mark a cold entry as
// recently used and let cleanup decide what survives eviction — so it re-walked
// the same front entries forever and reclaimed nothing behind them. Turning on the
// memory bound made reclamation worse than leaving it off. A cursor gives both
// modes the same coverage without either of them reordering anything.
func (m *Memory) purgeSampleLocked(now time.Time) {
	for range min(memoryCleanupSampleSize, m.order.Len()) {
		if m.cursor == nil {
			m.cursor = m.order.Front()
		}
		element := m.cursor

		// Advanced before any removal: deleting an element clears its links, and
		// the cursor would have nothing to continue from.
		m.cursor = element.Next()

		key := element.Value.(string)
		entry := m.entries[key]
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			m.deleteLocked(key, entry)
		}
	}
}

// getAndPromote reads a key and records the access, for a bounded cache.
func (m *Memory) getAndPromote(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.currentTime()
	entry, ok := m.entries[key]
	if !ok {
		m.purgeSampleLocked(now)
		return nil, ErrMiss
	}
	if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
		m.deleteLocked(key, entry)
		m.purgeSampleLocked(now)
		return nil, ErrMiss
	}

	m.order.MoveToBack(entry.orderElement)
	return bytes.Clone(entry.value), nil
}

// evictLocked drops the least recently used keys until the cache is within its
// budget.
//
// It evicts by age of use, not by expiry. Every write cleans a sample first, so
// dead entries are usually gone before this runs, but a sample is sixteen keys and
// a live one can be evicted while dead ones remain elsewhere. Scanning for a dead
// victim on every over-budget write would be linear in the cache size, and at
// steady state that is every write. A bound means live data is discarded; that is
// what makes it a bound.
func (m *Memory) evictLocked() {
	if m.maxEntries <= 0 {
		return
	}
	// Driven off the list rather than the map, which the two-way invariant makes
	// equivalent and which keeps Front from ever being nil inside the loop.
	for m.order.Len() > m.maxEntries {
		element := m.order.Front()
		key := element.Value.(string)
		m.deleteLocked(key, m.entries[key])
	}
}

func (m *Memory) purgeExpiredLocked(now time.Time) int {
	removed := 0
	for key, entry := range m.entries {
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			m.deleteLocked(key, entry)
			removed++
		}
	}
	return removed
}

func (m *Memory) deleteLocked(key string, entry memoryEntry) {
	delete(m.entries, key)
	// Every entry has an element, which the order invariant covers, so this is not
	// guarded against nil: a break there should surface as a panic in a test rather
	// than as an entry silently left in the ordering list.
	//
	// The cleanup cursor cannot be left pointing at a removed element: its links
	// are cleared, so the next sample would restart from the front and the keys
	// behind it would never be visited.
	if m.cursor == entry.orderElement {
		m.cursor = entry.orderElement.Next()
	}
	m.order.Remove(entry.orderElement)
}

func (m *Memory) currentTime() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

var (
	_ Store = (*Memory)(nil)
	// Asserted as well as implemented: Adder is reached by type assertion, so a
	// signature drift would silently demote this driver and turn every Claim into
	// ErrNotAtomic, with nothing failing to compile.
	_ Adder = (*Memory)(nil)
)

// Add stores a value only when the key is free, and reports whether it did.
//
// The check and the write happen under one lock, which is what separates this
// from Get followed by Set: there is no window for a second caller to see the
// same miss. A zero TTL stores a value that never expires, as Set does, so the
// key stays taken until it is deleted.
func (m *Memory) Add(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateKey(key); err != nil {
		return false, err
	}
	if err := validateTTL(ttl); err != nil {
		return false, err
	}

	cloned := bytes.Clone(value)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]memoryEntry)
	}

	now := m.currentTime()
	if existing, ok := m.entries[key]; ok {
		if existing.expiresAt.IsZero() || now.Before(existing.expiresAt) {
			return false, nil
		}
		// Expired: the key is free, and the stale entry is replaced below.
		m.deleteLocked(key, existing)
	}
	m.purgeSampleLocked(now)

	entry := memoryEntry{value: cloned, orderElement: m.order.PushBack(key)}
	if ttl > 0 {
		entry.expiresAt = now.Add(ttl)
	}
	m.entries[key] = entry
	m.evictLocked()
	return true, nil
}

// Len reports how many entries are resident, expired ones included until they
// are reclaimed. It exists so a size bound can be observed, by a test or by a
// health endpoint reporting memory use.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
