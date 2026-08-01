package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LoonY20/ossein/cache"
)

func TestAddStoresOnlyWhenTheKeyIsFree(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	stored, err := store.Add(ctx, "key", []byte("first"), time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !stored {
		t.Fatal("the first Add did not store")
	}

	stored, err = store.Add(ctx, "key", []byte("second"), time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if stored {
		t.Fatal("the second Add stored over a live key")
	}

	value, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(value) != "first" {
		t.Fatalf("value = %q, want the first writer's", value)
	}
}

// TestAddTreatsAnExpiredKeyAsFree is what makes a claim a lease rather than a
// permanent lock: the TTL is how a claim is released when the holder dies.
func TestAddTreatsAnExpiredKeyAsFree(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	if _, err := store.Add(ctx, "key", []byte("first"), time.Millisecond); err != nil {
		t.Fatalf("Add: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	stored, err := store.Add(ctx, "key", []byte("second"), time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !stored {
		t.Fatal("an expired key was still treated as taken")
	}

	value, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(value) != "second" {
		t.Fatalf("value = %q", value)
	}
}

// TestAddIsAtomicUnderConcurrency is the whole reason the capability exists. The
// Get-then-Set this replaces lets several callers observe the same miss; exactly
// one Add may return true.
func TestAddIsAtomicUnderConcurrency(t *testing.T) {
	for round := 0; round < 50; round++ {
		store := cache.NewMemory()

		const racers = 64
		var winners atomic.Int64
		start := make(chan struct{})
		var group sync.WaitGroup

		for i := 0; i < racers; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				stored, err := store.Add(context.Background(), "claim", []byte("1"), time.Minute)
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				if stored {
					winners.Add(1)
				}
			}()
		}
		close(start)
		group.Wait()

		if got := winners.Load(); got != 1 {
			t.Fatalf("%d of %d racers claimed the key, want exactly 1", got, racers)
		}
	}
}

// TestGetThenSetIsNotAtomic is the comparison that justifies the addition, run
// against the same store. It is deliberately not an assertion that the race
// manifests — the window is sub-microsecond against a local map — but it fails
// if two callers ever both win, which is the outcome Add makes impossible.
func TestGetThenSetIsNotAtomic(t *testing.T) {
	store := cache.NewMemory()

	const racers = 64
	var winners atomic.Int64
	start := make(chan struct{})
	var group sync.WaitGroup

	for i := 0; i < racers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, err := store.Get(context.Background(), "claim"); err == nil {
				return
			}
			if err := store.Set(context.Background(), "claim", []byte("1"), time.Minute); err != nil {
				t.Errorf("Set: %v", err)
				return
			}
			winners.Add(1)
		}()
	}
	close(start)
	group.Wait()

	t.Logf("Get-then-Set produced %d winners of %d racers; Add produces exactly 1",
		winners.Load(), racers)
}

func TestClaimUsesTheAtomicCapability(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	first, err := cache.Claim(ctx, store, "delivery:1", time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !first {
		t.Fatal("the first claim failed")
	}

	second, err := cache.Claim(ctx, store, "delivery:1", time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if second {
		t.Fatal("the key was claimed twice")
	}

	// Deleting releases it, which is how a failed unit of work is retried.
	if err := store.Delete(ctx, "delivery:1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	again, err := cache.Claim(ctx, store, "delivery:1", time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !again {
		t.Fatal("a released key could not be claimed")
	}
}

// TestClaimRefusesAStoreThatCannotBeAtomic is the deliberate departure from the
// obvious design. Falling back to Get-then-Set would hand back a guarantee that
// is not one, and the failure it produces — duplicate work — looks like an
// application bug rather than a missing capability.
func TestClaimRefusesAStoreThatCannotBeAtomic(t *testing.T) {
	claimed, err := cache.Claim(context.Background(), basicStore{}, "key", time.Minute)
	if !errors.Is(err, cache.ErrNotAtomic) {
		t.Fatalf("error = %v, want ErrNotAtomic", err)
	}
	if claimed {
		t.Fatal("Claim reported success against a store that cannot provide it")
	}
	if !errors.Is(err, cache.ErrNotAtomic) || !containsType(err.Error()) {
		t.Fatalf("error = %v, want it to name the store type", err)
	}
}

func TestClaimRejectsANilStore(t *testing.T) {
	if _, err := cache.Claim(context.Background(), nil, "key", time.Minute); !errors.Is(err, cache.ErrNilStore) {
		t.Fatalf("error = %v, want ErrNilStore", err)
	}
}

func TestAddValidatesItsArguments(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	if _, err := store.Add(ctx, "", []byte("v"), time.Minute); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("empty key error = %v", err)
	}
	if _, err := store.Add(ctx, "key", []byte("v"), -time.Second); !errors.Is(err, cache.ErrInvalidTTL) {
		t.Fatalf("negative TTL error = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Add(cancelled, "key", []byte("v"), time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
}

// TestAddDoesNotRetainTheCallerSlice matches the contract Set follows: a caller
// that reuses its buffer must not change what is stored.
func TestAddDoesNotRetainTheCallerSlice(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	value := []byte("first")
	if _, err := store.Add(ctx, "key", value, time.Minute); err != nil {
		t.Fatalf("Add: %v", err)
	}
	value[0] = 'X'

	stored, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(stored) != "first" {
		t.Fatalf("value = %q, want the bytes as they were at Add", stored)
	}
}

// TestAddKeepsTheEvictionOrderConsistent guards the bookkeeping: an entry added
// over an expired one must be tracked once, not twice.
func TestAddKeepsTheEvictionOrderConsistent(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Add(ctx, "key", []byte("v"), time.Millisecond); err != nil {
			t.Fatalf("Add: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if purged := store.PurgeExpired(); purged != 1 {
		t.Fatalf("PurgeExpired removed %d entries, want 1", purged)
	}
	if purged := store.PurgeExpired(); purged != 0 {
		t.Fatalf("a second purge removed %d entries, want 0", purged)
	}
}

// basicStore implements only Store, the shape of a backend that cannot claim.
type basicStore struct{}

func (basicStore) Get(context.Context, string) ([]byte, error) { return nil, cache.ErrMiss }
func (basicStore) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (basicStore) Delete(context.Context, string) error { return nil }

func containsType(message string) bool {
	return len(message) > 0 && message[len(message)-len("basicStore"):] == "basicStore"
}

// TestAddOnTheZeroValue covers the documented promise that a zero Memory is
// ready to use, which for Add means initialising the map it writes into.
func TestAddOnTheZeroValue(t *testing.T) {
	var store cache.Memory

	stored, err := store.Add(context.Background(), "key", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !stored {
		t.Fatal("the zero value refused a free key")
	}

	value, err := store.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(value) != "v" {
		t.Fatalf("value = %q", value)
	}
}
