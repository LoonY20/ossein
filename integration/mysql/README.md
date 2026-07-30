# MySQL integration

This nested module keeps the MySQL driver out of Ossein's dependency-free core.

Run the suite against a disposable database:

```bash
OSSEIN_MYSQL_DSN='ossein:ossein@tcp(127.0.0.1:3306)/ossein_test?parseTime=true' go test -race ./...
```

The test starts two migration runners concurrently and verifies that MySQL's
session-level named lock permits exactly one runner to apply the migration.
