package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"sync"
	"time"
)

const (
	defaultTable       = "ossein_migrations"
	defaultLockTimeout = 30 * time.Second
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Option configures a Runner.
type Option func(*Runner) error

// WithTable changes the migration metadata table.
func WithTable(table string) Option {
	return func(runner *Runner) error {
		if !identifierPattern.MatchString(table) {
			return fmt.Errorf("ossein migrate: invalid table name %q", table)
		}
		runner.table = table
		return nil
	}
}

// WithLockTimeout limits how long Up and Down wait for a database-specific
// migration lock. The default is 30 seconds.
func WithLockTimeout(timeout time.Duration) Option {
	return func(runner *Runner) error {
		if timeout <= 0 {
			return errors.New("ossein migrate: lock timeout must be positive")
		}
		runner.lockTimeout = timeout
		return nil
	}
}

// Runner applies migrations through a database/sql pool.
type Runner struct {
	db          *sql.DB
	dialect     Dialect
	table       string
	lockTimeout time.Duration
	mu          sync.Mutex
	now         func() time.Time
}

// New creates a migration runner.
func New(db *sql.DB, dialect Dialect, options ...Option) (*Runner, error) {
	if db == nil {
		return nil, errors.New("ossein migrate: database cannot be nil")
	}
	if err := dialect.validate(); err != nil {
		return nil, err
	}
	runner := &Runner{
		db:          db,
		dialect:     dialect,
		table:       defaultTable,
		lockTimeout: defaultLockTimeout,
		now:         time.Now,
	}
	for _, option := range options {
		if option != nil {
			if err := option(runner); err != nil {
				return nil, err
			}
		}
	}
	return runner, nil
}

// AppliedMigration is a migration recorded in the database.
type AppliedMigration struct {
	Version   int64
	Name      string
	AppliedAt time.Time
}

// Status describes whether a local migration is applied.
type Status struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
}

// Up applies pending migrations in ascending version order. A limit of zero
// applies every pending migration.
func (r *Runner) Up(ctx context.Context, migrations []Migration, limit int) (count int, err error) {
	if limit < 0 {
		return 0, errors.New("ossein migrate: up limit cannot be negative")
	}
	sorted, err := ValidateMigrations(migrations)
	if err != nil {
		return 0, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dialect.transactionalLock {
		return r.upWithTransactionalLock(ctx, sorted, limit)
	}
	conn, release, err := r.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, release())
	}()
	if err := r.ensureTable(ctx, conn); err != nil {
		return 0, err
	}
	applied, err := r.applied(ctx, conn)
	if err != nil {
		return 0, err
	}
	appliedByVersion := make(map[int64]AppliedMigration, len(applied))
	for _, migration := range applied {
		appliedByVersion[migration.Version] = migration
	}

	for _, migration := range sorted {
		if existing, ok := appliedByVersion[migration.Version]; ok {
			if existing.Name != migration.Name {
				return count, fmt.Errorf("ossein migrate: version %d is recorded as %q, local name is %q", migration.Version, existing.Name, migration.Name)
			}
			continue
		}
		if limit > 0 && count >= limit {
			break
		}
		if err := r.applyUp(ctx, conn, migration); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Down rolls back the most recently applied migrations.
func (r *Runner) Down(ctx context.Context, migrations []Migration, steps int) (count int, err error) {
	if steps <= 0 {
		return 0, errors.New("ossein migrate: rollback steps must be positive")
	}
	sorted, err := ValidateMigrations(migrations)
	if err != nil {
		return 0, err
	}
	local := make(map[int64]Migration, len(sorted))
	for _, migration := range sorted {
		local[migration.Version] = migration
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dialect.transactionalLock {
		return r.downWithTransactionalLock(ctx, local, steps)
	}
	conn, release, err := r.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, release())
	}()
	if err := r.ensureTable(ctx, conn); err != nil {
		return 0, err
	}
	applied, err := r.applied(ctx, conn)
	if err != nil {
		return 0, err
	}
	sort.Slice(applied, func(i, j int) bool { return applied[i].Version > applied[j].Version })

	for _, recorded := range applied {
		if count >= steps {
			break
		}
		migration, ok := local[recorded.Version]
		if !ok {
			return count, fmt.Errorf("ossein migrate: applied version %d is missing locally", recorded.Version)
		}
		if migration.Name != recorded.Name {
			return count, fmt.Errorf("ossein migrate: version %d is recorded as %q, local name is %q", recorded.Version, recorded.Name, migration.Name)
		}
		if err := r.applyDown(ctx, conn, migration); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Statuses returns local migrations and their database state.
func (r *Runner) Statuses(ctx context.Context, migrations []Migration) ([]Status, error) {
	sorted, err := ValidateMigrations(migrations)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureTable(ctx, r.db); err != nil {
		return nil, err
	}
	applied, err := r.applied(ctx, r.db)
	if err != nil {
		return nil, err
	}
	appliedByVersion := make(map[int64]AppliedMigration, len(applied))
	for _, migration := range applied {
		appliedByVersion[migration.Version] = migration
	}

	statuses := make([]Status, 0, len(sorted))
	for _, migration := range sorted {
		status := Status{Version: migration.Version, Name: migration.Name}
		if recorded, ok := appliedByVersion[migration.Version]; ok {
			if recorded.Name != migration.Name {
				return nil, fmt.Errorf("ossein migrate: version %d is recorded as %q, local name is %q", migration.Version, recorded.Name, migration.Name)
			}
			status.Applied = true
			status.AppliedAt = recorded.AppliedAt
			delete(appliedByVersion, migration.Version)
		}
		statuses = append(statuses, status)
	}
	for _, recorded := range appliedByVersion {
		statuses = append(statuses, Status{
			Version: recorded.Version, Name: recorded.Name,
			Applied: true, AppliedAt: recorded.AppliedAt,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Version < statuses[j].Version })
	return statuses, nil
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (r *Runner) ensureTable(ctx context.Context, connection queryer) error {
	table := r.dialect.quote(r.table)
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (version BIGINT PRIMARY KEY, name VARCHAR(255) NOT NULL, applied_at BIGINT NOT NULL)",
		table,
	)
	if _, err := connection.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("ossein migrate: create metadata table: %w", err)
	}
	return nil
}

func (r *Runner) applied(ctx context.Context, connection queryer) ([]AppliedMigration, error) {
	query := fmt.Sprintf(
		"SELECT version, name, applied_at FROM %s ORDER BY version ASC",
		r.dialect.quote(r.table),
	)
	rows, err := connection.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("ossein migrate: list applied migrations: %w", err)
	}
	defer rows.Close()

	var migrations []AppliedMigration
	for rows.Next() {
		var migration AppliedMigration
		var unix int64
		if err := rows.Scan(&migration.Version, &migration.Name, &unix); err != nil {
			return nil, fmt.Errorf("ossein migrate: scan applied migration: %w", err)
		}
		migration.AppliedAt = time.Unix(unix, 0).UTC()
		migrations = append(migrations, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ossein migrate: iterate applied migrations: %w", err)
	}
	return migrations, nil
}

func (r *Runner) applyUp(ctx context.Context, conn *sql.Conn, migration Migration) error {
	return r.transaction(ctx, conn, migration, func(tx *sql.Tx) error {
		return r.applyUpOn(ctx, tx, migration)
	})
}

func (r *Runner) applyDown(ctx context.Context, conn *sql.Conn, migration Migration) error {
	return r.transaction(ctx, conn, migration, func(tx *sql.Tx) error {
		return r.applyDownOn(ctx, tx, migration)
	})
}

func (r *Runner) transaction(
	ctx context.Context,
	conn *sql.Conn,
	migration Migration,
	apply func(*sql.Tx) error,
) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ossein migrate: begin migration %d: %w", migration.Version, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := apply(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ossein migrate: commit migration %d: %w", migration.Version, err)
	}
	committed = true
	return nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r *Runner) applyUpOn(ctx context.Context, connection execer, migration Migration) error {
	if err := executeStatements(ctx, connection, migration, migration.Up); err != nil {
		return err
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (version, name, applied_at) VALUES (%s, %s, %s)",
		r.dialect.quote(r.table),
		r.dialect.placeholder(1), r.dialect.placeholder(2), r.dialect.placeholder(3),
	)
	if _, err := connection.ExecContext(
		ctx,
		query,
		migration.Version,
		migration.Name,
		r.now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("ossein migrate: record migration %d: %w", migration.Version, err)
	}
	return nil
}

func (r *Runner) applyDownOn(ctx context.Context, connection execer, migration Migration) error {
	if err := executeStatements(ctx, connection, migration, migration.Down); err != nil {
		return err
	}
	query := fmt.Sprintf(
		"DELETE FROM %s WHERE version = %s",
		r.dialect.quote(r.table), r.dialect.placeholder(1),
	)
	if _, err := connection.ExecContext(ctx, query, migration.Version); err != nil {
		return fmt.Errorf("ossein migrate: record migration %d rollback: %w", migration.Version, err)
	}
	return nil
}

func executeStatements(
	ctx context.Context,
	connection execer,
	migration Migration,
	statements []string,
) error {
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf(
				"ossein migrate: execute migration %d (%s): %w",
				migration.Version,
				migration.Name,
				err,
			)
		}
	}
	return nil
}

func (r *Runner) acquire(ctx context.Context) (*sql.Conn, func() error, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("ossein migrate: acquire connection: %w", err)
	}
	if r.dialect.lock == nil {
		return conn, conn.Close, nil
	}

	key := migrationLockKey(r.table)
	lockCtx, cancel := context.WithTimeout(ctx, r.lockTimeout)
	lockErr := r.dialect.lock(lockCtx, conn, key, r.lockTimeout)
	cancel()
	if lockErr != nil {
		_ = conn.Close()
		if errors.Is(lockErr, context.DeadlineExceeded) && ctx.Err() == nil {
			lockErr = ErrLockTimeout
		}
		return nil, nil, fmt.Errorf("ossein migrate: acquire %s lock: %w", r.dialect.name, lockErr)
	}
	release := func() error {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		unlockErr := r.dialect.unlock(unlockCtx, conn, key)
		closeErr := conn.Close()
		if unlockErr != nil {
			unlockErr = fmt.Errorf("ossein migrate: release %s lock: %w", r.dialect.name, unlockErr)
		}
		if closeErr != nil {
			closeErr = fmt.Errorf("ossein migrate: release connection: %w", closeErr)
		}
		return errors.Join(unlockErr, closeErr)
	}
	return conn, release, nil
}

func migrationLockKey(table string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("github.com/LoonY20/ossein/migrate:"))
	_, _ = hash.Write([]byte(table))
	return int64(hash.Sum64())
}
