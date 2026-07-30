package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemorySetGetAndDelete(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	input := []byte("value")
	if err := store.Set(ctx, "key", input, 0); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'

	first, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "value" {
		t.Fatalf("Get() = %q", first)
	}
	first[0] = 'Y'
	second, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "value" {
		t.Fatalf("second Get() = %q", second)
	}

	if err := store.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "key"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get() after Delete = %v, want ErrMiss", err)
	}
	if err := store.Delete(ctx, "key"); err != nil {
		t.Fatalf("idempotent Delete() = %v", err)
	}
}

func TestMemoryExpiration(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewMemory()
	store.now = func() time.Time { return now }
	if err := store.Set(context.Background(), "key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute - time.Nanosecond)
	if _, err := store.Get(context.Background(), "key"); err != nil {
		t.Fatalf("Get() before expiration = %v", err)
	}
	now = now.Add(time.Nanosecond)
	if _, err := store.Get(context.Background(), "key"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get() at expiration = %v, want ErrMiss", err)
	}

	store.mu.RLock()
	entries := len(store.entries)
	store.mu.RUnlock()
	if entries != 0 {
		t.Fatalf("expired entries = %d, want 0", entries)
	}
}

func TestMemorySetAmortizesExpiredEntryCleanup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewMemory()
	store.now = func() time.Time { return now }
	ctx := context.Background()

	expiredCount := memoryCleanupSampleSize * 4
	for index := range expiredCount {
		key := fmt.Sprintf("expired:%d", index)
		if err := store.Set(ctx, key, []byte("value"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Minute)

	writes := expiredCount / memoryCleanupSampleSize
	for index := range writes {
		key := fmt.Sprintf("live:%d", index)
		if err := store.Set(ctx, key, []byte("value"), 0); err != nil {
			t.Fatal(err)
		}
	}

	store.mu.RLock()
	entries := len(store.entries)
	orderEntries := store.order.Len()
	store.mu.RUnlock()
	if entries != writes || orderEntries != writes {
		t.Fatalf(
			"entries after amortized cleanup = map:%d order:%d, want %d",
			entries,
			orderEntries,
			writes,
		)
	}
}

func TestMemoryReadsClockWhileHoldingEntryLock(t *testing.T) {
	expiry := time.Unix(1_700_000_000, 0)

	for _, test := range []struct {
		name string
		run  func(*Memory) error
	}{
		{
			name: "Get",
			run: func(store *Memory) error {
				_, err := store.Get(context.Background(), "key")
				return err
			},
		},
		{
			name: "PurgeExpired",
			run: func(store *Memory) error {
				if removed := store.PurgeExpired(); removed != 1 {
					return fmt.Errorf("PurgeExpired() = %d, want 1", removed)
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemory()
			store.mu.Lock()
			element := store.order.PushBack("key")
			store.entries["key"] = memoryEntry{
				value:        []byte("expired"),
				expiresAt:    expiry,
				orderElement: element,
			}
			store.mu.Unlock()
			store.now = func() time.Time {
				if store.mu.TryLock() {
					store.mu.Unlock()
					t.Error("currentTime() called without holding an entry lock")
				}
				return expiry
			}

			err := test.run(store)
			if test.name == "Get" && !errors.Is(err, ErrMiss) {
				t.Fatalf("Get() = %v, want ErrMiss", err)
			}
			if test.name != "Get" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMemoryPurgeExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewMemory()
	store.now = func() time.Time { return now }
	ctx := context.Background()
	if err := store.Set(ctx, "expired", []byte("old"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "live", []byte("current"), 0); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	if removed := store.PurgeExpired(); removed != 1 {
		t.Fatalf("PurgeExpired() = %d, want 1", removed)
	}
	if removed := store.PurgeExpired(); removed != 0 {
		t.Fatalf("second PurgeExpired() = %d, want 0", removed)
	}
	if value, err := store.Get(ctx, "live"); err != nil || string(value) != "current" {
		t.Fatalf("Get(live) = %q, %v", value, err)
	}
}

func TestMemoryExpiredReadAlsoAmortizesCleanup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewMemory()
	store.now = func() time.Time { return now }
	ctx := context.Background()

	for index := range memoryCleanupSampleSize + 1 {
		key := fmt.Sprintf("expired:%d", index)
		if err := store.Set(ctx, key, []byte("value"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Minute)

	if _, err := store.Get(ctx, "expired:0"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get() = %v, want ErrMiss", err)
	}
	store.mu.RLock()
	entries := len(store.entries)
	orderEntries := store.order.Len()
	store.mu.RUnlock()
	if entries != 0 || orderEntries != 0 {
		t.Fatalf("entries after expired read = map:%d order:%d, want 0", entries, orderEntries)
	}
}

func TestMemoryValidationAndContext(t *testing.T) {
	var store Memory
	ctx := context.Background()
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrMiss) {
		t.Fatalf("zero-value Get() = %v, want ErrMiss", err)
	}
	if _, err := store.Get(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Get(\"\") = %v, want ErrInvalidKey", err)
	}
	if err := store.Set(ctx, "", nil, 0); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Set(\"\") = %v, want ErrInvalidKey", err)
	}
	if err := store.Set(ctx, "key", nil, -time.Second); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("Set() negative TTL = %v, want ErrInvalidTTL", err)
	}
	if err := store.Delete(ctx, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Delete(\"\") = %v, want ErrInvalidKey", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Get(cancelled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Get() = %v", err)
	}
	if err := store.Set(cancelled, "key", nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Set() = %v", err)
	}
	if err := store.Delete(cancelled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Delete() = %v", err)
	}
}

func TestMemoryConcurrentAccess(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	var wait sync.WaitGroup
	for worker := range 16 {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			for iteration := range 100 {
				key := fmt.Sprintf("key:%d", (id+iteration)%8)
				if err := store.Set(ctx, key, []byte(key), time.Minute); err != nil {
					t.Errorf("Set() = %v", err)
					return
				}
				if _, err := store.Get(ctx, key); err != nil && !errors.Is(err, ErrMiss) {
					t.Errorf("Get() = %v", err)
					return
				}
				if iteration%10 == 0 {
					if err := store.Delete(ctx, key); err != nil {
						t.Errorf("Delete() = %v", err)
						return
					}
				}
			}
		}(worker)
	}
	wait.Wait()
}

func TestMemoryConcurrentExpirationAndRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewMemory()
	store.now = func() time.Time { return now }
	ctx := context.Background()

	for iteration := range 1_000 {
		now = time.Unix(1_700_000_000+int64(iteration), 0)
		if err := store.Set(ctx, "key", []byte("expired"), time.Nanosecond); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Nanosecond)

		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, _ = store.Get(ctx, "key")
		}()
		go func() {
			defer wait.Done()
			<-start
			if err := store.Set(ctx, "key", []byte("fresh"), 0); err != nil {
				t.Errorf("Set() = %v", err)
			}
		}()
		close(start)
		wait.Wait()

		value, err := store.Get(ctx, "key")
		if err != nil || string(value) != "fresh" {
			t.Fatalf("iteration %d: Get() = %q, %v", iteration, value, err)
		}
	}
}

func BenchmarkMemoryParallelGet(b *testing.B) {
	store := NewMemory()
	ctx := context.Background()
	if err := store.Set(ctx, "key", []byte("value"), 0); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			if _, err := store.Get(ctx, "key"); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
