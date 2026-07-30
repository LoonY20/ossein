# Migrations

Ossein migrations run on `database/sql` and use small dialect adapters for
PostgreSQL, MySQL, and SQLite.

## Files

Each migration is a pair:

```text
migrations/
├── 000001_create_users.up.sql
├── 000001_create_users.down.sql
├── 000002_add_email.up.sql
└── 000002_add_email.down.sql
```

Both files are required so every applied migration has an explicit rollback.

```sql
-- 000001_create_users.up.sql
CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    name VARCHAR(255) NOT NULL
);
```

```sql
-- 000001_create_users.down.sql
DROP TABLE users;
```

## Multiple statements

Ossein does not split SQL on semicolons because semicolons can occur inside
functions, strings, and database-specific syntax. Separate statements with an
explicit marker:

```sql
CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL
);

-- ossein:split

CREATE UNIQUE INDEX users_email_unique ON users (email);
```

Every statement runs in the same database transaction.

## Loading

Load migrations from a filesystem:

```go
migrations, err := migrate.LoadFS(os.DirFS("."), "migrations")
```

They can also be embedded into the application binary:

```go
//go:embed migrations/*.sql
var migrationFiles embed.FS

migrations, err := migrate.LoadFS(migrationFiles, "migrations")
```

## Runner

Select a dialect from the configured database driver:

```go
dialect, err := migrate.DialectForDriver(config.Database.Driver)
if err != nil {
	return err
}

runner, err := migrate.New(db, dialect)
if err != nil {
	return err
}
```

Apply all pending migrations:

```go
count, err := runner.Up(ctx, migrations, 0)
```

Apply only the next migration:

```go
count, err := runner.Up(ctx, migrations, 1)
```

Roll back the most recently applied migration:

```go
count, err := runner.Down(ctx, migrations, 1)
```

Inspect status:

```go
statuses, err := runner.Statuses(ctx, migrations)
```

## Metadata

Applied versions are stored in `ossein_migrations`. Change the table with:

```go
runner, err := migrate.New(
	db,
	dialect,
	migrate.WithTable("schema_migrations"),
	migrate.WithLockTimeout(15*time.Second),
)
```

Ossein rejects invalid table identifiers, duplicate versions, missing up/down
files, changed migration names, and applied migrations missing from the local
source. The lock timeout defaults to 30 seconds and must be positive.

## Deployment concurrency

A runner always serializes operations within its process. PostgreSQL and MySQL
also acquire database-level locks derived from the metadata table name:

- PostgreSQL uses `pg_advisory_lock`;
- MySQL uses the session-level `GET_LOCK` and `RELEASE_LOCK` functions.

The lock and the complete migration operation use one dedicated `database/sql`
connection, so runners in separate processes cannot apply the same version
concurrently. Lock acquisition stops when the configured lock timeout or the
context passed to `Up` or `Down` expires. A configured timeout is reported as
`migrate.ErrLockTimeout`, which can be checked with `errors.Is`.

PostgreSQL and MySQL runners using different metadata tables receive different
locks and can operate independently. MySQL named locks are server-wide, so
applications sharing one MySQL server should keep metadata table names unique
when they need independent migration schedules.

SQLite currently has only the in-process lock. Run SQLite migrations as one
deployment job until a multi-process strategy is implemented.

## CLI wiring

The Ossein CLI forwards migration commands to the application's
`./cmd/server` entry point. The application owns the database driver, DSN, and
migration source, so it must wire them before starting the HTTP server.

Compose migrations and seeders through one application-owned command handler:

```go
commands := data.Commands{
	DB:          db,
	Driver:      config.Database.Driver,
	MigrationFS: os.DirFS("."),
	Seeders:     applicationSeeders,
}
```

Call it from `main` before `app.RunContext`:

```go
handled, err := commands.HandleCommand(
	ctx,
	os.Args[1:],
	os.Stdout,
)
if err != nil {
	log.Fatal(err)
}
if handled {
	return
}
```

The available commands are:

```bash
ossein migrate
ossein migrate --limit 1
ossein migrate:rollback
ossein migrate:rollback --steps 2
ossein migrate:status
```

`--limit 0` applies every pending migration. Rollback defaults to one step.
The generated starter rejects migration commands until this database boundary
is wired, rather than accidentally starting the HTTP server.
