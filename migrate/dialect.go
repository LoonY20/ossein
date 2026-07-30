package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrLockTimeout reports that a migration runner could not acquire the
// database-specific lock before its configured timeout.
var ErrLockTimeout = errors.New("ossein migrate: lock timeout")

// Dialect describes the small amount of SQL syntax required by the migration
// metadata table.
type Dialect struct {
	name              string
	quoteLeft         string
	quoteRight        string
	placeholder       func(int) string
	lock              func(context.Context, *sql.Conn, int64, time.Duration) error
	unlock            func(context.Context, *sql.Conn, int64) error
	transactionalLock bool
}

// Name returns the dialect's stable name.
func (d Dialect) Name() string {
	return d.name
}

// PostgreSQL returns the PostgreSQL migration dialect.
func PostgreSQL() Dialect {
	return Dialect{
		name:       "postgres",
		quoteLeft:  `"`,
		quoteRight: `"`,
		placeholder: func(position int) string {
			return fmt.Sprintf("$%d", position)
		},
		lock: func(ctx context.Context, conn *sql.Conn, key int64, _ time.Duration) error {
			_, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key)
			return err
		},
		unlock: func(ctx context.Context, conn *sql.Conn, key int64) error {
			var released bool
			if err := conn.QueryRowContext(
				ctx,
				"SELECT pg_advisory_unlock($1)",
				key,
			).Scan(&released); err != nil {
				return err
			}
			if !released {
				return errors.New("advisory lock was not held")
			}
			return nil
		},
	}
}

// MySQL returns the MySQL migration dialect.
func MySQL() Dialect {
	return Dialect{
		name:        "mysql",
		quoteLeft:   "`",
		quoteRight:  "`",
		placeholder: func(int) string { return "?" },
		lock: func(ctx context.Context, conn *sql.Conn, key int64, timeout time.Duration) error {
			var acquired sql.NullInt64
			if err := conn.QueryRowContext(
				ctx,
				"SELECT GET_LOCK(?, ?)",
				mysqlLockName(key),
				int64(math.Ceil(timeout.Seconds())),
			).Scan(&acquired); err != nil {
				return err
			}
			if !acquired.Valid {
				return errors.New("GET_LOCK returned NULL")
			}
			if acquired.Int64 != 1 {
				return ErrLockTimeout
			}
			return nil
		},
		unlock: func(ctx context.Context, conn *sql.Conn, key int64) error {
			var released sql.NullInt64
			if err := conn.QueryRowContext(
				ctx,
				"SELECT RELEASE_LOCK(?)",
				mysqlLockName(key),
			).Scan(&released); err != nil {
				return err
			}
			if !released.Valid {
				return errors.New("named lock did not exist")
			}
			if released.Int64 != 1 {
				return errors.New("named lock was not held by this connection")
			}
			return nil
		},
	}
}

// SQLite returns the SQLite migration dialect.
func SQLite() Dialect {
	return Dialect{
		name:              "sqlite",
		quoteLeft:         `"`,
		quoteRight:        `"`,
		placeholder:       func(int) string { return "?" },
		transactionalLock: true,
	}
}

// DialectForDriver maps common database/sql driver names to a migration
// dialect.
func DialectForDriver(driverName string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(driverName)) {
	case "pgx", "postgres", "postgresql":
		return PostgreSQL(), nil
	case "mysql":
		return MySQL(), nil
	case "sqlite", "sqlite3":
		return SQLite(), nil
	default:
		return Dialect{}, fmt.Errorf("ossein migrate: unsupported database driver %q", driverName)
	}
}

func (d Dialect) validate() error {
	if d.name == "" || d.quoteLeft == "" || d.quoteRight == "" || d.placeholder == nil {
		return errors.New("ossein migrate: invalid dialect")
	}
	if (d.lock == nil) != (d.unlock == nil) {
		return errors.New("ossein migrate: dialect lock and unlock must be configured together")
	}
	if d.transactionalLock && d.lock != nil {
		return errors.New("ossein migrate: dialect cannot combine session and transactional locks")
	}
	return nil
}

func (d Dialect) quote(identifier string) string {
	return d.quoteLeft + identifier + d.quoteRight
}

func mysqlLockName(key int64) string {
	return fmt.Sprintf("ossein:migrate:%016x", uint64(key))
}
