# Cache

Ossein provides a small cache contract built from standard Go types. The core
does not require a specific network service, serialization format, or
third-party client.

## Store contract

Backends implement three operations:

```go
type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}
```

`Get` returns `cache.ErrMiss` for missing and expired keys. A zero TTL means
the entry does not expire; negative TTL values return `cache.ErrInvalidTTL`.
Keys cannot be empty, and `Delete` is idempotent.

The byte slice passed to `Set` remains owned by the caller: a backend must not
let later caller mutations change the stored value. A successful `Get` returns
bytes owned by the caller. Backend implementations must copy buffers when
their storage strategy requires it.

The byte-oriented boundary maps directly to in-memory, Redis, Memcached, and
other cache backends without forcing application models into the interface.

## In-memory driver

Create a concurrency-safe process-local store:

```go
store := cache.NewMemory()
```

The memory driver copies values when storing and loading them, so callers
cannot mutate cached data without another `Set`. It removes an expired entry
when that key is read and inspects a bounded sample for other expired entries
on that path and on every write. The sample advances on its own cursor, so
successive operations continue where the last one stopped and every resident
key is visited as cache activity continues, while per-operation cleanup work
stays bounded. Applications can request a complete pass and inspect the result
with:

```go
removed := store.PurgeExpired()
```

The zero value of `cache.Memory` is also ready for use.

The driver is appropriate for local development, tests, and data that may be
different in every application process. It is not a replacement for a shared
cache when an application runs on multiple instances.

By default it has no entry limit: permanent entries and a high number of
simultaneously live TTL entries consume as much memory as their arrival rate
times their lifetime. `WithMaxEntries` turns that into a budget:

```go
store := cache.NewMemory(cache.WithMaxEntries(10_000))
```

A write that would exceed the cap evicts the least recently used key. Eviction
is by age of use, not by expiry: cleaning a sample happens first, so dead
entries are usually gone before eviction runs, but a live key can still be
discarded while dead ones remain elsewhere. Discarding live data is what makes
it a bound.

A bound changes how reads are tracked. Unbounded, a read takes a shared lock
and the ordering list is only insertion order. Bounded, a read has to record
that it happened — otherwise eviction cannot tell hot keys from cold ones — so
it takes the exclusive lock. That cost is why the bound is opt-in.

**A bound and a claim do not mix.** `Claim` and `Once` depend on a key staying
present for its lease, and eviction does not know a key is a claim: dropping one
early lets the work it guards run a second time. Leave a store holding
idempotency keys, run-once markers, or leases unbounded, and keep a bounded one
for data that is merely expensive to recompute.

## Raw values

```go
err := store.Set(ctx, "greeting", []byte("hello"), 5*time.Minute)
if err != nil {
	return err
}

value, err := store.Get(ctx, "greeting")
if errors.Is(err, cache.ErrMiss) {
	// Load the value from its source.
}
```

## Typed JSON helpers

Applications can keep their domain types at the call site:

```go
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

err := cache.SetJSON(ctx, store, "users:42", user, 10*time.Minute)

user, err := cache.GetJSON[User](ctx, store, "users:42")
```

`RememberJSON` loads and stores a value on a cache miss, incompatible cached
JSON, or a recoverable backend read error:

```go
user, err := cache.RememberJSON(
	ctx,
	store,
	"users:42",
	10*time.Minute,
	func(ctx context.Context) (User, error) {
		return repository.Find(ctx, 42)
	},
)
```

`GetJSON` is strict: incompatible cached data returns an error matching
`cache.ErrDecode`. `RememberJSON` is self-healing: it treats that error as a
recoverable cache failure, loads the source value, and attempts to replace the
entry. Prefer versioned keys such as `users:v2:42` for schema changes that may
remain valid JSON but have different semantics.

Every logical key must also have one stable value type. If two call sites use
the same key with incompatible types, they can repeatedly replace each other's
entries and effectively disable caching. A persistent stream of
`cache.ErrDecode` reports from `WithErrorHandler` is a strong signal of this
key-design error.

`Remember` provides the same workflow for raw bytes. Both remember helpers are
fail-open for cache infrastructure errors: if the cache cannot be read, they
call the loader; if a cache fill fails after a successful load, they return the
loaded value. JSON encoding errors, loader errors, and context cancellation
remain errors. Use
`WithErrorHandler` to retain observability without making the cache a hard
dependency:

```go
value, err := cache.Remember(
	ctx,
	store,
	"greeting",
	time.Minute,
	loadGreeting,
	cache.WithErrorHandler(func(ctx context.Context, err error) {
		slog.ErrorContext(ctx, "cache degraded", "error", err)
	}),
)
```

Direct operations (`Get`, `Set`, `GetJSON`, and `SetJSON`) remain strict and
return backend errors to the caller.

The remember helpers deliberately do not hide stampede protection or
distributed locking. Applications that need singleflight behavior should
coordinate loaders explicitly at their service boundary.

## Dependency injection

`RegisterMemory` binds the driver behind the interface and reclaims expired
entries on a schedule:

```go
store := cache.NewMemory()
if err := cache.RegisterMemory(app, store, cache.WithCleanupInterval(time.Minute)); err != nil {
	return err
}
```

The schedule covers what traffic cannot: cleanup otherwise runs only on reads
and writes, so a process that goes quiet after a burst holds every expired entry
until its next write. The janitor starts with the application and is stopped and
waited for during shutdown.

A driver can also be registered as an ordinary constructor when no janitor is
wanted:

```go
if err := ossein.ProvideAs[cache.Store](app, cache.NewMemory); err != nil {
	return err
}
```

Services can then request the ordinary interface in their constructors:

```go
func NewUserService(store cache.Store) *UserService {
	return &UserService{cache: store}
}
```

Future distributed drivers will implement the same contract without changing
service constructors.

## Optional backend capabilities

`Store` intentionally stays limited to portable cache-aside operations.
Backend-specific atomic features such as set-if-absent, counters, or
compare-and-swap will use small optional capability interfaces discovered with
type assertions. They will not be added to `Store`, so third-party
implementations of the core contract will remain source-compatible.

Each capability will define its own atomicity, TTL, and overflow guarantees
before a distributed driver relies on it.
