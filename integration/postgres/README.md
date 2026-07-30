# PostgreSQL integration tests

This nested Go module keeps the pgx test driver out of Ossein's dependency-free
runtime module.

Run the suite against a disposable PostgreSQL database:

```bash
OSSEIN_POSTGRES_DSN='postgres://ossein:ossein@127.0.0.1:5432/ossein_test?sslmode=disable' go test -race ./...
```

The test verifies real DDL up/down behavior, metadata status, and serialization
of two concurrent migration runners through PostgreSQL advisory locks. Tables
use unique names and are removed during cleanup.
