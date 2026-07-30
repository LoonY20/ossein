# SQLite integration

This nested Go module keeps the SQLite test driver out of Ossein's
dependency-free core.

Run the suite with:

```bash
go test -race ./...
```

The test opens two independent `database/sql` pools on one file. It verifies
bounded lock waiting and proves that concurrent migration runners apply a
version exactly once.
