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

	store.mu.Lock()
	entries := len(store.entries)
	store.mu.Unlock()
	if entries != 0 {
		t.Fatalf("expired entries = %d, want 0", entries)
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
