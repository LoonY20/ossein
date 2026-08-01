# Ossein

[![CI](https://github.com/LoonY20/ossein/actions/workflows/ci.yml/badge.svg)](https://github.com/LoonY20/ossein/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/LoonY20/ossein.svg)](https://pkg.go.dev/github.com/LoonY20/ossein)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> Laravel-like developer experience. Idiomatic Go underneath.

Ossein is an experimental open-source Go framework focused on productive backend development without hiding the language that powers it.

The goal is simple: keep the clarity, performance, explicitness, and tooling of Go while reducing the repetitive setup work developers usually do when starting a production backend.

## Status

Ossein v0.2.0 adds the first production module — a backend-neutral cache
contract with a hardened in-memory driver and fail-open remember helpers — on
top of the HTTP core, developer CLI, and driver-neutral data workflow. SQLite
migrations gained multi-process safety, completing concurrency protection for
all three dialects, and the experimental `ossein wire` command generates
explicit service wiring for zero-reflection startup.

The API remains pre-1.0 and is not yet recommended for production systems
without careful evaluation. Further production modules remain part of the
roadmap.

## Philosophy

Ossein is built around one rule:

> Convenience without hiding Go.

That means:

- standard Go concepts stay visible: `context.Context`, `error`, `http.Handler`, interfaces, structs, and the standard library;
- developer experience can be opinionated, but application code should remain understandable to a Go developer who has never used Ossein;
- framework components should be replaceable instead of locking applications into a private ecosystem;
- runtime magic and reflection should be kept to a minimum;
- performance and debuggability should not be traded away for syntactic convenience;
- Ossein should provide good defaults while preserving escape hatches to ordinary Go.

## Current features

Today Ossein intentionally stays small:

- standard-library `net/http` foundation;
- Ossein handlers with `func(*ossein.Context) error`;
- direct access to the underlying `http.Request`, `http.ResponseWriter`, and `context.Context`;
- `GET`, `POST`, `PUT`, `PATCH`, and `DELETE` route helpers;
- Go 1.22+ `ServeMux` route patterns and path parameters;
- nested route groups with group middleware;
- named routes, URL generation, and a route registry;
- application-wide standard Go middleware;
- centralized JSON error rendering;
- explicit `HTTPError` helpers for expected failures;
- JSON request binding with unknown-field rejection, Content-Type validation,
  and a configurable body size limit;
- raw body access through `Context.Body` that composes with `BindJSON`, for
  signed payloads such as webhook HMACs;
- form, multipart, and query-string binding through explicit `FormBindable` and
  `QueryBindable` contracts, sharing one set of typed accessors, with file
  uploads and no reflection on the request path;
- response status and size tracking through a wrapped `http.ResponseWriter`;
- validation through an explicit `Validate() error` contract;
- field-level `ValidationError` responses;
- JSON `404` and `405` responses with a preserved `Allow` header, replaceable
  through `SetNotFoundHandler` and `SetMethodNotAllowedHandler`;
- `WriteError` for rendering the application's error contract from plain
  `net/http` middleware, plus the exported `ErrorEnvelope`;
- a `middleware` package with panic recovery, access logging, security headers,
  CORS with preflight handling, and a request timeout that preserves response
  tracking;
- typed environment configuration with defaults and required values, including
  comma-separated lists, `encoding.TextUnmarshaler` types, and URLs;
- optional dependency-free `.env` loading with exported-variable precedence;
- standard-library `log/slog` integration;
- automatic request IDs and request-scoped loggers;
- startup and shutdown lifecycle hooks;
- configurable graceful-shutdown timeout;
- constructor-based dependency wiring;
- singleton and transient service lifetimes;
- interface bindings and startup graph validation;
- driver-neutral `database/sql` configuration, lifecycle, and DI registration;
- transactional, dialect-aware SQL migrations with filesystem and embedded
  sources;
- PostgreSQL advisory locks, MySQL named locks, SQLite immediate write
  transactions, configurable lock timeouts, and real-database concurrency
  coverage;
- transaction helper with explicit `*sql.Tx`;
- driver-neutral SQL error classification: unique violations, broken references,
  deadlocks, and serialization failures, with a hook for an unknown driver;
- ordered transactional seeders and `ossein db:seed`;
- generic test factories with sequences, states, and persistence hooks;
- composed application command handling for migrations and seeders;
- backend-neutral cache contract, concurrency-safe in-memory driver with an
  optional size bound and LRU eviction, a lifecycle-managed janitor, typed
  JSON/remember helpers, and atomic claims for idempotency keys and run-once work;
- a bounded in-process job queue with a worker pool, per-name handlers, retries
  with backoff, load shedding through sentinel errors, and a drain on shutdown,
  behind an `Enqueuer` interface a durable driver can replace;
- response helpers for JSON, plain text, HTML, bytes, streams, redirects, files,
  downloads, and server-sent events, all preserving status and size tracking;
- blocking server start with `Run`, with conservative default timeouts;
- context-aware graceful shutdown with `RunContext`;
- `Serve`, `ServeTLS`, and `ServeListener` for a caller-owned `*http.Server`:
  timeouts, TLS, limits, and `BaseContext` stay yours while Ossein runs the
  lifecycle;
- native `http.Handler` escape hatches;
- `ossein new`, hot-reloading `ossein dev`, `ossein routes`, and application-owned
  migration commands;
- controller, middleware, and request generators;
- a minimal application starter with a health endpoint and test;
- race-tested core and CLI with an enforced 85% coverage floor;
- no third-party runtime dependencies.

## Quick start

Install the CLI:

```bash
go install github.com/LoonY20/ossein/cmd/ossein@latest
```

For reproducible installation, pin the current release:

```bash
go install github.com/LoonY20/ossein/cmd/ossein@v0.2.0
```

Create and run an application:

```bash
ossein new hello-ossein
cd hello-ossein
go mod tidy
ossein dev
```

Inspect its routes:

```bash
ossein routes
```

Or add Ossein to an existing module:

Install the module:

```bash
go get github.com/LoonY20/ossein@v0.2.0
```

Create an application:

```go
package main

import (
    "log"
    "net/http"

    "github.com/LoonY20/ossein"
)

func main() {
    app := ossein.New()

app.Get("/users/{id}", func(ctx *ossein.Context) error {
        ctx.Logger().Info("show user", "user_id", ctx.Param("id"))

        return ctx.JSON(http.StatusOK, map[string]string{
            "id": ctx.Param("id"),
        })
    }).Named("users.show")

    if err := app.Run(":8080"); err != nil {
        log.Fatal(err)
    }
}
```

Build a URL from its route name:

```go
path, err := app.URL("users.show", map[string]string{"id": "42"})
```

Every request receives an `X-Request-ID` response header. The same ID is available through `ctx.RequestID()` and is automatically attached to `ctx.Logger()` together with the request method and path.

## Service container

Ossein can wire ordinary Go constructors without field injection or hidden service lookups.

```go
type UserRepository interface {
    Find(ctx context.Context, id int64) (*User, error)
}

type PostgresUserRepository struct{}

type UserService struct {
    users UserRepository
}

func NewPostgresUserRepository() *PostgresUserRepository {
    return &PostgresUserRepository{}
}

func NewUserService(users UserRepository) *UserService {
    return &UserService{users: users}
}
```

Register the interface binding and dependent service:

```go
if err := ossein.ProvideAs[UserRepository](app, NewPostgresUserRepository); err != nil {
    return err
}

if err := app.Provide(NewUserService); err != nil {
    return err
}
```

Resolve the root service when wiring an application boundary:

```go
users, err := ossein.Resolve[*UserService](app)
if err != nil {
    return err
}
```

Singleton is the default lifetime. Transient services are explicit:

```go
app.Provide(NewRequestHandler, ossein.Transient())
```

Existing values can also be registered:

```go
ossein.Instance(app, logger)
```

`App.Start` validates the dependency graph before startup hooks run, so missing registrations and circular dependencies fail before the server starts.

Ossein uses reflection only at the service-container boundary to inspect constructor signatures and construct registered services. Dependencies remain visible in normal Go function signatures; there is no struct-field injection and no request-time container lookup hidden by the framework.

## Configuration

Ossein can load a typed configuration struct directly from environment variables:

```go
type Config struct {
    App struct {
        Name  string `env:"APP_NAME" required:"true"`
        Debug bool   `env:"APP_DEBUG" default:"false"`
    }

    HTTP struct {
        Address string `env:"HTTP_ADDRESS" default:":8080"`
    }
}

config, err := ossein.LoadConfig[Config]()
if err != nil {
    return err
}
```

For local development, load an optional `.env` before parsing the typed
configuration:

```go
if err := ossein.LoadEnvFileIfExists(".env"); err != nil {
	return err
}
config, err := ossein.LoadConfig[Config]()
```

Already exported environment variables take precedence over `.env`, which
keeps deployment configuration authoritative.

Beyond strings, booleans, integers, unsigned integers, floats, `time.Duration`,
and nested structs, three shapes cover most of what is left:

```go
type Config struct {
    // Lists, from a comma-separated value. Entries are trimmed, and empty ones
    // dropped, so a trailing comma or a value spaced out for readability is fine.
    Origins  []string        `env:"ALLOWED_ORIGINS" default:"http://localhost:3000"`
    Ports    []int           `env:"PORTS"`
    Timeouts []time.Duration `env:"TIMEOUTS"`

    // Anything implementing encoding.TextUnmarshaler parses itself, which covers
    // slog.Level, net/netip addresses, net.IP, time.Time, and your own types.
    Level  slog.Level `env:"LOG_LEVEL" default:"info"`
    Region RegionCode `env:"REGION"`

    // URLs, by value or by pointer.
    BaseURL *url.URL `env:"BASE_URL" default:"http://localhost:8080"`

    // A key or a secret: []byte is the raw value, never split on commas.
    Signing []byte `env:"SIGNING_KEY"`
}
```

A bad list entry says which one it was, counting the fields as written so the number
matches the value an operator is looking at (`element 3: invalid integer "nope"`). A
type that rejects a value in its own `UnmarshalText` fails startup rather than loading
something wrong. `required:"true"` on a list rejects a value that parses to no entries,
so `ORIGINS=,,` is an error rather than an allowlist that denies everything.

An unset list is `nil` and one set to no entries is an empty slice, so an application
can tell "not configured" from "configured empty".

Two shapes are worth knowing:

- `byte` is an alias for `uint8`, so `[]uint8` is the same type as `[]byte` and is also
  the raw value. Use a wider element type for a list of small numbers.
- a struct without an `env` tag is a nested group of settings, which means a
  self-parsing struct type such as `time.Time` or `url.URL` needs its tag; without one
  it is descended into and its own parsing never runs.

URLs are checked for two values `url.Parse` accepts but that are silently useless: an
empty one, and one like `localhost:8080` where a dropped scheme makes `localhost` the
scheme and leaves no host, which `JoinPath` then refuses to extend. A URL with a path
and no scheme stays legal, since a base path is a real thing to configure.

Maps are deliberately not supported: `KEYS=a=1,b=2` needs two separator conventions,
both arbitrary, so that encoding stays the application's to choose.

Reflection is limited to configuration loading and the service container, and is not
part of Ossein's request runtime.

For tests or custom environment sources, use `LoadConfigFromEnv` with an explicit lookup function.

## Structured logging and request IDs

Ossein uses the standard library `log/slog` instead of defining a custom logging abstraction:

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

app := ossein.New(
    ossein.WithLogger(logger),
)
```

Inside an Ossein handler:

```go
app.Get("/", func(ctx *ossein.Context) error {
    ctx.Logger().Info("request handled")
    return ctx.NoContent(http.StatusNoContent)
})
```

The request-scoped logger automatically includes:

```text
request_id=<id>
method=GET
path=/
```

Native handlers can access the same values without using an Ossein request type:

```go
requestID := ossein.RequestIDFromContext(r.Context())
logger := ossein.LoggerFromContext(r.Context())
```

Applications may change the header or request ID format with `WithRequestIDHeader` and `WithRequestIDGenerator`.

## Lifecycle

Startup hooks run in registration order. Shutdown hooks run in reverse order so resources can be released like a stack:

```go
app.OnStart(func(ctx context.Context) error {
    return database.Ping(ctx)
})

app.OnStop(func(ctx context.Context) error {
    database.Close()
    return nil
})
```

`RunContext` executes the lifecycle around the HTTP server and performs graceful shutdown when its context is cancelled. The timeout can be configured with `WithShutdownTimeout`.

`Run` and `RunContext` build the server for you with a `ReadHeaderTimeout` and
an `IdleTimeout`, so an unattended server cannot be held open by connections
that never finish a request. `ReadTimeout` and `WriteTimeout` are deliberately
left unset, because a write deadline would cut off server-sent events and long
downloads.

When an application needs to own those settings — or `MaxHeaderBytes`,
`BaseContext`, or `ErrorLog` — pass a configured server to `Serve` instead. Only
`Handler` is filled in, and only when nil:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

err := app.Serve(ctx, &http.Server{
    Addr:              ":8080",
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       30 * time.Second,
    IdleTimeout:       60 * time.Second,
    MaxHeaderBytes:    1 << 16,
})
```

`Serve` runs the same lifecycle as `Run`: service validation, start hooks,
graceful shutdown when the context is cancelled, then stop hooks.

Two variants cover the rest. As in `net/http`, the protocol follows the method
you call rather than a struct field, so setting `TLSConfig` alone never turns a
plain server into an HTTPS one:

```go
// HTTPS from certificate files, or from server.TLSConfig when both paths are empty.
err := app.ServeTLS(ctx, server, "cert.pem", "key.pem")

// An already-bound listener: socket activation, an ephemeral test port, or TLS
// composed with the standard library.
err := app.ServeListener(ctx, server, tls.NewListener(listener, tlsConfig))
```

## Caching

`cache.Store` is a three-method contract — `Get`, `Set`, `Delete` — with a
concurrency-safe in-memory driver and typed helpers:

```go
store := cache.NewMemory(cache.WithMaxEntries(10_000))

user, err := cache.RememberJSON(ctx, store, "user:42", time.Minute,
    func(ctx context.Context) (User, error) { return users.Find(ctx, 42) })
```

`cache.RegisterMemory(app, store)` binds it as a `Store` and reclaims expired
entries on a schedule:

```go
if err := cache.RegisterMemory(app, store, cache.WithCleanupInterval(time.Minute)); err != nil {
    return err
}
```

The schedule matters because reclamation is otherwise driven by traffic: every read
and write cleans a small sample, so a process that goes quiet after a burst holds
every expired entry until its next write. With day-long idempotency keys that is a
day of memory nothing will ask for again.

`WithMaxEntries` is the other half. Without a bound the driver does not leak, but
resident memory is TTL times arrival rate, which is not a number anyone chose. With
one, a write that would exceed the cap evicts the least recently used key. Eviction
goes by age of use, not by expiry: every write cleans a sample first, so dead entries
are usually gone before it runs, but a live key can still be discarded while dead ones
remain elsewhere. Discarding live data is what makes it a bound.

A bound and a claim do not mix. `Claim` and `Once` need a key to stay present for its
lease, and eviction does not know a key is a claim — dropping one early lets the work
it guards run a second time. Leave a store holding idempotency keys unbounded, with the
janitor to reclaim expired ones, and keep a bounded store for data that is merely
expensive to recompute.

A bound changes how reads are tracked: unbounded, a read takes a shared lock and the
ordering list is only a cleanup cursor; bounded, a read has to record that it
happened, or eviction cannot tell hot keys from cold ones, so it takes the exclusive
lock. That cost is why the bound is opt-in rather than a default.

Doing something at most once needs more than `Get` and `Set`:

```go
accepted, err := cache.Once(ctx, store, "delivery:"+id, 24*time.Hour,
    func(ctx context.Context) error {
        return work.EnqueueJSON(ctx, "webhook.delivery", delivery)
    })
if err != nil {
    return err
}
if !accepted {
    return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
}
```

That is an idempotency key; the same shape is a run-once job or a lease. The
`Get`-then-`Set` it replaces is racy by construction: two callers both miss, both
store, and both proceed. The window is sub-microsecond against a local map, so it
rarely appears in testing, and routine against a network cache — which is exactly
where duplicate work costs something.

`Once` releases the key when the work returns an error, which is the part worth
having a helper for. A claim taken before the work and kept when the work does not
happen turns "this might run twice" into "this can never run again": a webhook
receiver that sheds a delivery with `503`, asking the provider to resend, then
answers the resend as a duplicate and loses it for the whole window. Trading
duplicate work for lost work is the worse bargain, and it is not obvious from the
call site.

`cache.Claim` is the primitive underneath, for work whose lifetime is not a
function call. Releasing it is then the caller's job.

Both need a store implementing `cache.Adder` and report `ErrNotAtomic` otherwise,
rather than falling back to the racy form. A guarantee that quietly is not one
produces duplicate processing that looks like an application bug; an error at the
call site names the real problem. The in-memory driver implements it — but a
decorator around a store erases the capability unless it forwards `Add`.

A claim is a lease, so its TTL must be positive. One that never expires is a
tombstone: a crash between claiming and working would block that key until
something deletes it by hand.

## Background jobs

Work that a request should not wait for — a webhook fan-out, a thumbnail, an
email — belongs behind a queue. `queue.Memory` is a bounded in-process queue with
a worker pool, and `queue.Register` ties it into the application lifecycle:

```go
work := queue.NewMemory(
    queue.WithLogger(logger),
    queue.WithWorkers(4),
    queue.WithBuffer(256),
    queue.WithMaxAttempts(3),
)

work.Handle("email.welcome", func(ctx context.Context, job queue.Job) error {
    var payload WelcomeEmail
    if err := json.Unmarshal(job.Payload, &payload); err != nil {
        return err
    }
    return mailer.Send(ctx, payload)
})

// Starts the workers on app start and drains in-flight jobs on shutdown, and
// registers the queue as queue.Enqueuer in the container.
if err := queue.Register(app, work); err != nil {
    return err
}
```

A handler takes a `context.Context` and a `Job`, the same shape as an HTTP
handler, and `Job.Payload` is `[]byte`, so the encoding stays the
application's choice. `EnqueueJSON` covers the common one:

```go
func (h *Signups) Create(c *ossein.Context) error {
    // ... create the user ...

    if err := h.work.EnqueueJSON(c.Context(), "email.welcome", WelcomeEmail{
        To: user.Email,
    }); err != nil {
        return err
    }
    return c.JSON(http.StatusAccepted, user)
}
```

Handlers depend on the `queue.Enqueuer` interface rather than on `*queue.Memory`,
so a durable driver can replace it later without touching call sites:

```go
type Signups struct {
    work queue.Enqueuer
}
```

A full queue is back-pressure, not a server fault, and a stopped one is not
either. Both are sentinel errors, so an application decides the status code:

```go
if err := h.work.Enqueue(ctx, job); err != nil {
    if errors.Is(err, queue.ErrFull) || errors.Is(err, queue.ErrClosed) {
        return ossein.NewHTTPError(http.StatusServiceUnavailable, "busy",
            "Service is busy; retry later").WithCause(err)
    }
    return err
}
```

A job that returns an error is retried with a backoff, up to `WithMaxAttempts`,
after which `WithFailureHandler` sees it — that is where a dead-letter table or an
alert goes. A job that panics becomes an error on the same path instead of taking
its worker down. A job name with no registered handler is refused by `Enqueue`,
where the caller can still return an error, rather than disappearing into a log
line.

The failure handler also sees a job whose retries were cut short because shutdown
began, which is a different thing from a job that is dead. `ErrAbandoned` separates
them, and filing the two together means a dead-letter entry for live work on every
deploy:

```go
queue.WithFailureHandler(func(ctx context.Context, job queue.Job, err error) {
    if errors.Is(err, queue.ErrAbandoned) {
        // Still retryable; its source will resubmit it.
        return
    }
    deadLetters.Record(ctx, job, err)
})
```

`Stats` reports queue depth and processed, failed, abandoned, and refused counts,
which makes a useful health endpoint.

### Shutdown

A job runs under a context that is *not* cancelled by a graceful drain — that is
the point of draining: the shutdown waits for the job rather than interrupting it.
It is cancelled when the drain runs out of time, which is the signal that finishing
gracefully is no longer available:

```go
work.Handle("report.build", func(ctx context.Context, job queue.Job) error {
    for _, chunk := range chunks {
        if err := ctx.Err(); err != nil {
            return err // shutdown gave up waiting; stop cleanly
        }
        ...
    }
})
```

`Stop` reports what shutdown could not finish, so a dropped job is never mistaken
for a clean exit: an incomplete drain and accepted work that no worker ever ran are
both errors. Since `Register` returns the queue's `Stop` as a lifecycle hook, that
error surfaces from the application's own shutdown.

Register the queue *after* whatever its jobs depend on. Stop hooks run in reverse,
so the workers then finish before the database closes underneath them — for a drain
that completes. If it times out, cancellation is all a job gets, which is why a job
should honor the context it is given.

In-process means in-memory: pending jobs do not survive a crash or a kill that
outruns the shutdown timeout. That is the right trade-off for work that can be
retried from its source — a webhook the provider will redeliver — and the wrong
one for work that must not be lost, which needs a durable driver behind the same
`Enqueuer` interface.

### Correlating a job with the request that enqueued it

`Enqueue` copies the request ID out of the context onto the job, and the worker puts
it back, so both halves of an asynchronous request appear in the log under one ID:

```
level=INFO msg="request completed" request_id=8f7aab71 method=POST path=/webhooks status=202 bytes=48 duration_ms=3
level=INFO msg="delivering downstream" request_id=8f7aab71 job=webhook.delivery attempt=1 worker=0
```

`ossein.RequestIDFromContext` works inside a job handler for the same reason. The ID
travels on the `Job`, so a durable driver keeps the connection across a restart, and
`Job.RequestID` can be set explicitly for work whose origin the context does not know —
a replay, or a job restored from storage. Work with no origin carries no ID rather than
an empty one, which would read as an ID that was lost.

Background work started from a handler rather than a queue keeps its identity through
`context.WithoutCancel`: the request ID and logger are context values, so they survive,
and only the cancellation that fires when the response completes is dropped.

```go
detached := context.WithoutCancel(c.Context())
go func() { report(detached) }()
```

Work with no request at all still needs a logger. `ossein.ContextWithLogger` puts one
into a context, and `ossein.ContextWithRequestID` an identity, so a scheduled task logs
the way the rest of the application does:

```go
ctx := ossein.ContextWithLogger(context.Background(), logger)
```

## Standard middleware

The `middleware` package provides what a service needs but should not have to
write. Register it outermost first:

```go
import "github.com/LoonY20/ossein/middleware"

app.Use(
    middleware.AccessLog(middleware.SkipPaths("/healthz", "/readyz")),
    middleware.Recover(),
    middleware.SecurityHeaders(),
)
```

`AccessLog` goes outside `Recover` deliberately: a middleware only observes a
status written below it, so the other order logs a panicking request with the
status it had *before* recovery rather than the `500` the client received.

`Recover` turns a panic into a structured `500` through the application's
`ErrorHandler`, so it matches every other error the API reports. The panic value
is never sent to the client; it is logged with a stack trace through the
request-scoped logger. A response already committed is left alone, and
`http.ErrAbortHandler` still passes through.

`AccessLog` writes one line per request, including a request that panicked, using
the status and size Ossein already tracks — the values actually sent, including
those written by the error handler or the not-found fallback. The level follows the
outcome: `5xx` at error, `4xx` at warn, the rest at info. A hijacked connection,
such as a websocket upgrade, never reaches the tracked writer and is reported as an
uncommitted response.

`SecurityHeaders` sets `X-Content-Type-Options`, `X-Frame-Options`, and
`Referrer-Policy` before the handler runs, so error responses carry them too. A
value already set is never overwritten, and `SecurityHeaderValues` overrides or
adds one — an empty value removes a default:

```go
middleware.SecurityHeaders(middleware.SecurityHeaderValues(map[string]string{
    "X-Frame-Options":           "SAMEORIGIN",
    "Strict-Transport-Security": "max-age=63072000",
}))
```

`Content-Security-Policy` and `Strict-Transport-Security` are deliberately not
defaults: a useful CSP depends on what the application serves, and HSTS sent from
a host that is not fully HTTPS causes lasting problems.

`Timeout` bounds how long a request may take and answers `504` through the error
handler. Prefer it over `http.TimeoutHandler`, which substitutes a buffer for the
response writer: inside it a handler can no longer reach `*ossein.ResponseWriter`,
so the committed-response guard, the access log's status, and flushing all stop
working, and the timeout body is plain text outside the error contract.

```go
app.Group("/api", func(api *ossein.Router) {
    api.Use(middleware.Timeout(10 * time.Second))
    // ...
})
```

The request context is cancelled at the deadline, so a handler that honours
cancellation returns on its own; one that does not keeps running in the background
with its writes discarded. A response already committed is left alone, so a
streaming handler that overruns keeps what the client received, as is a connection
the handler hijacked. A cancellation for any other reason — a client disconnecting,
say — is not reported as a timeout.

The handler writes headers to a private map that is copied to the response when it
commits. That is the one thing not shared with the real response, because a header
map written from two goroutines risks a fatal concurrent map write when a handler
finishes near the deadline.

Scope it to a group rather than applying it to long-lived streaming routes, and
register it inside `Recover`, so a panic it forwards from the handler's goroutine is
still caught.

`CORS` answers cross-origin preflight requests and adds the headers a browser needs:

```go
app.Use(middleware.CORS(middleware.CORSOptions{
    AllowedOrigins:   []string{"https://app.example.com"},
    AllowedMethods:   []string{http.MethodGet, http.MethodPost},
    AllowCredentials: true,
    MaxAge:           10 * time.Minute,
}))
```

Register it with `App.Use` rather than on a group, and inside `AccessLog`. A
preflight is an `OPTIONS` request that matches no route, so it must be answered
before routing — group middleware does not run for a request that matches no route in
the group, and a log registered below CORS never sees a preflight at all.

A request with no `Origin` passes through untouched. An origin that is not allowed
also passes through, without the headers that let a browser read the response, since
enforcement is the browser's job. `Vary` is appended rather than replaced, so a value
set elsewhere survives.

**CORS is not CSRF protection.** A simple cross-origin request needs no preflight, so
it reaches the handler and runs whatever the browser then does with the response. CORS
governs who may *read* a response, not who may cause one.

Setup panics for configurations that cannot be served safely: one that can never
allow anything, and `AllowCredentials` combined with a wildcard origin, the `null`
origin, or an `AllowOriginFunc` that approves everything. That last check matters most
and is the least obvious: a wildcard with credentials is inert, because browsers
refuse the pair outright, while a function reflecting every origin *works* — so it,
not the wildcard, is the configuration that would hand any site authenticated read
access.

`AllowOriginFunc` covers subdomains and allowlists held elsewhere. Match on the whole
origin rather than a suffix: `https://evil-app.test` ends with `app.test`.

## Route groups

Groups share a path prefix and can carry their own middleware:

```go
app.Group("/api", func(api *ossein.Router) {
    api.Use(authMiddleware)

    api.Group("/v1", func(v1 *ossein.Router) {
        v1.Get("/users/{id}", showUser)
    })
})
```

Middleware remains ordinary Go middleware.

## Responses

`Context` writes the common response shapes without dropping to the raw writer, and
every one of them goes through the Ossein response writer, so status and size tracking,
the access log, and the committed-response guard keep working:

```go
c.JSON(http.StatusOK, payload)
c.Text(http.StatusOK, "pong")
c.HTML(http.StatusOK, page)                    // escaping is html/template's job
c.Blob(http.StatusOK, "text/csv", data)
c.Stream(http.StatusOK, "text/csv", reader)    // nothing buffered
c.NoContent(http.StatusNoContent)
c.Redirect(http.StatusFound, target)
```

An unspecified content type becomes `application/octet-stream` rather than being left
for `net/http` to sniff. Every helper refuses to write over a response that was already
sent, which otherwise produces one status, one set of headers, and two bodies
concatenated, with a "superfluous response.WriteHeader call" in the server log as the
only symptom.

`Redirect` accepts only a status that actually redirects — 301, 302, 303, 307, 308 — and
rejects a location containing a line break. Go replaces line breaks in a header value
with spaces rather than rejecting them, so response splitting becomes a `Location`
pointing somewhere nobody wrote. It delegates to `http.Redirect`, so a GET receives the
short HTML body that function writes, and a relative location resolves against the
request path.

Files delegate to `net/http`, so range and conditional requests work. A missing file, a
directory, or an unreadable path comes back as an error instead: the handler can fall
back, and the response stays in the application's error contract rather than the
plain-text `404 page not found` that `ServeFile` would write into a JSON API.

```go
c.File("/var/lib/app/report.pdf")     // trusted path, never one from the request
c.FileFS(assets, c.Param("name"))     // cannot escape the filesystem it is given
c.Attachment(path, "Q3 report.csv")   // Content-Disposition, encoded safely
```

`FileFS` exists because a name taken from a request must not be able to walk out of the
directory it is served from, and `Attachment` encodes the filename with
`mime.FormatMediaType`, since a download name is frequently user data and a quote in it
would otherwise end the header parameter early.

### Server-sent events

```go
app.Get("/events", func(c *ossein.Context) error {
    stream, err := c.EventStream()
    if err != nil {
        return err
    }
    defer stream.Close()

    for {
        if err := stream.Send(ossein.Event{Name: "stats", Data: payload}); err != nil {
            return err // the client is gone
        }
        select {
        case <-ticker.C:
        case <-c.Context().Done():
            return nil
        }
    }
})
```

Headers are written and flushed when the stream opens, so the connection is live before
the first event, and a writer that cannot flush is reported there rather than at the
first `Send` — a stream that cannot flush delivers nothing until the handler returns,
which for a stream is never.

Multi-line data becomes multiple `data:` lines, which is what the client rejoins.
Carriage returns are normalized first: a client ends a line on CR, LF, or CRLF, so a
bare CR left inside data would end the line there and let the rest be read as new
fields — an attacker-chosen event type and id. A line break in an ID, name, or comment
is rejected for the same reason, and an event with no data is rejected because no client
dispatches one. `Comment` writes a keepalive that clients ignore.

A stream must not outlive its handler: `net/http` releases the response once the handler
returns, so writing after that races the server recycling it. Handing the stream to a
producer goroutine and writing late panics that goroutine, where no recovery middleware
would see it — the write is recovered into an error and the stream closes itself, which
makes the mistake reportable rather than fatal, but does not make it safe. Keep the
stream inside its handler.

## Errors

Handlers return errors instead of rendering the same failure response repeatedly:

```go
func showUser(ctx *ossein.Context) error {
    user, err := findUser(ctx.Context(), ctx.Param("id"))
    if err != nil {
        return ossein.NotFound("user_not_found", "User not found")
    }

    return ctx.JSON(http.StatusOK, user)
}
```

Unexpected errors are logged through the request-scoped logger and rendered as a generic `500` response instead of leaking internal details. Applications can replace the renderer with `SetErrorHandler`.

Middleware is plain `func(http.Handler) http.Handler` and has no `*Context`, so
`WriteError` renders through the same handler. Auth, rate-limit, and CORS
rejections then match the rest of the API instead of hand-rolling a body that
drifts from it:

```go
func RequireAPIKey(keys map[string]string) ossein.Middleware {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if _, ok := keys[r.Header.Get("X-API-Key")]; !ok {
                ossein.WriteError(w, r, ossein.Unauthorized("invalid_api_key", "API key is not valid"))
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

`WriteError` covers middleware registered through `Use`. Middleware composed
around `app.Handler()` runs before the request context exists, and falls back to
the default document.

The document itself is exported as `ossein.ErrorEnvelope`, for decoding in tests
and clients or reusing the shape from a custom handler:

```json
{"error":{"code":"invalid_api_key","message":"API key is not valid"}}
```

A custom handler that only owns some errors delegates the rest to
`ossein.DefaultErrorHandler`:

```go
app.SetErrorHandler(func(c *ossein.Context, err error) {
    var domain *DomainError
    if errors.As(err, &domain) {
        _ = c.JSON(domain.Status, domain.Payload())
        return
    }
    ossein.DefaultErrorHandler(c, err)
})
```

Like routes and middleware, the error handler must be set before the application
starts serving requests.

## Request binding and validation

Ossein keeps validation explicit rather than relying on hidden runtime behavior.

```go
type CreateUserRequest struct {
    Name  string `json:"name"`
    Email string `json:"email"`
}

func (r *CreateUserRequest) Validate() error {
    validation := ossein.NewValidationError()

    if r.Name == "" {
        validation.Add("name", "is required")
    }

    if r.Email == "" {
        validation.Add("email", "is required")
    }

    return validation.OrNil()
}
```

Binding automatically calls `Validate` when the target implements `ossein.Validatable`.

`BindJSON` accepts `application/json` (including `+json` suffixes) and rejects
other media types with a `415` response. Bodies are limited to 1 MiB by
default; configure the limit with `ossein.WithMaxBindBytes` and oversized bodies
render `413`.

When a payload must be inspected as received — a webhook whose HMAC signature
covers the exact bytes, where a re-encoded struct would not match — take them
with `Body`. The body is read once under the same limit and cached, so `BindJSON`
still works afterwards with its strict decoding and automatic validation:

```go
func receive(c *ossein.Context) error {
    raw, err := c.Body()
    if err != nil {
        return err
    }
    if !validSignature(c.Request.Header.Get("X-Signature"), raw) {
        return ossein.Unauthorized("invalid_signature", "Signature does not match")
    }

    var delivery Delivery
    if err := c.BindJSON(&delivery); err != nil {
        return err
    }
    return c.JSON(http.StatusAccepted, delivery)
}
```

`Body` does not check `Content-Type`, since a raw body may be anything, and it
leaves the request body readable for helpers such as `ParseForm`.

### Forms and file uploads

`BindForm` handles `application/x-www-form-urlencoded` and `multipart/form-data`
with the same guarantees: the media type is enforced with a `415`, the body limit
is shared, and `Validate` runs automatically. Because the request path stays
free of reflection, binding is an explicit method rather than struct tags:

```go
type ReplayRequest struct {
    Event  string
    Limit  int
    DryRun bool
}

func (r *ReplayRequest) BindForm(form *ossein.Form) error {
    r.Event = form.Required("event")
    r.DryRun = form.Bool("dry_run")
    r.Limit = form.IntOr("limit", 10)
    return nil
}

func replay(c *ossein.Context) error {
    var request ReplayRequest
    if err := c.BindForm(&request); err != nil {
        return err
    }
    return c.JSON(http.StatusOK, request)
}
```

Accessors record field-level errors instead of returning them, so a bind method
reads as a list of assignments. `Required`, `Int`, `Int64`, `Float64`, and `Bool`
report a malformed value against its field; the `Or` variants take a default for a
field that is absent or blank, which is what an HTML form submits for an untouched
input; `Has` distinguishes an absent field from one that was never sent at all;
`AddError` adds an application rule. Those errors are reported before `Validate`
runs, so a malformed value is not also blamed for breaking a rule that never saw
it.

`ossein.NewValues` wraps a `url.Values`, so a bind method can be tested directly
without building a request.

Uploads come through `File`, `RequiredFile`, and `Files`, which return
`*multipart.FileHeader`. Parts are held in memory under the same body limit, so
nothing is written to a temporary file:

```go
func (r *BulkRequest) BindForm(form *ossein.Form) error {
    header := form.RequiredFile("deliveries")
    if header == nil {
        return nil
    }
    file, err := header.Open()
    if err != nil {
        return err
    }
    defer file.Close()
    // ...
    return nil
}
```

Unlike `BindJSON`, `BindForm` rejects an absent `Content-Type`: decoding JSON
validates the format as it goes, while a query-string parse almost never fails,
so an unlabelled body would bind as silently empty fields.

### Query strings

Query binding uses the same accessors, so there is one vocabulary to learn.
`Values` holds them and `Form` embeds it:

```go
type ListQuery struct {
    Page    int
    PerPage int
    Search  string
}

func (q *ListQuery) BindQuery(values *ossein.Values) error {
    q.Page = values.IntOr("page", 1)
    q.PerPage = values.IntOr("per_page", 20)
    q.Search = values.String("q")
    return nil
}

func (q *ListQuery) Validate() error {
    errs := ossein.NewValidationError()
    if q.PerPage < 1 || q.PerPage > 100 {
        errs.Add("per_page", "must be between 1 and 100")
    }
    return errs.OrNil()
}
```

For a handler that wants a parameter or two and not a request type, read them
directly and return the recorded errors:

```go
query, err := c.Query()
if err != nil {
    return err
}
page := query.Int("page")
if err := query.Err(); err != nil {
    return err
}
```

Only the query string is read, so a form body never satisfies a query field, and
a malformed query string is reported as a `400` rather than binding as silently
missing fields.

## Standard Go escape hatch

Ossein does not require every endpoint to use an Ossein handler. Ordinary `net/http` handlers remain first-class:

```go
app.HandleHTTPFunc(http.MethodGet, "/native", func(w http.ResponseWriter, r *http.Request) {
    logger := ossein.LoggerFromContext(r.Context())
    logger.Info("native handler")
    w.WriteHeader(http.StatusNoContent)
})
```

The same is available inside route groups.

## Who is Ossein for?

Ossein is especially aimed at developers coming from batteries-included ecosystems such as Laravel, Rails, Django, NestJS, or Spring who like Go but do not want to assemble every backend concern from scratch.

It is **not** intended to be Laravel reimplemented in Go. The inspiration is Laravel's developer experience, not its internals.

## Direction

The core MVP and initial driver-neutral data workflow are complete, including
`database/sql` connection management, dialect-aware migrations, PostgreSQL
MySQL, and SQLite concurrency protection, seeders, factories, and
application-owned data commands. The first production module now provides a
cache contract and process-local memory driver.

Next steps include database adapters, a distributed cache driver, queues and
workers, scheduling, events, mail, testing helpers, OpenAPI generation,
observability, and project generators.

See the [roadmap](docs/ROADMAP.md) for the current direction.

## Documentation

- [Getting started](docs/GETTING_STARTED.md)
- [Routing and named routes](docs/ROUTING.md)
- [CLI and generators](docs/CLI.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Cache](docs/CACHE.md)
- [Database](docs/DATABASE.md)
- [Migrations](docs/MIGRATIONS.md)
- [Seeding](docs/SEEDING.md)
- [Test factories](docs/FACTORIES.md)
- [Roadmap](docs/ROADMAP.md)
- [Field notes: building two applications on Ossein](docs/FIELD_NOTES.md)
- [Changelog](CHANGELOG.md)
- [v0.2.0 release notes](docs/releases/v0.2.0.md)
- [v0.1.0 release notes](docs/releases/v0.1.0.md)
- [Basic example](examples/basic/main.go)
- [Complete CRUD example](examples/crud/README.md)

## Complete example

The repository includes a dependency-free CRUD application that exercises the
MVP as a coherent stack:

```bash
go run ./examples/crud
```

It demonstrates interface-based dependency injection, route groups, named
routes, request validation, structured errors, typed configuration, graceful
shutdown, and full HTTP integration tests.

## Design principles

### Go remains Go

Ossein should compose with standard library APIs instead of creating a separate universe around them.

### Explicit over magical

Prefer code that is easy to trace, profile, test, and debug.

### Batteries included, parts replaceable

Ossein should eventually provide a coherent default stack while allowing applications to replace infrastructure behind clear interfaces.

### Great defaults, easy escape hatches

A developer should be productive quickly without losing access to `net/http`, `context`, database drivers, logging APIs, or other normal Go packages.

## Contributing

Contributions and architectural discussion are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) and the [Code of Conduct](CODE_OF_CONDUCT.md) before contributing.

Security reports should follow [SECURITY.md](SECURITY.md).

## License

Ossein is released under the [MIT License](LICENSE).
