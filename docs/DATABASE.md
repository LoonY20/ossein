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

## Query tooling: sqlx, sqlc, and native pgx

Ossein does not ship query-layer adapters, because the standard library
boundary already composes with the common Go approaches. The
[database tooling example](../examples/database-tooling) compiles these
patterns in CI.

### sqlx

[sqlx](https://github.com/jmoiron/sqlx) wraps the pool that `database.Register`
already manages — one pool, one lifecycle:

```go
db, err := database.Register(app, config.Database)
if err != nil {
	return err
}
if err := ossein.Instance(app, sqlx.NewDb(db, config.Database.Driver)); err != nil {
	return err
}

func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db: db}
}
```

### sqlc

[sqlc](https://sqlc.dev) generates plain Go on top of `database/sql`, so its
output works with Ossein without glue. Point `sqlc.yaml` at the migration
directory so the schema has a single source of truth:

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "migrations"
    queries: "internal/queries"
    gen:
      go:
        package: "queries"
        out: "internal/queries"
```

Register the generated `Queries` type through the container; it accepts the
managed pool because `*sql.DB` satisfies sqlc's `DBTX`:

```go
if err := ossein.Instance(app, queries.New(db)); err != nil {
	return err
}
```

Inside `database.WithinTransaction`, `queries.New(tx)` scopes the same
generated code to the transaction.

### Native pgx

Applications committed exclusively to PostgreSQL can skip `database/sql` and
manage a native pool with the same lifecycle hooks Ossein uses internally:

```go
pool, err := pgxpool.New(ctx, dsn)
if err != nil {
	return err
}
if err := ossein.Instance(app, pool); err != nil {
	return err
}
app.OnStart(func(ctx context.Context) error { return pool.Ping(ctx) })
app.OnStop(func(context.Context) error { pool.Close(); return nil })
```

Migrations still run through the `database/sql` pgx stdlib driver; both pools
can coexist during a migration step.

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

## Classifying errors

Branching on a database failure — "that code is taken, generate another", "that
transaction lost a deadlock, run it again" — otherwise means matching a driver's
message, which breaks the day the DSN points at a different engine:

```go
if database.IsUniqueViolation(err) {
    return errCodeTaken
}
if database.IsRetryable(err) {
    return retry(ctx, work)
}
```

`Classify` names the failure; `IsUniqueViolation`, `IsExclusionViolation`,
`IsForeignKeyViolation`, `IsNotNullViolation`, `IsCheckViolation`, `IsLockTimeout`,
and `IsRetryable` are the predicates. Wrapped errors are unwrapped — including
`errors.Join` and multi-`%w` wraps, which `errors.Unwrap` alone does not traverse —
so an error carried up through a repository is still recognised.

`IsRetryable` covers deadlocks and serialization failures: the two classes the
engine has already rolled back, where re-running the transaction is the documented
remedy. A **lock timeout is deliberately not one of them**. MySQL rolls back only
the failed statement unless `innodb_rollback_on_timeout` is on, and SQLite's busy
errors leave the transaction open too, so re-running without rolling back first
means running against a half-applied transaction that still holds its locks.
`IsLockTimeout` is separate for that reason.

Code that is not a SQL driver can take part through the sentinels, so a fake store
in a test or an adapter in front of another system reaches the same call site:

```go
return fmt.Errorf("create link: %w", database.ErrUniqueViolation)
```

### How a driver is recognised, and where that is fragile

This package cannot import a driver: the core has no third-party dependencies, and
an application should not inherit one it does not use. So errors are recognised
through whatever a driver exposes, in this order:

1. The sentinels above.
2. `SQLState() string`, which PostgreSQL drivers implement. A standard, and the most
   reliable.
3. `Code() int`, which the pure-Go SQLite driver implements, carrying the result code
   that distinguishes constraint kinds — *and* a SQLite-shaped message. A bare
   `Code() int` is no evidence of SQLite: an Oracle driver has one carrying ORA
   numbers that collide outright (ORA-01555, "snapshot too old", is
   `SQLITE_CONSTRAINT_PRIMARYKEY`), and so does any small application enum, where a
   code of 5 would become a lock timeout. The code decides the class; the message
   only confirms the family.
4. The error message, for `go-sql-driver/mysql` and `mattn/go-sqlite3`, which keep
   their codes in struct fields with no accessor.

Each mechanism is tried against the **whole error chain** before the next one is
tried at all. That is what makes "a structured code wins over text" true rather than
merely intended: a wrapper's message contains the text of everything it wraps, so
checking every mechanism at the outermost level first would let a quoted string
decide the class of the error underneath it — turning a serialization failure that
should be retried into a unique violation that should not.

The last mechanism is the fragile one: a driver is free to reword its errors, a
localized message will not match, and a wrapper quoting the wrong text can mislead
it. MySQL numbers are matched with their delimiter, so `Error 10620` is not
`Error 1062`. When it is wrong the answer is `ClassUnknown`, which is what an
application would have had anyway.

MySQL's SQLSTATE is deliberately not read. It reports every integrity constraint as
`23000`, so trusting it would name a broken foreign key a duplicate key.

### Teaching it a driver

```go
classifier := database.NewClassifier(func(err error) (database.ErrorClass, bool) {
    var driverErr *somedriver.Error
    if errors.As(err, &driverErr) && driverErr.Code == 1234 {
        return database.ClassUniqueViolation, true
    }
    return database.ClassUnknown, false
})
```

Recognizers run before the built-in ones, so this both extends and corrects. An
application that imports its driver anyway can assert on the concrete type, which is
always more reliable than anything this package can do from the outside.

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
