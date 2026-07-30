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

The byte-oriented boundary maps directly to in-memory, Redis, Memcached, and
other cache backends without forcing application models into the interface.

## In-memory driver

Create a concurrency-safe process-local store:

```go
store := cache.NewMemory()
```

The memory driver copies values when storing and loading them, so callers
cannot mutate cached data without another `Set`. Expired entries are removed
lazily when they are read. The zero value of `cache.Memory` is also ready for
use.

The driver is appropriate for local development, tests, and data that may be
different in every application process. It is not a replacement for a shared
cache when an application runs on multiple instances.

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

`RememberJSON` loads and stores a value only on a cache miss:

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

`Remember` provides the same workflow for raw bytes. These helpers deliberately
do not hide stampede protection or distributed locking. Applications that need
singleflight behavior should coordinate loaders explicitly at their service
boundary.

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
