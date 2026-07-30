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
- response status and size tracking through a wrapped `http.ResponseWriter`;
- validation through an explicit `Validate() error` contract;
- field-level `ValidationError` responses;
- typed environment configuration with defaults and required values;
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
- ordered transactional seeders and `ossein db:seed`;
- generic test factories with sequences, states, and persistence hooks;
- composed application command handling for migrations and seeders;
- backend-neutral cache contract, concurrency-safe in-memory driver, and typed
  JSON/remember helpers;
- JSON response helpers;
- blocking server start with `Run`, with conservative default timeouts;
- context-aware graceful shutdown with `RunContext`;
- `Serve` for a caller-owned `*http.Server`: timeouts, TLS, limits, and
  `BaseContext` stay yours while Ossein runs the lifecycle;
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

The first configuration layer supports strings, booleans, integers, unsigned integers, floats, and `time.Duration`. Nested structs are supported. Reflection is limited to configuration loading and is not part of Ossein's request runtime.

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

When an application needs to own those settings — or TLS, `MaxHeaderBytes`,
`BaseContext`, or `ErrorLog` — pass a configured server to `Serve` instead. Only
`Handler` is filled in, and only when nil:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

err := app.Serve(ctx, &http.Server{
    Addr:              ":8443",
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       30 * time.Second,
    IdleTimeout:       60 * time.Second,
    MaxHeaderBytes:    1 << 16,
    TLSConfig:         tlsConfig, // a non-nil TLSConfig serves HTTPS
})
```

`Serve` runs the same lifecycle as `Run`: service validation, start hooks,
graceful shutdown when the context is cancelled, then stop hooks.

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
