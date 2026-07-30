package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWithinTransactionCommits(t *testing.T) {
	state, db := openTransactionDB(t)
	err := WithinTransaction(nil, db, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(
		ctx context.Context,
		tx *sql.Tx,
	) error {
		if ctx == nil {
			t.Fatal("transaction context is nil")
		}
		_, err := tx.ExecContext(ctx, "INSERT USER")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.begins != 1 || state.commits != 1 || state.rollbacks != 0 {
		t.Fatalf("transaction state = %#v", state)
	}
	if len(state.executed) != 1 || state.executed[0] != "INSERT USER" {
		t.Fatalf("executed = %#v", state.executed)
	}
	if state.options.Isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Fatalf("isolation = %d", state.options.Isolation)
	}
}

func TestWithinTransactionRollsBackError(t *testing.T) {
	state, db := openTransactionDB(t)
	expected := errors.New("work failed")
	err := WithinTransaction(context.Background(), db, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("transaction error = %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.commits != 0 || state.rollbacks != 1 {
		t.Fatalf("transaction state = %#v", state)
	}
}

func TestWithinTransactionRollsBackPanic(t *testing.T) {
	state, db := openTransactionDB(t)
	defer func() {
		if recovered := recover(); recovered != "boom" {
			t.Fatalf("recovered = %#v", recovered)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.rollbacks != 1 {
			t.Fatalf("rollbacks = %d", state.rollbacks)
		}
	}()
	_ = WithinTransaction(context.Background(), db, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		panic("boom")
	})
}

func TestWithinTransactionValidationAndDriverErrors(t *testing.T) {
	if err := WithinTransaction(context.Background(), nil, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		return nil
	}); err == nil {
		t.Fatal("expected nil database error")
	}
	_, db := openTransactionDB(t)
	if err := WithinTransaction(context.Background(), db, nil, nil); err == nil {
		t.Fatal("expected nil work error")
	}

	state, failingDB := openTransactionDB(t)
	state.beginErr = errors.New("begin failed")
	if err := WithinTransaction(context.Background(), failingDB, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		return nil
	}); !errors.Is(err, state.beginErr) {
		t.Fatalf("begin error = %v", err)
	}

	state.beginErr = nil
	state.commitErr = errors.New("commit failed")
	if err := WithinTransaction(context.Background(), failingDB, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		return nil
	}); !errors.Is(err, state.commitErr) {
		t.Fatalf("commit error = %v", err)
	}

	state.commitErr = nil
	state.rollbackErr = errors.New("rollback failed")
	workErr := errors.New("work failed")
	err := WithinTransaction(context.Background(), failingDB, nil, func(
		context.Context,
		*sql.Tx,
	) error {
		return workErr
	})
	if !errors.Is(err, workErr) || !errors.Is(err, state.rollbackErr) {
		t.Fatalf("joined rollback error = %v", err)
	}
}

type transactionDriverState struct {
	mu          sync.Mutex
	begins      int
	commits     int
	rollbacks   int
	executed    []string
	options     driver.TxOptions
	beginErr    error
	commitErr   error
	rollbackErr error
}

type transactionDriver struct {
	state *transactionDriverState
}

func (d *transactionDriver) Open(string) (driver.Conn, error) {
	return &transactionConnection{state: d.state}, nil
}

type transactionConnection struct {
	state *transactionDriverState
}

func (*transactionConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (*transactionConnection) Close() error { return nil }

func (c *transactionConnection) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *transactionConnection) BeginTx(
	_ context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.beginErr != nil {
		return nil, c.state.beginErr
	}
	c.state.begins++
	c.state.options = options
	return &transactionTx{state: c.state}, nil
}

func (c *transactionConnection) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.executed = append(c.state.executed, query)
	return driver.RowsAffected(1), nil
}

type transactionTx struct {
	state *transactionDriverState
}

func (tx *transactionTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	return tx.state.commitErr
}

func (tx *transactionTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rollbacks++
	return tx.state.rollbackErr
}

var transactionDriverSequence atomic.Uint64

func openTransactionDB(t *testing.T) (*transactionDriverState, *sql.DB) {
	t.Helper()
	state := &transactionDriverState{}
	name := fmt.Sprintf("ossein-transaction-test-%d", transactionDriverSequence.Add(1))
	sql.Register(name, &transactionDriver{state: state})
	db, err := sql.Open(name, "memory")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return state, db
}

var _ driver.ConnBeginTx = (*transactionConnection)(nil)
var _ driver.ExecerContext = (*transactionConnection)(nil)
