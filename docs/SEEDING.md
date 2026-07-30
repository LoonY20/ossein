# Seeding

Ossein seeders are ordered, named functions over the standard `*sql.Tx`. Each
seeder runs in its own transaction. A failure rolls back that seeder and stops
the remaining sequence.

## Defining seeders

```go
var seeders = []seed.Seeder{
	{
		Name: "users",
		Seed: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(
				ctx,
				`INSERT INTO users (name, email) VALUES ($1, $2)`,
				"Admin",
				"admin@example.com",
			)
			return err
		},
	},
}
```

Run them directly:

```go
count, err := seed.Run(ctx, db, seeders...)
```

Seeders are validated as a complete set before execution. Names must be
non-empty and unique, and every seeder must provide a function. Ossein does not
track seed history or impose idempotency; applications decide whether a seeder
inserts once, upserts, or resets development data.

## CLI wiring

Wire `db:seed` beside migrations in `cmd/server`:

```go
commands := data.Commands{
	DB:          db,
	Driver:      config.Database.Driver,
	MigrationFS: os.DirFS("."),
	Seeders:     seeders,
}

handled, err := commands.HandleCommand(ctx, os.Args[1:], os.Stdout)
if err != nil {
	log.Fatal(err)
}
if handled {
	return
}
```

Then run:

```bash
ossein db:seed
```

The Ossein CLI forwards the command to `go run ./cmd/server db:seed`, so the
application remains responsible for its driver, database connection, and
seeder list.
