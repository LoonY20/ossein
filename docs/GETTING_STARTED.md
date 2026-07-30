# Getting started

Ossein requires Go 1.23 or newer.

## Install the CLI

```bash
go install github.com/LoonY20/ossein/cmd/ossein@latest
```

Make sure the Go binary directory is on your `PATH`, then create an application:

```bash
ossein new hello-ossein
cd hello-ossein
go mod tidy
ossein dev
```

The generated application exposes `GET /health` on `http://localhost:8080`.
Copy `.env.example` to `.env` to change the application name or HTTP address
for local development. Exported environment variables override file values.

## Add a route

Register routes in `internal/http/routes.go`:

```go
app.Get("/users/{id}", showUser).Named("users.show")
```

Handlers return an error and retain access to standard Go HTTP primitives:

```go
func showUser(ctx *ossein.Context) error {
	return ctx.JSON(http.StatusOK, map[string]string{
		"id": ctx.Param("id"),
	})
}
```

## Inspect routes

```bash
ossein routes
```

The starter's server command exposes its registry to the CLI and prints method,
pattern, and name in a deterministic table.

## Generate common types

```bash
ossein make:controller User
ossein make:middleware Auth
ossein make:request CreateUser
```

Generators create ordinary Go source under `internal/http`. Generated code is
intended to be edited and does not require a runtime generator.

## Next reading

- [Routing](ROUTING.md)
- [CLI](CLI.md)
- [Application architecture](ARCHITECTURE.md)
- [Database](DATABASE.md)
- [Migrations](MIGRATIONS.md)
- [Seeding](SEEDING.md)
- [Test factories](FACTORIES.md)
- [Complete CRUD example](../examples/crud/README.md)
- [Main README](../README.md)
