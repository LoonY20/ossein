# Ossein Roadmap

Ossein is being built from the inside out: first the small, explicit core, then the developer-experience layer around it.

This roadmap is directional. APIs may change significantly before the first stable release.

## Phase 0 — Foundation

Goal: establish the rules the project will not compromise on.

- [x] public open-source repository
- [x] MIT license
- [x] project philosophy and contribution guidelines
- [x] minimal `net/http` application core
- [x] HTTP method helpers
- [x] application middleware pipeline
- [x] JSON response helper
- [x] initial tests
- [x] continuous integration
- [x] package-level documentation
- [x] stable MVP error model

## Phase 1 — Core HTTP DX

Goal: make common API work pleasant without hiding standard Go.

- [x] request binding
- [x] explicit validation foundation
- [x] structured application errors
- [x] centralized error rendering
- [x] route groups
- [x] middleware groups
- [x] named routes
- [x] request IDs
- [x] structured request logging through `log/slog`
- [x] graceful application lifecycle hooks
- [x] typed environment configuration primitives
- [x] configurable graceful-shutdown timeout

The current validation design intentionally uses an explicit `Validate() error` contract. More ergonomic validation helpers may be added later without making reflection a requirement for the core.

Configuration uses a small amount of reflection only while loading tagged configuration structs. The HTTP request runtime remains reflection-free.

## Phase 2 — Application Structure and CLI

Goal: provide the Laravel-like productivity layer while keeping generated code ordinary Go.

- [x] constructor-based service container
- [x] interface bindings
- [x] singleton and transient lifetimes
- [x] missing-dependency validation
- [x] circular-dependency detection
- [ ] optional code-generation strategy for dependency wiring
- [x] `ossein new`
- [x] `ossein dev`
- [x] `ossein routes`
- [x] `ossein make:controller`
- [x] `ossein make:middleware`
- [x] `ossein make:request`
- [x] application starter template
- [x] documented project conventions
- [x] complete CRUD reference application

The service container uses reflection only to inspect constructor signatures, validate dependency graphs, and construct registered services. Dependencies remain explicit in normal Go constructor arguments; Ossein does not inject struct fields or hide dependencies behind request-time service lookups.

## Phase 3 — Data Layer

Goal: offer a coherent database workflow without inventing a mandatory ORM.

- [x] database abstraction boundaries
- [x] driver-neutral `database/sql` configuration and lifecycle
- [x] transactional migration runner and filesystem source
- [x] PostgreSQL, MySQL, and SQLite migration dialects
- [x] migration CLI commands
- [x] PostgreSQL advisory migration lock
- [x] MySQL distributed migration lock
- [x] SQLite multi-process migration strategy
- [x] PostgreSQL integration
- [x] MySQL integration
- [x] SQLite integration
- [x] migration workflow foundation
- [x] seeders
- [x] factories for tests
- [x] transaction helpers
- [ ] adapters for common Go database approaches

The initial direction is to integrate proven Go database tooling instead of building a custom Eloquent-style ORM.

## Phase 4 — Production Modules

Goal: make Ossein useful for real backend services.

- [ ] cache contracts and drivers
- [ ] queues and workers
- [ ] retries and backoff
- [ ] failed jobs / dead-letter handling
- [ ] scheduler
- [ ] events
- [ ] mail
- [ ] storage adapters
- [ ] health and readiness endpoints

## Phase 5 — Observability and API Tooling

- [ ] OpenAPI generation
- [ ] metrics
- [ ] OpenTelemetry integration
- [ ] profiling helpers
- [ ] testing utilities
- [ ] HTTP test client
- [ ] benchmark suite

## Non-goals

Ossein does not aim to:

- reproduce Laravel internals in Go;
- hide `context.Context`, `error`, `http.Handler`, or other fundamental Go concepts;
- require a custom ORM, logger, database driver, or queue backend;
- rely heavily on runtime reflection for core behavior;
- replace the Go standard library where composition is sufficient.

## Guiding question

Every feature should answer this question:

> Does this remove repetitive work while leaving the resulting Go code understandable and debuggable?

If the answer is no, the design should be reconsidered.
