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
  and cached — including a failed read, so a retry cannot return a fragment of a
  partially drained stream — so `BindJSON` still works afterwards and keeps its
  strict decoding and automatic `Validate()`, and the body stays readable for
  `ParseForm`. `BindJSON` keeps streaming when the raw body was not taken, so an
  invalid body is still abandoned at its first syntax error rather than read up
  to the limit
- `Context.BindForm` binds `application/x-www-form-urlencoded` and
  `multipart/form-data` requests with the guarantees `BindJSON` already had:
  media-type enforcement (`415`), the shared `WithMaxBindBytes` limit, and an
  automatic `Validate()`. Targets implement `FormBindable` with an explicit
  `BindForm(*ossein.Form) error` method, so no reflection enters the request
  path, and `Form` provides typed accessors (`Required`, `Int`, `Bool`,
  `RequiredFile`, …) that record field-level errors into the usual
  `ValidationError`. Only the request body is read, so a field can never be
  satisfied from the query string; the body is parsed directly rather than through
  `Request.ParseForm`, which ignores bodies on methods other than POST, PUT, and
  PATCH and imposes its own 10 MB cap. Multipart parts are held in memory under
  the same body limit, so nothing is spilled to a temporary file, and the number
  of urlencoded fields is capped because the body limit does not bound the size of
  the parsed form
- `Context.BindQuery` and `Context.Query` for the query string, on the same typed
  accessors as forms: `Values` holds the accessor set and `Form` embeds it, so
  learning it once covers both. `BindQuery` takes a `QueryBindable` and applies the
  same error precedence and automatic `Validate()`; `Query` returns the parsed
  values for a handler that wants one or two parameters without a request type. A
  malformed query string is reported as a `400` instead of binding as silently
  missing fields, and only the query string is read, so a form body never satisfies
  a query field. The field count is capped as it is for bodies
- `StringOr`, `IntOr`, `Int64Or`, `Float64Or`, and `BoolOr` take a default for a
  field that is absent or blank. Pairing `Has` with a typed accessor gets this
  wrong: an HTML form submits an untouched input as present-but-empty, so the
  default would be skipped and the zero value then reported as invalid
- `ossein.NewValues` wraps a `url.Values`, so a bind method can be tested directly
  without building a request
- a `middleware` package with the standard middleware a service should not have to
  write: `Recover` renders a panic as a structured 500 through the application's
  error handler, logging the value and stack through the request-scoped logger,
  leaving a committed response alone, and falling back to a plain 500 if the error
  handler is itself what panicked; `AccessLog` writes one line per request,
  including a request that panicked, from the status and size Ossein already
  tracks, at a level that reflects the outcome, with `SkipPaths` for health probes;
  `SecurityHeaders` sets conservative defaults before the handler runs, so they
  cover error responses too, without replacing a header already present — including
  one deliberately set empty. Register `AccessLog` outside `Recover`, since a
  middleware only observes a status written below it
- `middleware.CORS` answers cross-origin preflight requests and adds the response
  headers a browser needs. Short-circuiting the preflight is necessary rather than
  convenient: an `OPTIONS` request matches no route, so the router would otherwise
  answer `405`. `Vary` is appended, never replaced, so a value set elsewhere
  survives. Setup panics for configurations that cannot be served safely: one that can
  never allow anything, and `AllowCredentials` combined with a wildcard origin, the
  `null` origin, or an `AllowOriginFunc` that approves every origin — the last being
  the one that actually grants any site authenticated read access, since browsers
  refuse a wildcard with credentials outright. Configured methods are upper-cased,
  because a browser compares the approved list byte for byte. `AllowOriginFunc` covers
  subdomains and allowlists held elsewhere
- `middleware.Timeout` bounds how long a request may take and answers `504` through
  the application's error handler. Unlike `http.TimeoutHandler`, it preserves the
  Ossein response writer, so `Written()` tracking, the committed-response guard, the
  access log's status, `http.ResponseController`, and streaming all keep working. The
  request context is cancelled at the deadline; a handler that ignores it keeps
  running but its writes and header changes are discarded, and rejection is keyed on
  the deadline rather than on scheduling, so it is deterministic. A cancellation for
  any other reason, such as a client disconnecting, is not reported as a timeout, and
  a connection the handler hijacked is left alone. Headers are the one thing not
  shared with the real response: the handler mutates a private map copied across when
  it commits, because two goroutines writing one header map risks a fatal concurrent
  map write. A panic on the handler's goroutine is forwarded to the request goroutine
  for `Recover`, and one arriving after the deadline is logged rather than lost

- `cache.Adder`, an optional capability for storing a value only when a key is free,
  implemented by the in-memory driver under its existing lock, and `cache.Claim` for the
  case that motivates it: an idempotency key, a run-once job, or a lease. `Get`-then-`Set`
  is what applications write today and it is racy by construction — two callers both miss,
  both store, and both proceed — with a window that is sub-microsecond against a local map
  and routine against a network cache, which is exactly where duplicate work is expensive.
  `Claim` reports `ErrNotAtomic` against a store that cannot provide the guarantee rather
  than falling back to the racy form, because a guarantee that quietly is not one produces
  a failure that looks like an application bug. A claim is a lease, so its TTL must be
  positive: one that never expires is a tombstone that only an explicit delete can lift
- `cache.WithMaxEntries` caps how much the in-memory driver holds, evicting the least
  recently used key when a write would exceed the cap. Without it the driver does not
  leak — every read and write reclaims a sample — but resident memory is TTL times
  arrival rate, which is not a budget anyone chose. Eviction goes by age of use rather
  than by expiry: a write cleans a sample first, so dead entries are usually gone before
  it runs, but a live key can still be discarded while dead ones remain. A bound changes
  reads from a shared to an exclusive lock, because eviction cannot tell hot keys from
  cold ones unless a read records that it happened, which is why it is opt-in — and it
  must not be used for a store holding claims, since evicting one lets the work it
  guards run twice
- `cache.RegisterMemory` binds an in-memory cache as a `Store` and reclaims its expired
  entries on a schedule, stopping the janitor during shutdown and waiting for it. Without
  a schedule, reclamation is driven by traffic, so a process that goes quiet after a burst
  holds every expired entry until its next write — a day of dead idempotency keys, and a
  ticker every application was writing by hand
- `cache.Once` runs work at most once per key and releases the claim if the work fails.
  Claiming before the work and keeping the claim when the work did not happen turns "this
  might run twice" into "this can never run again" — a webhook receiver that sheds a
  delivery with a `503`, asking the provider to resend, then answers the resend as a
  duplicate and loses it for the whole idempotency window. That trade is the worse one and
  is not visible at the call site, which is why it belongs in a helper rather than in a
  documented pattern
- a `queue` package for background work: `queue.Memory` is a bounded in-process
  job queue with a worker pool, per-job-name handlers, retries with backoff, and
  a drain on shutdown, and `queue.Register` ties it into the application
  lifecycle so workers start with the server and finish their in-flight jobs
  before `Stop` returns. Handlers take a `context.Context` and a `Job`, the same
  shape as an HTTP handler, and enqueuing goes through the `Enqueuer` interface,
  so a handler depends on the contract rather than on the implementation and a
  durable driver can replace it later without touching call sites. A full queue
  is reported as `ErrFull` and a stopped one as `ErrClosed`, so an application can
  shed load with a `503` instead of reporting back-pressure as a server fault; a
  panicking job becomes an error for the retry path rather than taking the worker
  down, and a job whose name has no handler is refused at enqueue time, where the
  caller can still see it. A job runs under a context that a graceful drain leaves
  alone — draining means waiting for the job, not interrupting it — and that is
  cancelled when the drain runs out of time, so a handler can tell "finish up" from
  "the process is going away". `Stop` reports what it could not finish, including
  accepted work that no worker ever ran, so a dropped job is never reported as a
  clean shutdown. `ErrAbandoned` marks a job whose retries were cut short by
  shutdown rather than exhausted, because filing those as dead letters means
  recording live work as dead on every deploy. `Stats` exposes queue depth and
  processed, failed, abandoned, and refused counts for a health endpoint.
  In-process means in-memory: pending jobs do not survive a crash, which the
  package documents rather than papers over
- response helpers on `Context` for everything other than JSON: `Text`, `HTML`, `Blob`,
  `Stream`, `Redirect`, `File`, `FileFS`, and `Attachment`, with `Text`, `HTML`, `Blob`,
  `Stream`, and `Redirect` also available as package-level functions for plain
  `net/http` handlers. All of them write through the Ossein response writer, so status
  and size tracking, the access log, and the committed-response guard keep working —
  which is what dropping to the raw writer costs. An unspecified content type becomes
  `application/octet-stream` rather than being left for `net/http` to sniff, since
  sniffing is how a text upload comes back as HTML, and each of them refuses to write
  over a response that was already sent rather than appending a second body to it.
  `Redirect` accepts only a status that redirects — 301, 302, 303, 307, 308, not the rest
  of the 3xx range, where Location means something else and 304 must not carry the body
  `http.Redirect` writes — and rejects a location containing a line break, which Go
  replaces with spaces rather than rejecting, leaving a redirect to somewhere nobody
  wrote. `File` delegates to `net/http`, so range and conditional requests work, but
  reports a missing file, a directory, or an unreadable path as an error, so a handler
  can fall back and the response stays in the application's error contract instead of
  ServeFile's plain-text 404. `FileFS` exists next to it because a name from a request
  must not be able to escape the directory it is served from; and `Attachment` encodes
  the filename with `mime.FormatMediaType` and sets the header only once the file is
  known to exist, since a download name is frequently user data and would otherwise be
  stamped on an error response
- `Context.EventStream` for server-sent events. Headers are written and flushed when the
  stream opens, so the connection is live before the first event and a writer that cannot
  flush is reported there rather than at the first send — a stream that cannot flush
  delivers nothing until the handler returns, which for a stream is never. Multi-line
  data becomes multiple data lines and one trailing terminator does not end the event
  early. Carriage returns in data are normalized to newlines, because a client ends a
  line on CR as well and a bare one left in place would end the data line and let the
  rest be read as forged fields; a line break in an id, name, or comment is rejected for
  the same reason. An event with no data is rejected, since no client dispatches one — a
  retry on its own is still allowed, being a directive rather than an event. A write from
  a stream that outlived its handler is recovered into an error instead of panicking a
  goroutine no recovery middleware is watching — reportable rather than fatal, though
  still a mistake, since net/http is concurrently recycling the response by then. `X-Accel-Buffering: no` is set, without which nginx holds every event until its
  proxy buffer fills. The Ossein response writer is preserved, so a stream is logged and
  the error handler will not write over it
- typed configuration understands more than scalars. `[]string`, `[]int`, and lists
  of any supported element type come from a comma-separated value, with entries
  trimmed and empty ones dropped, so a trailing comma is not a phantom element and a
  bad entry is reported by position rather than leaving the operator to bisect the
  value by hand. Any type implementing `encoding.TextUnmarshaler` parses itself,
  which covers `slog.Level`, `net/netip` addresses, `net.IP`, `time.Time`, and
  application types that validate their own input — a named type now parses itself
  instead of being assigned as its underlying string. `url.URL` and `*url.URL` are
  parsed with `url.Parse`, since `url.URL` implements `BinaryUnmarshaler` rather than
  `TextUnmarshaler` and the general mechanism does not reach it. `[]byte` stays the
  raw value: a key or a secret must not be split on commas, and since `byte` is an
  alias for `uint8` that necessarily covers `[]uint8` too. An element type that parses
  itself is decided before those rules, which go by kind, so `[]net.IP` — a list of
  `[]byte` that parse themselves — is a list of addresses rather than a rejected nested
  list. `required:"true"` on a list now rejects a value that parses to no entries, since
  `,,` is not an empty value and an allowlist that required entries would otherwise load
  with none. A URL is rejected when empty or when a dropped scheme leaves it with no
  host (`localhost:8080` parses as scheme `localhost`), both of which `url.Parse` accepts
  and neither of which can be joined onto. An unset list is nil and one set to no
  entries is empty, so "not configured" stays distinguishable from "configured empty".
  Maps remain unsupported, deliberately — their environment encoding needs two arbitrary
  separators
- `Job.RequestID` carries the identity of whatever enqueued a job, filled in by
  `Enqueue` from the context and put back by the worker, so both halves of an
  asynchronous request log under one ID and `ossein.RequestIDFromContext` works inside a
  job handler. A webhook accepted under one request and processed later on a worker could
  otherwise only be connected by guessing from timestamps. The ID travels with the job, so
  a durable driver keeps the connection across a restart, and it can be set explicitly for
  work whose origin the context does not know. Work with no origin carries no ID rather
  than an empty one
- `ossein.ContextWithRequestID` puts a request ID into a context, for the same reason
  `ContextWithLogger` exists: work that outlives a request still belongs to it
- `ossein.ContextWithLogger` puts a logger into a context that did not come from
  a request, so background work logs through the same handler as the rest of the
  application
- field notes from building two applications on Ossein, and the roadmap items
  they produced: `http.Server` configuration, `404`/`405` rendering, an error
  path reachable from middleware, raw-body and form binding, and atomic cache
  claims

### Changed

- a service constructor's variadic parameter is treated as options rather than as a
  dependency, so a constructor gaining one keeps working in the container. Without
  that, adding an option parameter to an existing constructor turns every
  registration of it into "service []Option is not registered" at startup
- `Run` and `RunContext` now delegate to `Serve` and build their server with a
  10s `ReadHeaderTimeout` and a 120s `IdleTimeout`, so an unattended server can
  no longer be held open indefinitely by connections that never complete a
  request. `ReadTimeout` and `WriteTimeout` remain unset so that large uploads
  and server-sent events keep working; use `Serve` to set them
- `SetErrorHandler` now panics once the application is serving requests, matching
  `Use`, route registration, and the new fallback setters. The request path reads
  the handler, so a late replacement was both a data race and a source of
  within-request disagreement between middleware and handlers
- a malformed `Content-Type` *parameter* no longer decides the media type for
  `BindJSON` or `BindForm`: `application/json; charset=` and its form equivalent
  are accepted, as `net/http` accepts them, rather than answering `415` with a
  message naming the very type that was sent. Only the media type itself decides

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
