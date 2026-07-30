package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	osseindatabase "github.com/LoonY20/ossein/database"
	"github.com/LoonY20/ossein/migrate"
	_ "modernc.org/sqlite"
)

func TestConcurrentMigrationRunners(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "migrations.db"))
	firstDB := openDatabase(t, dsn)
	secondDB := openDatabase(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := firstDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := secondDB.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	migrations := []migrate.Migration{{
		Version: 1,
		Name:    "create_lock_test",
		Up: []string{
			"CREATE TABLE ossein_lock_test (id INTEGER PRIMARY KEY)",
		},
		Down: []string{
			"DROP TABLE ossein_lock_test",
		},
	}}

	holder, err := firstDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	timeoutRunner, err := migrate.New(
		secondDB,
		migrate.SQLite(),
		migrate.WithLockTimeout(100*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := timeoutRunner.Up(ctx, migrations, 0); !errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("locked Up() error = %v, want ErrLockTimeout", err)
	}
	if _, err := holder.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := migrate.New(
		firstDB,
		migrate.SQLite(),
		migrate.WithLockTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := migrate.New(
		secondDB,
		migrate.SQLite(),
		migrate.WithLockTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		count int
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, runner := range []*migrate.Runner{first, second} {
		go func(current *migrate.Runner) {
			<-start
			count, err := current.Up(ctx, migrations, 0)
			results <- result{count: count, err: err}
		}(runner)
	}
	close(start)

	counts := make([]int, 0, 2)
	for range 2 {
		current := <-results
		if current.err != nil {
			t.Fatal(current.err)
		}
		counts = append(counts, current.count)
	}
	sort.Ints(counts)
	if counts[0] != 0 || counts[1] != 1 {
		t.Fatalf("concurrent migration counts = %v, want [0 1]", counts)
	}

	statuses, err := first.Statuses(ctx, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || !statuses[0].Applied {
		t.Fatalf("statuses = %#v", statuses)
	}
	if count, err := first.Down(ctx, migrations, 1); err != nil || count != 1 {
		t.Fatalf("Down() = %d, %v", count, err)
	}

	var tableCount int
	if err := firstDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		"ossein_lock_test",
	).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("rolled-back table still exists")
	}
}

func openDatabase(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := osseindatabase.Open(osseindatabase.Config{
		Driver:          "sqlite",
		DSN:             dsn,
		MaxOpenConns:    2,
		MaxIdleConns:    2,
		ConnMaxLifetime: 3 * time.Minute,
		PingTimeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
