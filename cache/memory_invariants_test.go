package cache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// checkOrderInvariant verifies that the eviction list and the entry map agree.
//
// They are two views of the same data, and every reclamation path walks the list
// to find keys to remove from the map. A key tracked twice leaks an element that
// nothing will ever remove; a key tracked zero times is invisible to sampled
// cleanup forever. Neither shows up through the public API until the process has
// been running long enough to matter.
func checkOrderInvariant(t *testing.T, store *Memory) {
	t.Helper()

	store.mu.RLock()
	defer store.mu.RUnlock()

	if store.order.Len() != len(store.entries) {
		t.Fatalf("order tracks %d keys, the map holds %d", store.order.Len(), len(store.entries))
	}

	seen := make(map[string]*list.Element, len(store.entries))
	for element := store.order.Front(); element != nil; element = element.Next() {
		key, ok := element.Value.(string)
		if !ok {
			t.Fatalf("order holds a %T, want a key", element.Value)
		}
		if previous, duplicate := seen[key]; duplicate {
			t.Fatalf("key %q is tracked twice (%p and %p)", key, previous, element)
		}
		seen[key] = element

		entry, present := store.entries[key]
		if !present {
			t.Fatalf("order tracks %q, which the map does not hold", key)
		}
		if entry.orderElement != element {
			t.Fatalf("entry %q points at %p, the list holds it at %p",
				key, entry.orderElement, element)
		}
	}

	for key := range store.entries {
		if _, tracked := seen[key]; !tracked {
			t.Fatalf("the map holds %q, which the order does not track", key)
		}
	}
}

// TestAddKeepsTheOrderAndMapInAgreement hammers the write paths against
// overlapping keys and expiries, then checks the invariant the whole eviction
// mechanism rests on. Add was the one writer with no coverage of it.
func TestAddKeepsTheOrderAndMapInAgreement(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	ttls := []time.Duration{0, time.Millisecond, 10 * time.Millisecond, time.Minute, time.Hour}
	for round := 0; round < 400; round++ {
		key := fmt.Sprintf("key-%d", round%40)
		ttl := ttls[round%len(ttls)]

		switch round % 5 {
		case 0, 1:
			if _, err := store.Add(ctx, key, []byte("v"), ttl); err != nil {
				t.Fatalf("Add: %v", err)
			}
		case 2:
			if err := store.Set(ctx, key, []byte("v"), ttl); err != nil {
				t.Fatalf("Set: %v", err)
			}
		case 3:
			if _, err := store.Get(ctx, key); err != nil && err != ErrMiss {
				t.Fatalf("Get: %v", err)
			}
		case 4:
			if err := store.Delete(ctx, key); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		}

		// Advance past the short TTLs so expired replacement is exercised, which
		// is the path where Add has to remove the old element before pushing.
		now = now.Add(2 * time.Millisecond)

		if round%37 == 0 {
			store.PurgeExpired()
		}
		checkOrderInvariant(t, store)
	}

	store.PurgeExpired()
	checkOrderInvariant(t, store)
}

// TestAddReclaimsExpiredEntriesAsItWrites covers the bounded cleanup the type's
// documentation promises for writes. Without it a cache written through Add only
// grows, since the sampled reclamation is the only thing that runs unprompted.
func TestAddReclaimsExpiredEntriesAsItWrites(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	for i := 0; i < 64; i++ {
		if _, err := store.Add(ctx, fmt.Sprintf("stale-%d", i), []byte("v"), time.Second); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	now = now.Add(time.Minute)

	store.mu.RLock()
	before := len(store.entries)
	store.mu.RUnlock()

	// One unrelated write, which must carry some of the cleanup with it.
	if _, err := store.Add(ctx, "fresh", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Add: %v", err)
	}

	store.mu.RLock()
	after := len(store.entries)
	store.mu.RUnlock()

	if after > before {
		t.Fatalf("entries went from %d to %d; Add reclaimed nothing", before, after)
	}
	checkOrderInvariant(t, store)
}

// TestAddTreatsAKeyWithNoExpiryAsTaken covers the zero-TTL half of the freshness
// check. Reading only the deadline would treat an entry that never expires as
// expired, since its deadline is the zero time — quietly overwriting the value a
// caller stored precisely because it should not go away.
func TestAddTreatsAKeyWithNoExpiryAsTaken(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	if _, err := store.Add(ctx, "key", []byte("first"), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	now = now.Add(100 * 365 * 24 * time.Hour)

	stored, err := store.Add(ctx, "key", []byte("second"), time.Minute)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if stored {
		t.Fatal("a key stored with no expiry was treated as free a century later")
	}

	value, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(value) != "first" {
		t.Fatalf("value = %q, want the original", value)
	}
}

// TestAddWithAZeroTTLStoresSomethingThatDoesNotExpire is the other side: the
// entry has to be written without a deadline rather than with one already past.
func TestAddWithAZeroTTLStoresSomethingThatDoesNotExpire(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	if _, err := store.Add(ctx, "key", []byte("v"), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	now = now.Add(time.Hour)

	if _, err := store.Get(ctx, "key"); err != nil {
		t.Fatalf("Get after an hour: %v, want the value to still be there", err)
	}
	if purged := store.PurgeExpired(); purged != 0 {
		t.Fatalf("PurgeExpired removed %d entries with no expiry", purged)
	}
}

// TestClaimPassesTheLeaseToTheStore is what makes a claim time-limited. Nothing
// observed the TTL before this: a claim helper that dropped it would hold every
// key for the life of the process, and one that shortened it would let a retry
// through as a fresh request — the failure the claim exists to prevent.
func TestClaimPassesTheLeaseToTheStore(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	claimed, err := Claim(ctx, store, "delivery:1", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("Claim = %v, %v", claimed, err)
	}

	now = now.Add(59 * time.Second)
	claimed, err = Claim(ctx, store, "delivery:1", time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed {
		t.Fatal("the claim was released before its lease ran out")
	}

	now = now.Add(2 * time.Second)
	claimed, err = Claim(ctx, store, "delivery:1", time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed {
		t.Fatal("the claim outlived its lease")
	}
}

// TestAddOverAnExpiredEntryOutsideTheSampleRemovesItsElement is the narrow case
// the hammer misses. Sampled cleanup only looks at the front of the list, so an
// expired key further back is not reclaimed by it — and an Add over that key has
// to remove the old element itself, or the map holds one entry while the list
// holds two, and the extra one is never removed by anything.
func TestAddOverAnExpiredEntryOutsideTheSampleRemovesItsElement(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	now := time.Now()
	store.now = func() time.Time { return now }

	// Well past the sample window, so cleanup cannot reach what follows.
	for i := 0; i < memoryCleanupSampleSize*4; i++ {
		if err := store.Set(ctx, fmt.Sprintf("live-%d", i), []byte("v"), time.Hour); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	if _, err := store.Add(ctx, "stale", []byte("first"), time.Minute); err != nil {
		t.Fatalf("Add: %v", err)
	}
	now = now.Add(2 * time.Minute)

	stored, err := store.Add(ctx, "stale", []byte("second"), time.Hour)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !stored {
		t.Fatal("the expired key was treated as taken")
	}

	checkOrderInvariant(t, store)
}

// TestGetRechecksAnExpiredCandidateUnderTheExclusiveLock covers the window the
// unbounded read path exists to handle. The shared lock finds the entry expired,
// releases, and takes the exclusive lock — and by then another goroutine may have
// replaced or removed the key, so the decision has to be made again.
//
// Both interleavings are driven through the injectable clock rather than by
// racing goroutines, which would make the test a coin flip.
func TestGetRechecksAnExpiredCandidateUnderTheExclusiveLock(t *testing.T) {
	t.Run("replaced by a live entry", func(t *testing.T) {
		store := NewMemory()
		ctx := context.Background()

		base := time.Now()
		store.now = func() time.Time { return base }
		if err := store.Set(ctx, "key", []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}

		// Expired to the shared read, live again by the time the exclusive lock is
		// held — the shape of a concurrent Set landing in between.
		reads := 0
		store.now = func() time.Time {
			reads++
			if reads == 1 {
				return base.Add(2 * time.Minute)
			}
			return base
		}

		value, err := store.Get(ctx, "key")
		if err != nil {
			t.Fatalf("Get: %v, want the refreshed value", err)
		}
		if string(value) != "v" {
			t.Fatalf("value = %q", value)
		}
	})

	t.Run("removed entirely", func(t *testing.T) {
		store := NewMemory()
		ctx := context.Background()

		base := time.Now()
		store.now = func() time.Time { return base }
		if err := store.Set(ctx, "key", []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}

		// The second call runs with the exclusive lock held, which is the only
		// point where a concurrent Delete could have completed. Removing the entry
		// from inside it reproduces that without a second goroutine.
		reads := 0
		store.now = func() time.Time {
			reads++
			if reads == 2 {
				if entry, ok := store.entries["key"]; ok {
					store.deleteLocked("key", entry)
				}
			}
			return base.Add(2 * time.Minute)
		}

		if _, err := store.Get(ctx, "key"); err != ErrMiss {
			t.Fatalf("Get = %v, want ErrMiss", err)
		}
		checkOrderInvariant(t, store)
	})
}

// TestDeletingTheCursorElementKeepsCleanupAdvancing covers the one piece of
// bookkeeping the cursor adds. A removed element has its links cleared, so a
// cursor left pointing at one falls back to the front — and the keys behind the
// front are then never visited.
//
// The layout makes that observable: the front holds keys that never expire, so a
// sample that restarts there reclaims nothing, while one that continues reaches
// the expired tail.
func TestDeletingTheCursorElementKeepsCleanupAdvancing(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	base := time.Now()
	store.now = func() time.Time { return base }

	for i := 0; i < memoryCleanupSampleSize; i++ {
		if err := store.Set(ctx, fmt.Sprintf("permanent-%02d", i), []byte("v"), 0); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	for i := 0; i < memoryCleanupSampleSize; i++ {
		if err := store.Set(ctx, fmt.Sprintf("stale-%02d", i), []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	// One sample over the permanent front, leaving the cursor at the tail.
	store.mu.Lock()
	store.purgeSampleLocked(base)
	cursor := store.cursor
	store.mu.Unlock()
	if cursor == nil {
		t.Fatal("the cursor did not advance past the front")
	}
	if key := cursor.Value.(string); !strings.HasPrefix(key, "stale-") {
		t.Fatalf("the cursor is at %q, want the expired tail", key)
	}

	// Delete exactly the key the cursor stands on, as an application dropping an
	// idempotency key would.
	if err := store.Delete(ctx, cursor.Value.(string)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	checkOrderInvariant(t, store)

	base = base.Add(2 * time.Minute)
	before := store.Len()

	store.mu.Lock()
	store.purgeSampleLocked(base)
	store.mu.Unlock()

	if store.Len() == before {
		t.Fatalf("cleanup reclaimed nothing from %d entries; the cursor fell back "+
			"to the front, where nothing expires", before)
	}
	checkOrderInvariant(t, store)
}

// TestRegisterMemoryStopHonoursItsDeadline covers the shutdown timeout. A janitor
// blocked behind a slow operation must not hold the whole shutdown open past the
// deadline the application was given.
//
// The block is produced by holding the cache lock, which is what a long
// reclamation over a large cache does from the janitor's point of view.
func TestRegisterMemoryStopHonoursItsDeadline(t *testing.T) {
	store := NewMemory()
	app := ossein.New()

	if err := RegisterMemory(app, store, WithCleanupInterval(time.Millisecond)); err != nil {
		t.Fatalf("RegisterMemory: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The janitor's next tick blocks here.
	store.mu.Lock()
	time.Sleep(20 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := app.Stop(stopCtx)
	store.mu.Unlock()

	if err == nil {
		t.Fatal("Stop reported success while the janitor was still blocked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the deadline", err)
	}
}

// TestDefaultCleanupIntervalIsShortEnoughToMatter pins the janitor's default. The
// interval is the upper bound on how long an idle process holds expired entries,
// so a value chosen for tidiness rather than for that number is the wrong one.
func TestDefaultCleanupIntervalIsShortEnoughToMatter(t *testing.T) {
	if defaultCleanupInterval <= 0 {
		t.Fatalf("defaultCleanupInterval = %v", defaultCleanupInterval)
	}
	if defaultCleanupInterval > time.Minute {
		t.Fatalf("defaultCleanupInterval = %v; an idle process would hold expired "+
			"entries that long", defaultCleanupInterval)
	}
}
