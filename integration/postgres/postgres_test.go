package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	osseindatabase "github.com/LoonY20/ossein/database"
	"github.com/LoonY20/ossein/migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestConcurrentMigrationRunners(t *testing.T) {
	dsn := os.Getenv("OSSEIN_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OSSEIN_POSTGRES_DSN is not set")
	}

	db, err := osseindatabase.Open(osseindatabase.Config{
		Driver:       "pgx",
		DSN:          dsn,
		MaxOpenConns: 4,
		MaxIdleConns: 2,
		PingTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	dataTable := fmt.Sprintf("ossein_lock_test_%d", suffix)
	metadataTable := fmt.Sprintf("ossein_migrations_%d", suffix)
	dropIntegrationTables(t, db, dataTable, metadataTable)
	t.Cleanup(func() {
		dropIntegrationTables(t, db, dataTable, metadataTable)
	})

	migrations := []migrate.Migration{{
		Version: 1,
		Name:    "create_lock_test",
		Up: []string{
			"SELECT pg_sleep(0.25)",
			fmt.Sprintf(`CREATE TABLE "%s" (id BIGINT PRIMARY KEY)`, dataTable),
		},
		Down: []string{
			fmt.Sprintf(`DROP TABLE "%s"`, dataTable),
		},
	}}
	first, err := migrate.New(
		db,
		migrate.PostgreSQL(),
		migrate.WithTable(metadataTable),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := migrate.New(
		db,
		migrate.PostgreSQL(),
		migrate.WithTable(metadataTable),
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

	var relation sql.NullString
	if err := db.QueryRowContext(
		ctx,
		"SELECT to_regclass($1)",
		"public."+dataTable,
	).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation.Valid {
		t.Fatalf("rolled-back table still exists as %q", relation.String)
	}
}

func dropIntegrationTables(t *testing.T, db *sql.DB, dataTable, metadataTable string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, table := range []string{dataTable, metadataTable} {
		if _, err := db.ExecContext(
			ctx,
			fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, table),
		); err != nil {
			t.Errorf("drop integration table %s: %v", table, err)
		}
	}
}
