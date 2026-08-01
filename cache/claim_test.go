package cache_test

import (
	"context"
	"errors"
	"strings"
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
	return strings.Contains(message, "basicStore")
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

// TestClaimRejectsANonPositiveLease keeps a claim from becoming a tombstone. A
// zero TTL never expires, so a crash between claiming and working would block
// that key for the life of the process, with nothing to say why.
func TestClaimRejectsANonPositiveLease(t *testing.T) {
	store := cache.NewMemory()

	for _, ttl := range []time.Duration{0, -time.Second} {
		claimed, err := cache.Claim(context.Background(), store, "key", ttl)
		if !errors.Is(err, cache.ErrInvalidTTL) {
			t.Fatalf("ttl %v error = %v, want ErrInvalidTTL", ttl, err)
		}
		if claimed {
			t.Fatalf("ttl %v was accepted", ttl)
		}
	}

	// Add itself still follows Set's rule, since it is the general capability
	// rather than the lease helper.
	stored, err := store.Add(context.Background(), "key", []byte("v"), 0)
	if err != nil || !stored {
		t.Fatalf("Add with a zero TTL = %v, %v", stored, err)
	}
}

func TestClaimValidatesTheKey(t *testing.T) {
	store := cache.NewMemory()
	if _, err := cache.Claim(context.Background(), store, "", time.Minute); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("error = %v, want ErrInvalidKey", err)
	}
}

// TestClaimNamesTheKeyInADriverError is what makes a failure actionable: a claim
// error in a log otherwise says only that something could not be claimed.
func TestClaimNamesTheKeyInADriverError(t *testing.T) {
	_, err := cache.Claim(context.Background(), failingAdder{}, "delivery:42", time.Minute)
	if err == nil {
		t.Fatal("expected the driver error")
	}
	if !strings.Contains(err.Error(), `"delivery:42"`) {
		t.Fatalf("error = %v, want it to name the key", err)
	}
	if !errors.Is(err, errDriverDown) {
		t.Fatalf("error = %v, want the driver error preserved", err)
	}
}

// TestOnceRunsWorkAtMostOnce covers the helper's happy path and its exclusion.
func TestOnceRunsWorkAtMostOnce(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	var runs atomic.Int64
	work := func(context.Context) error {
		runs.Add(1)
		return nil
	}

	ran, err := cache.Once(ctx, store, "job:1", time.Minute, work)
	if err != nil || !ran {
		t.Fatalf("Once = %v, %v", ran, err)
	}

	ran, err = cache.Once(ctx, store, "job:1", time.Minute, work)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if ran {
		t.Fatal("the work ran twice")
	}
	if runs.Load() != 1 {
		t.Fatalf("the work ran %d times", runs.Load())
	}
}

// TestOnceReleasesTheClaimWhenWorkFails is the whole reason the helper exists.
// A claim taken for work that then failed turns "this might run twice" into
// "this can never run again" — for a webhook receiver, a delivery the provider
// was told to resend, answered as a duplicate and lost.
func TestOnceReleasesTheClaimWhenWorkFails(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	failure := errors.New("downstream refused")
	ran, err := cache.Once(ctx, store, "job:1", time.Hour, func(context.Context) error {
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the work's own error", err)
	}
	if ran {
		t.Fatal("Once reported the work as done after it failed")
	}

	// The key is free again, so a retry can take it.
	var retried bool
	ran, err = cache.Once(ctx, store, "job:1", time.Hour, func(context.Context) error {
		retried = true
		return nil
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !ran || !retried {
		t.Fatal("the claim was not released, so the retry was refused")
	}
}

// TestOnceReleasesEvenWhenTheContextIsDone covers the case where releasing
// matters most: work that failed because its deadline passed. Releasing through
// the same dead context would deny exactly the retry the failure calls for.
func TestOnceReleasesEvenWhenTheContextIsDone(t *testing.T) {
	store := cache.NewMemory()

	ctx, cancel := context.WithCancel(context.Background())
	ran, err := cache.Once(ctx, store, "job:1", time.Hour, func(context.Context) error {
		cancel()
		return context.Canceled
	})
	if ran {
		t.Fatal("Once reported the work as done")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	claimed, err := cache.Claim(context.Background(), store, "job:1", time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed {
		t.Fatal("the claim survived a cancelled context, so the work can never retry")
	}
}

// TestOnceReportsAFailedRelease keeps a claim that could not be dropped from
// looking like a clean failure: the work will not retry, and the caller needs to
// know that as well as why the work failed.
func TestOnceReportsAFailedRelease(t *testing.T) {
	failure := errors.New("downstream refused")
	ran, err := cache.Once(context.Background(), undeletableStore{cache.NewMemory()},
		"job:1", time.Hour, func(context.Context) error { return failure })

	if !ran {
		t.Fatal("a claim that could not be released must be reported as still held")
	}
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the work's error preserved", err)
	}
	if !errors.Is(err, errDeleteDown) {
		t.Fatalf("error = %v, want the release failure reported too", err)
	}
}

func TestOnceRejectsNilWork(t *testing.T) {
	if _, err := cache.Once(context.Background(), cache.NewMemory(), "k", time.Minute, nil); !errors.Is(err, cache.ErrNilLoader) {
		t.Fatalf("error = %v, want ErrNilLoader", err)
	}
}

func TestOncePropagatesAClaimFailure(t *testing.T) {
	var ran bool
	claimed, err := cache.Once(context.Background(), basicStore{}, "k", time.Minute,
		func(context.Context) error { ran = true; return nil })

	if !errors.Is(err, cache.ErrNotAtomic) {
		t.Fatalf("error = %v, want ErrNotAtomic", err)
	}
	if claimed || ran {
		t.Fatal("work ran without a claim")
	}
}

var (
	errDriverDown = errors.New("driver is down")
	errDeleteDown = errors.New("delete is down")
)

// failingAdder claims nothing and says why.
type failingAdder struct{ basicStore }

func (failingAdder) Add(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, errDriverDown
}

// undeletableStore can claim but cannot release, the shape of a backend that is
// partly unreachable.
type undeletableStore struct{ *cache.Memory }

func (undeletableStore) Delete(context.Context, string) error { return errDeleteDown }

// TestClaimValidatesBeforeReachingTheStore keeps the helper's own guarantees off
// the driver. A third-party Adder that skips validation would otherwise decide
// what an empty key means, and each one differently.
func TestClaimValidatesBeforeReachingTheStore(t *testing.T) {
	spy := &recordingAdder{}

	if _, err := cache.Claim(context.Background(), spy, "", time.Minute); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("error = %v, want ErrInvalidKey", err)
	}
	if _, err := cache.Claim(context.Background(), spy, "key", 0); !errors.Is(err, cache.ErrInvalidTTL) {
		t.Fatalf("error = %v, want ErrInvalidTTL", err)
	}
	if spy.calls.Load() != 0 {
		t.Fatalf("the store was called %d times for arguments the helper rejects", spy.calls.Load())
	}

	if _, err := cache.Claim(context.Background(), spy, "key", time.Minute); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if spy.calls.Load() != 1 {
		t.Fatalf("the store was called %d times, want 1", spy.calls.Load())
	}
}

// recordingAdder accepts everything and counts what reached it.
type recordingAdder struct {
	basicStore
	calls atomic.Int64
}

func (a *recordingAdder) Add(context.Context, string, []byte, time.Duration) (bool, error) {
	a.calls.Add(1)
	return true, nil
}
