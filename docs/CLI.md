# Ossein CLI

Install:

```bash
go install github.com/LoonY20/ossein/cmd/ossein@latest
```

## Commands

| Command | Purpose |
| --- | --- |
| `ossein new <name>` | Create a minimal application |
| `ossein dev` | Run `./cmd/server` with the Go toolchain |
| `ossein routes` | Print the application's registered routes |
| `ossein migrate [--limit N]` | Apply pending migrations |
| `ossein migrate:rollback [--steps N]` | Roll back the latest applied migrations |
| `ossein migrate:status` | Print applied and pending migrations |
| `ossein db:seed` | Run application-defined database seeders |
| `ossein wire` | Generate explicit service wiring |
| `ossein make:controller <name>` | Create an HTTP controller |
| `ossein make:middleware <name>` | Create standard Go middleware |
| `ossein make:request <name>` | Create an explicitly validated request |
| `ossein version` | Print the CLI version |

## Starter layout

```text
my-app/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go
│   └── http/
│       ├── health.go
│       ├── health_test.go
│       └── routes.go
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

The starter is deliberately small. It demonstrates project conventions without
forcing a layered architecture onto applications that do not need one.

Configuration loads an optional `.env` file for local development, while
already exported environment variables retain precedence.

Route and migration commands are application commands: the CLI forwards them
to `go run ./cmd/server`. This keeps database drivers, connection configuration,
and migration sources under the application's control. See the
[migration guide](MIGRATIONS.md#cli-wiring) for the required wiring.

`ossein db:seed` uses the same application-owned command boundary. See the
[seeding guide](SEEDING.md#cli-wiring).

`ossein dev` builds the application into a temporary directory, runs the server
as its direct child process, and rebuilds it when Go source, module,
configuration, migration, or `.env` files change. Build failures keep the
watcher alive until the next edit. Cancelling the CLI stops the server before
removing the temporary binary.

## Generated service wiring

> **Experimental.** The `ossein wire` command, the `GenerateWiring` and
> `WriteWiringFile` APIs, and the shape of the generated code may change or be
> removed before the first stable release, depending on real-world feedback.
> The service container remains the default and fully supported path.

`ossein wire` is an application command like `migrate`: it runs the
application's normal service registrations, then writes
`internal/wiring/wiring_gen.go` with explicit constructor calls in dependency
order. The generated `Wire` function builds a `Services` struct without any
container reflection:

```go
services, err := wiring.Wire(logger, db)
if err != nil {
	log.Fatal(err)
}
services.UserService.Handle(...)
```

Rules the generator enforces instead of guessing:

- singleton services become `Services` fields; transient services become
  `New<Type>` factory methods; `Instance` registrations become `Wire`
  parameters, because generated code cannot reproduce runtime values;
- constructors must be exported, named, top-level, non-generic functions
  declared outside package `main`; closures and methods are rejected with the
  offending symbol in the error;
- registration types must be exported and non-generic;
- singletons must not depend on transient services;
- the dependency graph is validated first, so missing registrations and
  cycles fail generation exactly as they would fail `Start`.

The output is deterministic (stable ordering, explicit import aliases,
gofmt-formatted), so regenerated files produce reviewable diffs. Generation
never calls constructors — it only inspects registration metadata — and the
container remains the default; generated wiring is an opt-in for applications
that want zero-reflection startup. A wiring file is a snapshot of the graph
that the registration code built during that run: applications with
environment-dependent registrations should regenerate per configuration or
stay on the container.
