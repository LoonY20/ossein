# Database

Ossein's database foundation is driver-neutral and built on Go's standard
`database/sql` package.

Ossein configures the pool, verifies connectivity during application startup,
registers `*sql.DB` in the service container, and closes it during shutdown. It
does not bundle a database driver or require an ORM.

## Configuration

Embed `database.Config` in the application configuration:

```go
type Config struct {
	Database database.Config
}

config, err := ossein.LoadConfig[Config]()
```

Environment:

```env
DB_DRIVER=pgx
DB_DSN=postgres://postgres:secret@localhost:5432/app
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=5
DB_CONN_MAX_LIFETIME=30m
DB_CONN_MAX_IDLE_TIME=5m
DB_PING_TIMEOUT=5s
```

Register the connection:

```go
db, err := database.Register(app, config.Database)
```

The same pool is available through dependency injection:

```go
func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

app.Provide(NewUserRepository)
```

## Choosing a driver

`DB_DRIVER` selects a driver already compiled into the application. Import one
or more drivers with a blank import.

### PostgreSQL

Using [pgx](https://github.com/jackc/pgx):

```go
import _ "github.com/jackc/pgx/v5/stdlib"
```

```env
DB_DRIVER=pgx
DB_DSN=postgres://postgres:secret@localhost:5432/app
```

### MySQL

Using [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql):

```go
import _ "github.com/go-sql-driver/mysql"
```

```env
DB_DRIVER=mysql
DB_DSN=user:password@tcp(localhost:3306)/app?parseTime=true
```

### SQLite

Using [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite):

```go
import _ "modernc.org/sqlite"
```

```env
DB_DRIVER=sqlite
DB_DSN=file:app.db
```

Applications that compile multiple drivers can switch between them entirely
through configuration. If a driver is not compiled into the binary,
`database/sql` returns an unknown-driver error during setup.

## Why database/sql?

- it is part of the Go standard library;
- PostgreSQL, MySQL, SQLite, SQL Server, and other databases provide drivers;
- repositories can depend on `*sql.DB` without an Ossein-specific query API;
- connection pooling, contexts, transactions, and prepared statements remain
  ordinary Go;
- Ossein core keeps zero third-party runtime dependencies.

Database-specific capabilities remain available through the selected driver.
For applications committed exclusively to PostgreSQL, native pgx APIs may be a
better choice than `database/sql`; Ossein preserves that escape hatch.

## Transactions

`database.WithinTransaction` removes repetitive begin, rollback, panic, and
commit handling while exposing the ordinary `*sql.Tx`:

```go
err := database.WithinTransaction(ctx, db, nil, func(
	ctx context.Context,
	tx *sql.Tx,
) error {
	_, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = balance - $1 WHERE id = $2", amount, fromID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE accounts SET balance = balance + $1 WHERE id = $2", amount, toID)
	return err
})
```

The callback error is preserved for `errors.Is`. Panics are rolled back before
being re-thrown. Use `*sql.TxOptions` when a specific isolation level or
read-only transaction is required.

## Migrations

Connection management is driver-neutral, but migration SQL is not. The
migration layer uses dialect adapters for PostgreSQL, MySQL, and SQLite instead
of pretending their DDL and locking behavior are identical. PostgreSQL and
MySQL migrations use session-level locks, while SQLite uses immediate write
transactions to serialize runners across processes. Lock waiting is bounded
and configurable with `migrate.WithLockTimeout`.

See [Migrations](MIGRATIONS.md) for the runner and file format.

See [Seeding](SEEDING.md) and [Test factories](FACTORIES.md) for application
data and deterministic test fixtures.
