package cache_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/cache"
)

// TestMaxEntriesCapsResidentSize is what turns "does not leak" into "cannot
// exceed a budget". Without it, memory is TTL times arrival rate, so a burst of
// long-lived keys is held in full until they expire.
func TestMaxEntriesCapsResidentSize(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(10))
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	if got := store.Len(); got != 10 {
		t.Fatalf("the cache holds %d entries, want the configured 10", got)
	}

	// The most recent survive, which is the useful half of the trade.
	for i := 990; i < 1000; i++ {
		if _, err := store.Get(ctx, strconv.Itoa(i)); err != nil {
			t.Fatalf("recent key %d: %v", i, err)
		}
	}
	if _, err := store.Get(ctx, "0"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("the oldest key survived a 1000-write burst into a 10-entry cache")
	}
}

// TestEvictionIsLeastRecentlyUsed is the difference between a bound that keeps
// the working set and one that discards it. A key read on every request must
// outlive keys written after it and never touched.
func TestEvictionIsLeastRecentlyUsed(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(3))
	ctx := context.Background()

	for _, key := range []string{"a", "b", "c"} {
		if err := store.Set(ctx, key, []byte(key), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	// "a" is the oldest, but it is the one being used.
	if _, err := store.Get(ctx, "a"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := store.Set(ctx, "d", []byte("d"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, err := store.Get(ctx, "a"); err != nil {
		t.Fatalf("the key in active use was evicted: %v", err)
	}
	if _, err := store.Get(ctx, "b"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("the least recently used key survived: %v", err)
	}
	for _, key := range []string{"c", "d"} {
		if _, err := store.Get(ctx, key); err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
	}
}

// TestOverwritingCountsAsUse keeps a key that is being written from looking cold.
func TestOverwritingCountsAsUse(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(2))
	ctx := context.Background()

	for _, key := range []string{"a", "b"} {
		if err := store.Set(ctx, key, []byte("1"), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := store.Set(ctx, "a", []byte("2"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, "c", []byte("1"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, err := store.Get(ctx, "a"); err != nil {
		t.Fatalf("the rewritten key was evicted: %v", err)
	}
	if _, err := store.Get(ctx, "b"); !errors.Is(err, cache.ErrMiss) {
		t.Fatal("the untouched key survived")
	}
}

// TestAddRespectsTheBound covers the other write path.
func TestAddRespectsTheBound(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(5))
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if _, err := store.Add(ctx, strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if got := store.Len(); got != 5 {
		t.Fatalf("the cache holds %d entries, want 5", got)
	}
}

// TestExpiredEntriesGoBeforeLiveOnes checks the ordering between the two
// reclamation mechanisms: a bounded cache should drop what is already dead
// before it starts discarding data someone might still want.
func TestExpiredEntriesGoBeforeLiveOnes(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(4))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := store.Set(ctx, fmt.Sprintf("stale-%d", i), []byte("v"), 10*time.Millisecond); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := store.Set(ctx, "keep", []byte("v"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	if err := store.Set(ctx, "fresh", []byte("v"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// "keep" is the oldest live key, so a bound that evicted blindly would take
	// it even though three dead entries were available.
	if _, err := store.Get(ctx, "keep"); err != nil {
		t.Fatalf("a live key was evicted while expired ones remained: %v", err)
	}
}

// TestUnboundedCacheKeepsEverythingLive is the default, stated as a test so a
// bound cannot creep in as one.
func TestUnboundedCacheKeepsEverythingLive(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	for i := 0; i < 500; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if got := store.Len(); got != 500 {
		t.Fatalf("an unbounded cache holds %d of 500 entries", got)
	}
}

func TestWithMaxEntriesIgnoresNonPositiveValues(t *testing.T) {
	for _, limit := range []int{0, -1} {
		store := cache.NewMemory(cache.WithMaxEntries(limit))
		for i := 0; i < 20; i++ {
			if err := store.Set(context.Background(), strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
				t.Fatalf("Set: %v", err)
			}
		}
		if got := store.Len(); got != 20 {
			t.Fatalf("limit %d capped the cache at %d entries", limit, got)
		}
	}
	// And a nil option is ignored rather than panicking.
	if store := cache.NewMemory(nil); store == nil {
		t.Fatal("NewMemory returned nil")
	}
}

// TestBoundedCacheIsSafeUnderConcurrency covers the read path that a bound
// changes from shared to exclusive.
func TestBoundedCacheIsSafeUnderConcurrency(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(32))

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < 200; i++ {
				key := strconv.Itoa((worker*i)%64 + 1)
				_ = store.Set(context.Background(), key, []byte("v"), time.Hour)
				_, _ = store.Get(context.Background(), key)
				_, _ = store.Add(context.Background(), key+"-a", []byte("v"), time.Hour)
			}
		}(worker)
	}
	group.Wait()

	if got := store.Len(); got > 32 {
		t.Fatalf("the cache holds %d entries, over its bound of 32", got)
	}
}

// TestRegisterMemoryReclaimsOnASchedule covers the idle case: reclamation is
// driven by traffic, so a process that goes quiet after a burst holds every
// expired entry until its next write.
func TestRegisterMemoryReclaimsOnASchedule(t *testing.T) {
	store := cache.NewMemory()
	app := ossein.New()

	if err := cache.RegisterMemory(app, store, cache.WithCleanupInterval(10*time.Millisecond)); err != nil {
		t.Fatalf("RegisterMemory: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), []byte("v"), 5*time.Millisecond); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.Len() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("the janitor left %d expired entries behind", got)
	}

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestRegisterMemoryStopsItsJanitor keeps a goroutine from outliving the
// application it belongs to, which in a test suite means one per application.
func TestRegisterMemoryStopsItsJanitor(t *testing.T) {
	store := cache.NewMemory()
	app := ossein.New()

	if err := cache.RegisterMemory(app, store, cache.WithCleanupInterval(time.Millisecond)); err != nil {
		t.Fatalf("RegisterMemory: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Stop waits for the janitor, so nothing can be reclaimed after it returns.
	if err := store.Set(context.Background(), "k", []byte("v"), time.Nanosecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := store.Len(); got != 1 {
		t.Fatalf("the janitor ran after shutdown: %d entries left", got)
	}
}

func TestRegisterMemoryResolvesAsAStore(t *testing.T) {
	store := cache.NewMemory()
	app := ossein.New()

	if err := cache.RegisterMemory(app, store); err != nil {
		t.Fatalf("RegisterMemory: %v", err)
	}

	app.Get("/", func(c *ossein.Context) error { return c.NoContent(http.StatusNoContent) })
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	resolved, err := ossein.Resolve[cache.Store](app)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != cache.Store(store) {
		t.Fatal("a different store was registered")
	}
}

func TestRegisterMemoryRejectsNilArguments(t *testing.T) {
	if err := cache.RegisterMemory(nil, cache.NewMemory()); err == nil {
		t.Fatal("a nil app was accepted")
	}
	if err := cache.RegisterMemory(ossein.New(), nil); err == nil {
		t.Fatal("a nil cache was accepted")
	}
}

func TestRegisterMemoryReportsADuplicateRegistration(t *testing.T) {
	app := ossein.New()
	if err := cache.RegisterMemory(app, cache.NewMemory()); err != nil {
		t.Fatalf("RegisterMemory: %v", err)
	}
	if err := cache.RegisterMemory(app, cache.NewMemory()); err == nil {
		t.Fatal("a second registration was accepted")
	}
}

// TestBoundedGetReclaimsAnExpiredKey covers the read path a bound replaces: it
// still has to drop what it finds expired, and still has to clean a sample, or
// the bounded cache reclaims less than the unbounded one it replaced.
func TestBoundedGetReclaimsAnExpiredKey(t *testing.T) {
	store := cache.NewMemory(cache.WithMaxEntries(100))
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := store.Set(ctx, strconv.Itoa(i), []byte("v"), 5*time.Millisecond); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	time.Sleep(20 * time.Millisecond)

	if _, err := store.Get(ctx, "0"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("an expired key was returned: %v", err)
	}
	if got := store.Len(); got == 20 {
		t.Fatal("reading an expired key reclaimed nothing")
	}

	// A miss on an absent key cleans too, which is how an idle-but-read process
	// recovers memory.
	before := store.Len()
	if _, err := store.Get(ctx, "absent"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("Get: %v", err)
	}
	if store.Len() > before {
		t.Fatal("a miss added an entry")
	}
}

// TestUnboundedGetReclaimsAnExpiredKey is the same property on the shared-lock
// path, which escalates to the exclusive lock only for an expired candidate.
func TestUnboundedGetReclaimsAnExpiredKey(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	if err := store.Set(ctx, "key", []byte("v"), 5*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, "live", []byte("v"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if _, err := store.Get(ctx, "key"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("an expired key was returned: %v", err)
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("the cache holds %d entries, want the live one only", got)
	}
	if _, err := store.Get(ctx, "live"); err != nil {
		t.Fatalf("the live key was reclaimed: %v", err)
	}
}

// TestZeroValueMemoryAcceptsWrites covers the documented promise for Set, as the
// claim tests do for Add.
func TestZeroValueMemoryAcceptsWrites(t *testing.T) {
	var store cache.Memory

	if err := store.Set(context.Background(), "key", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, err := store.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(value) != "v" {
		t.Fatalf("value = %q", value)
	}
}
