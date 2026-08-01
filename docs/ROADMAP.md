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

The following gaps were found by building two applications on 0.2.0; see
[field notes](FIELD_NOTES.md) for the evidence behind each one. They are core
gaps rather than new modules: in each case the framework's own contract —
structured JSON errors, an owned server lifecycle — does not currently hold.

- [x] configurable `http.Server` through `App.Serve`, `App.ServeTLS`, and
      `App.ServeListener`: timeouts, TLS, header limits, `BaseContext`, and
      `ErrorLog` stay owned by the caller while Ossein runs the lifecycle,
      including socket activation and TLS composed with `tls.NewListener`
- [x] safe default timeouts in `Run`/`RunContext`: `ReadHeaderTimeout` and
      `IdleTimeout` bound half-open connections, while `ReadTimeout` and
      `WriteTimeout` stay unset so uploads and streaming keep working
- [x] customizable `404` and `405` responses rendered through the error handler,
      preserving `Allow` (`SetNotFoundHandler`,
      `SetMethodNotAllowedHandler`)
- [x] an error path reachable from middleware: `WriteError` plus the exported
      `ErrorEnvelope`, chosen over a `*Context`-aware middleware form because it
      keeps one middleware type and works with third-party middleware
- [x] raw request-body access that composes with `BindJSON`, for signed payloads
      such as webhook HMACs (`Context.Body`, read once and cached under the
      configured limit)
- [x] form and multipart binding with `BindJSON`'s guarantees: media-type
      enforcement, shared body limit, automatic `Validate()` (`BindForm` and the
      explicit `FormBindable` contract, so the request path stays
      reflection-free)
- [x] query-string helpers and `BindQuery`, sharing one typed accessor set with
      form binding through `Values`
- [x] response helpers beyond `JSON` and `NoContent`: text, HTML, bytes, streams,
      redirect, file, download, and server-sent events
- [x] typed config support for lists and `encoding.TextUnmarshaler`, plus `url.URL`
      (which implements `BinaryUnmarshaler`, so the general mechanism misses it)
- [ ] typed config support for maps — deliberately deferred: an environment encoding
      for one needs two arbitrary separators

The current validation design intentionally uses an explicit `Validate() error` contract. More ergonomic validation helpers may be added later without making reflection a requirement for the core.

Configuration uses a small amount of reflection only while loading tagged configuration structs. The HTTP request runtime remains reflection-free.

## Phase 2 — Application Structure and CLI

Goal: provide the Laravel-like productivity layer while keeping generated code ordinary Go.

- [x] constructor-based service container
- [x] interface bindings
- [x] singleton and transient lifetimes
- [x] missing-dependency validation
- [x] circular-dependency detection
- [x] optional code-generation strategy for dependency wiring
      (`ossein wire`, experimental until validated by real applications)
- [ ] `ossein wire` staleness detection: a `//go:generate` directive and a
      checked-in regeneration test so CI fails when the generated graph drifts
- [ ] documented package layout `ossein wire` requires — generated code cannot
      reference `package main`, so single-package services must restructure
      first
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

- [x] panic recovery middleware with structured 500 responses
      (`middleware.Recover`)
- [x] access-log middleware built on the response status and size tracking
      (`middleware.AccessLog`)
- [x] security headers middleware (`middleware.SecurityHeaders`)
- [x] CORS middleware, including `OPTIONS` preflight short-circuiting
      (`middleware.CORS`) — a preflight matches no route, so without it the router
      answers `405`
- [x] request timeout middleware that preserves `ResponseWriter` tracking and
      renders through the error handler (`middleware.Timeout`) —
      `http.TimeoutHandler` replaces the writer, silently disabling `Written()`
      and the already-committed guard
- [ ] request body limit middleware (weak: `WithMaxBindBytes` already bounds every
      binding path and `Context.Body`)
- [ ] private network access preflight headers, which Chrome requires for a public
      page calling a private-network service
- [x] request identity across deferred work: `Job.RequestID` carries the enqueuing
      request into the worker, `ossein.ContextWithRequestID` carries one into work
      that has no request, and `context.WithoutCancel` already covered a goroutine
      started from a handler
- [ ] driver-neutral SQL error classification: unique violation, deadlock,
      serialization failure (today applications string-match driver messages)
- [x] cache contract
- [x] in-memory cache driver
- [x] memory cache driver amortized expiration sweeping
- [x] memory cache driver configurable size bounds and eviction policy
      (`cache.WithMaxEntries`, least-recently-used)
- [x] documented cache semantics for undecodable entries and backend
      write failures
- [x] optional atomic cache capability interfaces — `cache.Adder`, `cache.Claim`,
      and `cache.Once`, for idempotency keys, run-once jobs, and leases
- [x] lifecycle-managed cleanup for the memory driver (`cache.RegisterMemory`),
      since sampled reclamation is driven by traffic and a process idle after a
      burst would otherwise hold expired entries until its next write
- [ ] distributed cache driver
- [x] queues and workers with lifecycle-managed graceful shutdown
      (`queue.Memory`, `queue.Register`), behind an `Enqueuer` interface so a
      durable driver can replace the in-memory one without touching call sites
- [ ] database-backed queue driver on `database/sql` (`SKIP LOCKED`) — the
      in-memory driver loses pending jobs on a crash, which is fine only for work
      whose source retries
- [ ] `ossein queue:work` application command
- [x] retries and backoff (`WithMaxAttempts`, `WithBackoff`; a panicking job
      becomes an error on the retry path instead of killing its worker)
- [ ] failed jobs / dead-letter handling — `WithFailureHandler` is the hook;
      persistence behind it is still the application's
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

- [ ] testing utilities and HTTP test client with response assertions — the
      first thing hand-rolled in both applications built on 0.2.0
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
