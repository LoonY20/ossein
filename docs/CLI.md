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
