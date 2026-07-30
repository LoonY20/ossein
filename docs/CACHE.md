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
when that key is read and inspects a bounded round-robin sample for other
expired entries on that path and on every `Set`. This deterministic amortized
cleanup visits every resident key as cache activity continues while keeping
per-operation cleanup work bounded. Applications can request a complete pass
and inspect the result with:

```go
removed := store.PurgeExpired()
```

The zero value of `cache.Memory` is also ready for use.

The driver is appropriate for local development, tests, and data that may be
different in every application process. It is not a replacement for a shared
cache when an application runs on multiple instances. It has no hard entry or
memory limit: permanent entries and a high number of simultaneously live TTL
entries can still consume unbounded memory. Use bounded key cardinality or a
backend with an eviction policy when that risk matters.

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

Register the concrete driver behind the interface:

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
