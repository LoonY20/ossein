# Database tooling example

This example shows how Ossein composes with common Go database tooling
through the standard library instead of framework adapters:

- the [pgx](https://github.com/jackc/pgx) stdlib driver powers the ordinary
  `database/sql` pool that `database.Register` manages;
- [sqlx](https://github.com/jmoiron/sqlx) wraps that same pool for struct
  scanning, registered in the service container with `ossein.Instance`;
- repositories receive `*sqlx.DB` through ordinary constructor injection.

The example lives in its own Go module so the framework core keeps zero
third-party runtime dependencies. CI compiles it to keep the guide honest.

## Run

```bash
DB_DRIVER=pgx \
DB_DSN='postgres://postgres:secret@localhost:5432/app?sslmode=disable' \
go run .
```

Then:

```bash
curl localhost:8080/users/1
```

See the [database guide](../../docs/DATABASE.md) for the full pgx, sqlx, and
sqlc integration patterns.
