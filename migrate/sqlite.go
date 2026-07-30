package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func (r *Runner) upWithTransactionalLock(
	ctx context.Context,
	migrations []Migration,
	limit int,
) (count int, err error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("ossein migrate: acquire connection: %w", err)
	}
	defer func() {
		err = errors.Join(err, conn.Close())
	}()

	for limit == 0 || count < limit {
		appliedOne := false
		hasMore := false
		err := r.withSQLiteTransaction(ctx, conn, func() error {
			if err := r.ensureTable(ctx, conn); err != nil {
				return err
			}
			applied, err := r.applied(ctx, conn)
			if err != nil {
				return err
			}
			appliedByVersion := make(map[int64]AppliedMigration, len(applied))
			for _, migration := range applied {
				appliedByVersion[migration.Version] = migration
			}

			var next *Migration
			for index := range migrations {
				migration := &migrations[index]
				if existing, ok := appliedByVersion[migration.Version]; ok {
					if existing.Name != migration.Name {
						return fmt.Errorf(
							"ossein migrate: version %d is recorded as %q, local name is %q",
							migration.Version,
							existing.Name,
							migration.Name,
						)
					}
					continue
				}
				if next == nil {
					next = migration
				} else {
					hasMore = true
				}
			}
			if next == nil {
				return nil
			}
			if err := r.applyUpOn(ctx, conn, *next); err != nil {
				return err
			}
			appliedOne = true
			return nil
		})
		if err != nil {
			return count, err
		}
		if !appliedOne {
			return count, nil
		}
		count++
		if !hasMore {
			return count, nil
		}
	}
	return count, nil
}

func (r *Runner) downWithTransactionalLock(
	ctx context.Context,
	local map[int64]Migration,
	steps int,
) (count int, err error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("ossein migrate: acquire connection: %w", err)
	}
	defer func() {
		err = errors.Join(err, conn.Close())
	}()

	for count < steps {
		rolledBackOne := false
		hasMore := false
		err := r.withSQLiteTransaction(ctx, conn, func() error {
			if err := r.ensureTable(ctx, conn); err != nil {
				return err
			}
			applied, err := r.applied(ctx, conn)
			if err != nil {
				return err
			}
			sort.Slice(applied, func(i, j int) bool {
				return applied[i].Version > applied[j].Version
			})
			if len(applied) == 0 {
				return nil
			}

			recorded := applied[0]
			migration, ok := local[recorded.Version]
			if !ok {
				return fmt.Errorf(
					"ossein migrate: applied version %d is missing locally",
					recorded.Version,
				)
			}
			if migration.Name != recorded.Name {
				return fmt.Errorf(
					"ossein migrate: version %d is recorded as %q, local name is %q",
					recorded.Version,
					recorded.Name,
					migration.Name,
				)
			}
			if err := r.applyDownOn(ctx, conn, migration); err != nil {
				return err
			}
			rolledBackOne = true
			hasMore = len(applied) > 1
			return nil
		})
		if err != nil {
			return count, err
		}
		if !rolledBackOne {
			return count, nil
		}
		count++
		if !hasMore {
			return count, nil
		}
	}
	return count, nil
}

func (r *Runner) withSQLiteTransaction(
	ctx context.Context,
	conn *sql.Conn,
	run func() error,
) (err error) {
	if err := r.beginSQLiteTransaction(ctx, conn); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, finishSQLiteTransaction(conn, "ROLLBACK"))
		}
	}()
	if err := run(); err != nil {
		return err
	}
	if err := finishSQLiteTransaction(conn, "COMMIT"); err != nil {
		return fmt.Errorf("ossein migrate: commit sqlite migration: %w", err)
	}
	committed = true
	return nil
}

func (r *Runner) beginSQLiteTransaction(ctx context.Context, conn *sql.Conn) error {
	timeoutMilliseconds := r.lockTimeout.Milliseconds()
	if timeoutMilliseconds < 1 {
		timeoutMilliseconds = 1
	}
	if timeoutMilliseconds > math.MaxInt32 {
		timeoutMilliseconds = math.MaxInt32
	}
	if _, err := conn.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA busy_timeout = %d", timeoutMilliseconds),
	); err != nil {
		return fmt.Errorf("ossein migrate: configure sqlite lock timeout: %w", err)
	}

	lockCtx, cancel := context.WithTimeout(ctx, r.lockTimeout)
	_, lockErr := conn.ExecContext(lockCtx, "BEGIN IMMEDIATE")
	cancel()
	if lockErr == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		lockErr = ctxErr
	} else if errors.Is(lockErr, context.DeadlineExceeded) || isSQLiteBusy(lockErr) {
		lockErr = ErrLockTimeout
	}
	return fmt.Errorf("ossein migrate: acquire sqlite lock: %w", lockErr)
}

func finishSQLiteTransaction(conn *sql.Conn, statement string) error {
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := conn.ExecContext(finishCtx, statement)
	return err
}

func isSQLiteBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
