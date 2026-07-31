# Changelog

All notable changes to Ossein will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Ossein uses semantic versioning for published releases.

## [Unreleased]

### Added

- `App.Serve(ctx, *http.Server)` runs a caller-owned server through the
  application lifecycle. Timeouts, `MaxHeaderBytes`, `BaseContext`, `ConnState`,
  and `ErrorLog` stay as the caller set them; only a nil `Handler` is filled in,
  and only after start hooks, so route conflicts still surface as errors rather
  than panics
- `App.ServeTLS` and `App.ServeListener` for HTTPS and for an already-bound
  `net.Listener` (socket activation, ephemeral test ports, or TLS composed with
  `tls.NewListener`). As in `net/http`, the protocol follows the method called:
  a `TLSConfig` alone never promotes a plain server to HTTPS. `ServeTLS` reports
  a missing certificate up front instead of failing with `open :`
- an already-cancelled context, or a server that was already closed, is now
  reported instead of returning success for a run that never accepted a
  connection; a graceful shutdown that exceeds its deadline is wrapped so it is
  distinguishable from a bare context error
- `App.SetNotFoundHandler` and `App.SetMethodNotAllowedHandler`. Unmatched
  routes and method mismatches are now rendered by the application instead of
  `ServeMux`, so a JSON API answers them with its own error envelope. The `Allow`
  header computed by `ServeMux` is preserved, and both fallbacks are ordinary
  handlers, so a custom `ErrorHandler` applies to them too
- `ResponseWriterFrom` follows `Unwrap() http.ResponseWriter` chains, the same
  convention `http.ResponseController` uses, so middleware layered between
  Ossein and a handler no longer hides the recorded status and size
- `ossein.WriteError(w, r, err)` renders an error through the application's
  `ErrorHandler` from ordinary `net/http` middleware, which has no `*Context`.
  The handler travels on the request context, so middleware needs no reference
  to the `App`, and a custom `ErrorHandler` now applies to auth, rate-limit, and
  CORS rejections instead of leaving one API with two error contracts
- the error document is exported as `ErrorEnvelope` and `ErrorResponse`, so
  applications can decode it in tests and clients and reuse the shape from a
  custom `ErrorHandler`
- `ossein.DefaultErrorHandler` is exported as the delegation target for a custom
  `ErrorHandler` that only wants to own some errors. Delegating through
  `WriteError` instead renders the default document rather than recursing
- `Context.Body` returns the raw request body under the configured
  `WithMaxBindBytes` limit, for payloads that must be inspected as received such
  as a webhook whose HMAC signature covers the exact bytes. The body is read once
  and cached, so `BindJSON` still works afterwards and keeps its strict decoding
  and automatic `Validate()`, and the body stays readable for `ParseForm`

- field notes from building two applications on Ossein, and the roadmap items
  they produced: `http.Server` configuration, `404`/`405` rendering, an error
  path reachable from middleware, raw-body and form binding, and atomic cache
  claims

### Changed

- `Run` and `RunContext` now delegate to `Serve` and build their server with a
  10s `ReadHeaderTimeout` and a 120s `IdleTimeout`, so an unattended server can
  no longer be held open indefinitely by connections that never complete a
  request. `ReadTimeout` and `WriteTimeout` remain unset so that large uploads
  and server-sent events keep working; use `Serve` to set them
- `SetErrorHandler` now panics once the application is serving requests, matching
  `Use`, route registration, and the new fallback setters. The request path reads
  the handler, so a late replacement was both a data race and a source of
  within-request disagreement between middleware and handlers

## [0.2.0] - 2026-07-30

### Added

- backend-neutral `cache.Store` contract with explicit miss, key, and TTL
  errors
- concurrency-safe in-memory cache driver with concurrent reads, bounded
  round-robin TTL cleanup on reads and writes, explicit full cleanup, and value
  copy isolation
- typed JSON and fail-open remember helpers with self-healing decode failures,
  strict encoding errors, and optional cache-error observation
- defined cache buffer ownership and a forward-compatible strategy for
  optional atomic backend capabilities
- SQLite multi-process migration safety through per-migration
  `BEGIN IMMEDIATE` transactions and bounded `PRAGMA busy_timeout` waiting
- real SQLite lock-timeout and concurrent-runner integration tests in CI
- documented sqlx, sqlc, and native pgx integration patterns with a
  CI-compiled database tooling example
- `ossein wire` (experimental): generated explicit service wiring
  (`GenerateWiring`, `WriteWiringFile`) that builds a `Services` struct with
  constructor calls in dependency order for zero-reflection startup;
  deterministic, gofmt-formatted output with strict diagnostics for closures,
  methods, generics, unexported symbols, and package-main registrations

## [0.1.0] - 2026-07-30

The first public release.

### Added

- `net/http` application, routing, groups, and middleware
- error-returning handlers with centralized, structured JSON errors
- strict JSON binding with Content-Type validation, unknown-field rejection,
  and a configurable body size limit (`WithMaxBindBytes`, 1 MiB default)
- explicit request validation with field-level `ValidationError` responses
- `ResponseWriter` wrapper recording committed status and response size, with
  `Unwrap` for `http.ResponseController`
- deferred route registration: conflicting patterns surface as `Start` errors
  before the server accepts traffic
- group middleware resolved at startup, applying regardless of declaration
  order inside a `Group` block
- request IDs and request-scoped `log/slog` loggers
- application lifecycle hooks and graceful shutdown
- typed environment configuration and dependency-free `.env` loading
- constructor-based service container with interface bindings, singleton and
  transient lifetimes, and dependency graph validation
- route registry, named routes, and URL generation
- driver-neutral `database/sql` configuration, pool management, startup ping,
  graceful shutdown, and DI registration
- transactional migration runner with up, rollback, and status operations
- filesystem and embedded migration sources with explicit statement splitting
- PostgreSQL, MySQL, and SQLite migration dialects
- PostgreSQL advisory locks and MySQL named locks for cross-process migration
  safety, with configurable lock timeouts (`migrate.WithLockTimeout`)
- transaction helper with rollback, panic safety, and `*sql.TxOptions`
- ordered transactional seeders and the application-owned `ossein db:seed`
  command
- generic, concurrency-safe test factories with states and persistence hooks
- composed `data.Commands` handler for migrations and seeders
- `ossein` CLI: starter application, supervised hot-reloading `ossein dev`,
  route listing, migration commands, and code generators
- complete in-memory CRUD reference application with HTTP integration tests
- real PostgreSQL and MySQL migration and concurrency integration tests in CI
- CI coverage gate requiring at least 85% total statement coverage
- tests, CI, package documentation, guides, and open-source policies

[Unreleased]: https://github.com/LoonY20/ossein/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/LoonY20/ossein/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/LoonY20/ossein/releases/tag/v0.1.0
