package cache

import (
	"bytes"
	"context"
	"sync"
	"time"
)

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

// Memory is a concurrency-safe, process-local Store. Its zero value is ready
// for use. Expired entries are removed lazily when they are read.
type Memory struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
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

	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	if !ok {
		return nil, ErrMiss
	}
	if !entry.expiresAt.IsZero() && !m.currentTime().Before(entry.expiresAt) {
		delete(m.entries, key)
		return nil, ErrMiss
	}
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

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]memoryEntry)
	}
	entry := memoryEntry{value: bytes.Clone(value)}
	if ttl > 0 {
		entry.expiresAt = m.currentTime().Add(ttl)
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
	delete(m.entries, key)
	m.mu.Unlock()
	return nil
}

func (m *Memory) currentTime() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

var _ Store = (*Memory)(nil)
