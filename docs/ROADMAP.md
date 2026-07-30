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
- [x] documented sqlx, sqlc, and native pgx integration patterns with a
      CI-compiled example

The initial direction is to integrate proven Go database tooling instead of building a custom Eloquent-style ORM.

## Phase 4 — Production Modules

Goal: make Ossein useful for real backend services.

- [ ] panic recovery middleware with structured 500 responses
- [ ] access-log middleware built on the response status and size tracking
- [x] cache contract
- [x] in-memory cache driver
- [x] memory cache driver amortized expiration sweeping
- [ ] memory cache driver configurable size bounds and eviction policy
- [x] documented cache semantics for undecodable entries and backend
      write failures
- [ ] optional atomic cache capability interfaces
- [ ] distributed cache driver
- [ ] queues and workers with lifecycle-managed graceful shutdown
- [ ] database-backed queue driver on `database/sql` (`SKIP LOCKED`)
- [ ] `ossein queue:work` application command
- [ ] retries and backoff
- [ ] failed jobs / dead-letter handling
- [ ] scheduler
- [ ] events
- [ ] mail
- [ ] storage adapters
- [ ] health and readiness endpoints

Drivers that require third-party clients (Redis and similar) will live in
separate Go modules inside this repository, following the
`integration/` precedent. The core module keeps zero third-party runtime
dependencies; applications pay for a dependency only by importing its driver
module.

## Phase 5 — Multi-Service Workspaces

Goal: make starting and running several Ossein services as easy as one,
through code generation and standard-library patterns — not a platform.

- [ ] `ossein new --workspace`: a `go.work` monorepo starter with
      `services/`, a shared contracts module, Docker Compose, and shared
      environment files
- [ ] multi-service `ossein dev`: one supervisor building, running, and
      hot-reloading every workspace service with prefixed output
- [ ] typed inter-service HTTP client helpers with timeouts and retries on
      the standard `http.Client`
- [ ] outgoing request-ID and trace-context propagation for inter-service
      calls
- [ ] transactional outbox helper on top of queues and the existing
      transaction workflow
- [ ] multi-service guide: contracts modules, typed clients, outbox, and
      deployment patterns

The workspace layer is deliberately thin: generated code, `go.work`, and
`net/http`. Anything that would turn Ossein into a distributed-systems
platform is a non-goal (see below).

## Phase 6 — Observability and API Tooling

- [ ] testing utilities and HTTP test client with response assertions
- [ ] OpenAPI generation
- [ ] metrics
- [ ] OpenTelemetry integration
- [ ] profiling helpers
- [ ] benchmark suite

## Non-goals

Ossein does not aim to:

- reproduce Laravel internals in Go;
- hide `context.Context`, `error`, `http.Handler`, or other fundamental Go concepts;
- require a custom ORM, logger, database driver, or queue backend;
- rely heavily on runtime reflection for core behavior;
- replace the Go standard library where composition is sufficient;
- become a distributed-systems platform: service discovery, an RPC
  framework, a configuration server, a service mesh, and deployment
  orchestration are out of scope — multi-service support stays at the level
  of code generation, shared contracts, and standard-library HTTP.

## Guiding question

Every feature should answer this question:

> Does this remove repetitive work while leaving the resulting Go code understandable and debuggable?

If the answer is no, the design should be reconsidered.
