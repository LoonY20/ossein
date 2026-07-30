package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TransactionFunc is work executed inside a database transaction.
type TransactionFunc func(context.Context, *sql.Tx) error

// WithinTransaction executes work in a transaction.
//
// It commits when work succeeds, rolls back when work returns an error, and
// rolls back before re-panicking when work panics.
func WithinTransaction(
	ctx context.Context,
	db *sql.DB,
	options *sql.TxOptions,
	work TransactionFunc,
) (err error) {
	if db == nil {
		return errors.New("ossein database: transaction database cannot be nil")
	}
	if work == nil {
		return errors.New("ossein database: transaction work cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("ossein database: begin transaction: %w", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
	}()

	if err := work(ctx, tx); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("ossein database: rollback transaction: %w", rollbackErr)
		}
		return errors.Join(err, rollbackErr)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ossein database: commit transaction: %w", err)
	}
	return nil
}
