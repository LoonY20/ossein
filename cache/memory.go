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
type Memory struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
	order   list.List
	now     func() time.Time
}

// NewMemory creates an empty process-local cache.
func NewMemory() *Memory {
	return &Memory{
		entries: make(map[string]memoryEntry),
		now:     time.Now,
	}
}

// Get returns a copy of the cached value.
func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
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
	} else {
		entry.orderElement = m.order.PushBack(key)
	}
	m.entries[key] = entry
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

func (m *Memory) purgeSampleLocked(now time.Time) {
	limit := min(memoryCleanupSampleSize, m.order.Len())
	for range limit {
		element := m.order.Front()
		key := element.Value.(string)
		entry := m.entries[key]
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			m.deleteLocked(key, entry)
			continue
		}
		m.order.MoveToBack(element)
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
	if entry.orderElement != nil {
		m.order.Remove(entry.orderElement)
	}
}

func (m *Memory) currentTime() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

var _ Store = (*Memory)(nil)
