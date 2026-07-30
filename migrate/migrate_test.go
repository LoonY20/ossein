package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func TestLoadFS(t *testing.T) {
	files := fstest.MapFS{
		"migrations/000002_add_email.up.sql": {
			Data: []byte("ALTER TABLE users ADD email TEXT;\n"),
		},
		"migrations/000002_add_email.down.sql": {
			Data: []byte("ALTER TABLE users DROP COLUMN email;\n"),
		},
		"migrations/000001_create-users.up.sql": {
			Data: []byte("CREATE TABLE users (id BIGINT);\n-- ossein:split\nCREATE INDEX users_id ON users (id);\n"),
		},
		"migrations/000001_create-users.down.sql": {
			Data: []byte("DROP TABLE users;\n"),
		},
		"migrations/README.md": {Data: []byte("ignored")},
	}

	migrations, err := LoadFS(files, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 2 || migrations[0].Version != 1 || migrations[0].Name != "create_users" {
		t.Fatalf("migrations = %#v", migrations)
	}
	if len(migrations[0].Up) != 2 {
		t.Fatalf("up statements = %#v", migrations[0].Up)
	}
}

func TestLoadFSValidationErrors(t *testing.T) {
	if _, err := LoadFS(nil, "."); err == nil {
		t.Fatal("expected nil filesystem error")
	}
	if _, err := LoadFS(fstest.MapFS{}, "missing"); err == nil {
		t.Fatal("expected missing directory error")
	}
	files := fstest.MapFS{
		"001_one.up.sql": {Data: []byte("UP")},
	}
	if _, err := LoadFS(files, "."); err == nil {
		t.Fatal("expected missing down migration error")
	}
}

func TestDialectForDriver(t *testing.T) {
	tests := map[string]string{
		"pgx": "postgres", "postgresql": "postgres",
		"mysql": "mysql", "sqlite3": "sqlite",
	}
	for driverName, expected := range tests {
		dialect, err := DialectForDriver(driverName)
		if err != nil || dialect.Name() != expected {
			t.Fatalf("DialectForDriver(%q) = %q, %v", driverName, dialect.Name(), err)
		}
	}
	if _, err := DialectForDriver("oracle"); err == nil {
		t.Fatal("expected unsupported driver error")
	}
	if migrationLockKey("ossein_migrations") != migrationLockKey("ossein_migrations") ||
		migrationLockKey("ossein_migrations") == migrationLockKey("other_migrations") {
		t.Fatal("migration lock keys are not stable and table-specific")
	}
}

func TestRunnerUpDownAndStatus(t *testing.T) {
	state, db := openMigrationDB(t)
	runner, err := New(db, PostgreSQL())
	if err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Unix(1_700_000_000, 0).UTC()
	runner.now = func() time.Time { return fixedTime }
	migrations := testMigrations()

	count, err := runner.Up(context.Background(), migrations, 1)
	if err != nil || count != 1 {
		t.Fatalf("first Up() = %d, %v", count, err)
	}
	statuses, err := runner.Statuses(context.Background(), migrations)
	if err != nil {
		t.Fatal(err)
	}
	if !statuses[0].Applied || statuses[0].AppliedAt != fixedTime || statuses[1].Applied {
		t.Fatalf("statuses = %#v", statuses)
	}

	count, err = runner.Up(context.Background(), migrations, 0)
	if err != nil || count != 1 {
		t.Fatalf("second Up() = %d, %v", count, err)
	}
	count, err = runner.Up(context.Background(), migrations, 0)
	if err != nil || count != 0 {
		t.Fatalf("idempotent Up() = %d, %v", count, err)
	}

	count, err = runner.Down(context.Background(), migrations, 1)
	if err != nil || count != 1 {
		t.Fatalf("Down() = %d, %v", count, err)
	}
	state.mu.Lock()
	executed := append([]string(nil), state.executed...)
	locks, unlocks := state.locks, state.unlocks
	state.mu.Unlock()
	expected := []string{"CREATE USERS", "CREATE USER INDEX", "ADD EMAIL", "DROP EMAIL"}
	if !reflect.DeepEqual(executed, expected) {
		t.Fatalf("executed = %#v", executed)
	}
	if locks != 4 || unlocks != locks {
		t.Fatalf("advisory lock calls = %d acquire, %d release", locks, unlocks)
	}
}

func TestRunnerRollsBackFailedMigration(t *testing.T) {
	state, db := openMigrationDB(t)
	state.failStatement = "FAIL"
	runner, err := New(db, SQLite())
	if err != nil {
		t.Fatal(err)
	}
	migrations := []Migration{{
		Version: 1, Name: "broken",
		Up: []string{"BEFORE", "FAIL", "AFTER"}, Down: []string{"DOWN"},
	}}
	if count, err := runner.Up(context.Background(), migrations, 0); err == nil || count != 0 {
		t.Fatalf("Up() = %d, %v", count, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.applied) != 0 || len(state.executed) != 0 {
		t.Fatalf("rollback state: applied=%v executed=%v", state.applied, state.executed)
	}
}

func TestRunnerValidation(t *testing.T) {
	_, db := openMigrationDB(t)
	if _, err := New(nil, PostgreSQL()); err == nil {
		t.Fatal("expected nil database error")
	}
	if _, err := New(db, Dialect{}); err == nil {
		t.Fatal("expected invalid dialect error")
	}
	invalidLockDialect := SQLite()
	invalidLockDialect.lock = PostgreSQL().lock
	if _, err := New(db, invalidLockDialect); err == nil {
		t.Fatal("expected incomplete dialect lock error")
	}
	if _, err := New(db, MySQL(), WithTable("bad-name")); err == nil {
		t.Fatal("expected invalid table error")
	}
	if _, err := New(db, MySQL(), WithLockTimeout(0)); err == nil {
		t.Fatal("expected invalid lock timeout error")
	}
	runner, err := New(
		db,
		MySQL(),
		WithTable("schema_migrations"),
		WithLockTimeout(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), testMigrations(), -1); err == nil {
		t.Fatal("expected negative limit error")
	}
	if _, err := runner.Down(context.Background(), testMigrations(), 0); err == nil {
		t.Fatal("expected rollback steps error")
	}

	invalid := []Migration{
		{Version: 0, Name: "", Up: nil, Down: nil},
		{Version: 1, Name: "one", Up: []string{"UP"}, Down: []string{"DOWN"}},
		{Version: 1, Name: "duplicate", Up: []string{"UP"}, Down: []string{"DOWN"}},
	}
	if _, err := ValidateMigrations(invalid); err == nil {
		t.Fatal("expected migration validation errors")
	}
}

func TestMySQLRunnerUsesNamedLock(t *testing.T) {
	state, db := openMigrationDB(t)
	runner, err := New(db, MySQL(), WithLockTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if count, err := runner.Up(context.Background(), testMigrations(), 1); err != nil || count != 1 {
		t.Fatalf("Up() = %d, %v", count, err)
	}

	state.mu.Lock()
	locks, unlocks := state.locks, state.unlocks
	lockName := state.lockName
	lockTimeout := state.lockTimeout
	state.mu.Unlock()
	if locks != 1 || unlocks != 1 {
		t.Fatalf("named lock calls = %d acquire, %d release", locks, unlocks)
	}
	if lockName != mysqlLockName(migrationLockKey(defaultTable)) {
		t.Fatalf("lock name = %q", lockName)
	}
	if lockTimeout != 1 {
		t.Fatalf("lock timeout = %d", lockTimeout)
	}
	if len(lockName) > 64 {
		t.Fatalf("lock name exceeds MySQL limit: %q", lockName)
	}
}

func TestMySQLRunnerReportsLockTimeout(t *testing.T) {
	state, db := openMigrationDB(t)
	state.mysqlLockResult = 0
	runner, err := New(db, MySQL(), WithLockTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), testMigrations(), 1); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Up() error = %v, want ErrLockTimeout", err)
	}
}

func TestRunnerDetectsNameAndLocalHistoryMismatch(t *testing.T) {
	state, db := openMigrationDB(t)
	state.applied[1] = recordedMigration{name: "old_name", appliedAt: 1}
	runner, err := New(db, PostgreSQL())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(context.Background(), testMigrations(), 0); err == nil {
		t.Fatal("expected applied name mismatch")
	}

	state.mu.Lock()
	state.applied = map[int64]recordedMigration{99: {name: "missing", appliedAt: 1}}
	state.mu.Unlock()
	if _, err := runner.Down(context.Background(), testMigrations(), 1); err == nil {
		t.Fatal("expected missing local migration error")
	}
	statuses, err := runner.Statuses(context.Background(), testMigrations())
	if err != nil {
		t.Fatal(err)
	}
	if statuses[len(statuses)-1].Version != 99 || !statuses[len(statuses)-1].Applied {
		t.Fatalf("orphan status = %#v", statuses)
	}
}

func testMigrations() []Migration {
	return []Migration{
		{Version: 2, Name: "add_email", Up: []string{"ADD EMAIL"}, Down: []string{"DROP EMAIL"}},
		{Version: 1, Name: "create_users", Up: []string{"CREATE USERS", "CREATE USER INDEX"}, Down: []string{"DROP USERS"}},
	}
}

type recordedMigration struct {
	name      string
	appliedAt int64
}

type migrationDriverState struct {
	mu              sync.Mutex
	applied         map[int64]recordedMigration
	executed        []string
	failStatement   string
	locks           int
	unlocks         int
	mysqlLockResult int64
	lockName        string
	lockTimeout     int64
}

type pendingState struct {
	applied  map[int64]recordedMigration
	executed []string
}

type migrationDriver struct {
	state *migrationDriverState
}

func (d *migrationDriver) Open(string) (driver.Conn, error) {
	return &migrationConnection{state: d.state}, nil
}

type migrationConnection struct {
	state   *migrationDriverState
	pending *pendingState
}

func (*migrationConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (*migrationConnection) Close() error { return nil }

func (c *migrationConnection) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *migrationConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.pending != nil {
		return nil, errors.New("transaction already active")
	}
	c.pending = &pendingState{
		applied:  cloneApplied(c.state.applied),
		executed: append([]string(nil), c.state.executed...),
	}
	return &migrationTransaction{connection: c}, nil
}

func (c *migrationConnection) ExecContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	normalized := strings.ToUpper(strings.TrimSpace(query))
	if strings.Contains(normalized, c.state.failStatement) && c.state.failStatement != "" {
		return nil, errors.New("forced statement failure")
	}

	targetApplied := c.state.applied
	targetExecuted := &c.state.executed
	if c.pending != nil {
		targetApplied = c.pending.applied
		targetExecuted = &c.pending.executed
	}
	switch {
	case strings.Contains(normalized, "PG_ADVISORY_LOCK"):
		c.state.locks++
	case strings.HasPrefix(normalized, "CREATE TABLE IF NOT EXISTS"):
	case strings.HasPrefix(normalized, "INSERT INTO"):
		version := namedInt64(args[0])
		targetApplied[version] = recordedMigration{
			name: namedString(args[1]), appliedAt: namedInt64(args[2]),
		}
	case strings.HasPrefix(normalized, "DELETE FROM"):
		delete(targetApplied, namedInt64(args[0]))
	default:
		*targetExecuted = append(*targetExecuted, strings.TrimSpace(query))
	}
	return driver.RowsAffected(1), nil
}

func (c *migrationConnection) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	normalized := strings.ToUpper(query)
	if strings.Contains(normalized, "PG_ADVISORY_UNLOCK") {
		c.state.unlocks++
		return &migrationRows{
			columns: []string{"pg_advisory_unlock"},
			values:  [][]driver.Value{{true}},
		}, nil
	}
	if strings.Contains(normalized, "GET_LOCK") {
		c.state.locks++
		c.state.lockName = namedString(args[0])
		c.state.lockTimeout = namedInt64(args[1])
		return &migrationRows{
			columns: []string{"get_lock"},
			values:  [][]driver.Value{{c.state.mysqlLockResult}},
		}, nil
	}
	if strings.Contains(normalized, "RELEASE_LOCK") {
		c.state.unlocks++
		return &migrationRows{
			columns: []string{"release_lock"},
			values:  [][]driver.Value{{int64(1)}},
		}, nil
	}
	versions := make([]int64, 0, len(c.state.applied))
	for version := range c.state.applied {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	values := make([][]driver.Value, 0, len(versions))
	for _, version := range versions {
		recorded := c.state.applied[version]
		values = append(values, []driver.Value{version, recorded.name, recorded.appliedAt})
	}
	return &migrationRows{
		columns: []string{"version", "name", "applied_at"},
		values:  values,
	}, nil
}

type migrationTransaction struct {
	connection *migrationConnection
}

func (t *migrationTransaction) Commit() error {
	t.connection.state.mu.Lock()
	defer t.connection.state.mu.Unlock()
	t.connection.state.applied = t.connection.pending.applied
	t.connection.state.executed = t.connection.pending.executed
	t.connection.pending = nil
	return nil
}

func (t *migrationTransaction) Rollback() error {
	t.connection.state.mu.Lock()
	defer t.connection.state.mu.Unlock()
	t.connection.pending = nil
	return nil
}

type migrationRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *migrationRows) Columns() []string { return r.columns }
func (*migrationRows) Close() error        { return nil }

func (r *migrationRows) Next(destination []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(destination, r.values[r.index])
	r.index++
	return nil
}

func cloneApplied(source map[int64]recordedMigration) map[int64]recordedMigration {
	result := make(map[int64]recordedMigration, len(source))
	for version, migration := range source {
		result[version] = migration
	}
	return result
}

func namedInt64(value driver.NamedValue) int64 {
	return value.Value.(int64)
}

func namedString(value driver.NamedValue) string {
	return value.Value.(string)
}

var migrationDriverSequence atomic.Uint64

func openMigrationDB(t *testing.T) (*migrationDriverState, *sql.DB) {
	t.Helper()
	state := &migrationDriverState{
		applied:         make(map[int64]recordedMigration),
		mysqlLockResult: 1,
	}
	name := fmt.Sprintf("ossein-migrate-test-%d", migrationDriverSequence.Add(1))
	sql.Register(name, &migrationDriver{state: state})
	db, err := sql.Open(name, "memory")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return state, db
}

var _ driver.ExecerContext = (*migrationConnection)(nil)
var _ driver.QueryerContext = (*migrationConnection)(nil)
var _ driver.ConnBeginTx = (*migrationConnection)(nil)
