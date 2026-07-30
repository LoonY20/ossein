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

### Changed

- `Run` and `RunContext` now delegate to `Serve` and build their server with a
  10s `ReadHeaderTimeout` and a 120s `IdleTimeout`, so an unattended server can
  no longer be held open indefinitely by connections that never complete a
  request. `ReadTimeout` and `WriteTimeout` remain unset so that large uploads
  and server-sent events keep working; use `Serve` to set them.

- field notes from building two applications on Ossein, and the roadmap items
  they produced: `http.Server` configuration, `404`/`405` rendering, an error
  path reachable from middleware, raw-body and form binding, and atomic cache
  claims

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
