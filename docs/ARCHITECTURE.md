# Architecture

Ossein's design rule is: **convenience without hiding Go**.

## Core boundaries

```text
Application
├── net/http router and middleware
├── Context, binding, validation, and errors
├── slog request context
├── typed environment configuration
├── lifecycle
└── constructor-based service container
```

The framework does not replace `context.Context`, `error`, `http.Handler`,
`http.Request`, `http.ResponseWriter`, or `slog.Logger`.

## Reflection policy

Reflection is limited to two setup-time boundaries:

- decoding environment variables into typed configuration;
- inspecting and invoking registered service constructors.

HTTP routing, middleware, handlers, errors, and response rendering do not depend
on reflection.

## Dependency wiring

Dependencies stay visible in constructor signatures:

```go
func NewUserService(users UserRepository) *UserService
```

The container supports concrete and interface registrations, singleton and
transient lifetimes, missing dependency validation, and circular dependency
detection. It does not inject struct fields or expose a request-time service
locator.

## Compatibility

Ossein is pre-1.0. Public APIs may change while the project gathers practical
usage. Changes should preserve standard Go interoperability and be documented
in pull requests and release notes.
