package seed

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRunExecutesSeedersInOrder(t *testing.T) {
	state, db := openSeedDB(t)
	seeders := []Seeder{
		{Name: " users ", Seed: execute("INSERT USERS")},
		{Name: "posts", Seed: execute("INSERT POSTS")},
	}
	count, err := Run(context.Background(), db, seeders...)
	if err != nil || count != 2 {
		t.Fatalf("Run() = %d, %v", count, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !reflect.DeepEqual(state.committed, []string{"INSERT USERS", "INSERT POSTS"}) {
		t.Fatalf("committed = %#v", state.committed)
	}
	if state.commits != 2 || state.rollbacks != 0 {
		t.Fatalf("commits=%d rollbacks=%d", state.commits, state.rollbacks)
	}
}

func TestRunStopsAndRollsBackFailure(t *testing.T) {
	state, db := openSeedDB(t)
	expected := errors.New("posts failed")
	count, err := Run(context.Background(), db,
		Seeder{Name: "users", Seed: execute("INSERT USERS")},
		Seeder{Name: "posts", Seed: func(context.Context, *sql.Tx) error {
			return expected
		}},
		Seeder{Name: "comments", Seed: execute("INSERT COMMENTS")},
	)
	if count != 1 || !errors.Is(err, expected) {
		t.Fatalf("Run() = %d, %v", count, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !reflect.DeepEqual(state.committed, []string{"INSERT USERS"}) {
		t.Fatalf("committed = %#v", state.committed)
	}
	if state.commits != 1 || state.rollbacks != 1 {
		t.Fatalf("commits=%d rollbacks=%d", state.commits, state.rollbacks)
	}
}

func TestRunValidatesAllSeedersBeforeExecution(t *testing.T) {
	state, db := openSeedDB(t)
	count, err := Run(context.Background(), db,
		Seeder{Name: "", Seed: execute("ONE")},
		Seeder{Name: "duplicate", Seed: execute("TWO")},
		Seeder{Name: "duplicate", Seed: execute("THREE")},
		Seeder{Name: "missing"},
	)
	if count != 0 || err == nil {
		t.Fatalf("Run() = %d, %v", count, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.begins != 0 {
		t.Fatalf("begins = %d", state.begins)
	}
}

func TestHandleCommand(t *testing.T) {
	_, db := openSeedDB(t)
	var output bytes.Buffer
	handled, err := HandleCommand(
		context.Background(),
		[]string{"db:seed"},
		db,
		[]Seeder{{Name: "users", Seed: execute("INSERT USERS")}},
		&output,
	)
	if err != nil || !handled || output.String() != "Ran 1 seeder(s).\n" {
		t.Fatalf("HandleCommand() = %t, %v, %q", handled, err, output.String())
	}

	handled, err = HandleCommand(context.Background(), []string{"serve"}, db, nil, io.Discard)
	if err != nil || handled {
		t.Fatalf("other command = %t, %v", handled, err)
	}
	if !IsCommand("db:seed") || IsCommand("seed") {
		t.Fatal("IsCommand returned an unexpected result")
	}

	for _, test := range []struct {
		args []string
		db   *sql.DB
	}{
		{args: []string{"db:seed", "--again"}, db: db},
		{args: []string{"db:seed"}, db: nil},
	} {
		handled, err := HandleCommand(
			context.Background(),
			test.args,
			test.db,
			nil,
			nil,
		)
		if !handled || err == nil {
			t.Fatalf("HandleCommand(%v) = %t, %v", test.args, handled, err)
		}
	}
}

func execute(query string) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query)
		return err
	}
}

type seedDriverState struct {
	mu        sync.Mutex
	begins    int
	commits   int
	rollbacks int
	committed []string
	pending   []string
}

type seedDriver struct {
	state *seedDriverState
}

func (d *seedDriver) Open(string) (driver.Conn, error) {
	return &seedConnection{state: d.state}, nil
}

type seedConnection struct {
	state *seedDriverState
}

func (*seedConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (*seedConnection) Close() error { return nil }

func (c *seedConnection) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *seedConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.begins++
	c.state.pending = nil
	return &seedTx{state: c.state}, nil
}

func (c *seedConnection) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.pending = append(c.state.pending, query)
	return driver.RowsAffected(1), nil
}

type seedTx struct {
	state *seedDriverState
}

func (tx *seedTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.commits++
	tx.state.committed = append(tx.state.committed, tx.state.pending...)
	tx.state.pending = nil
	return nil
}

func (tx *seedTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rollbacks++
	tx.state.pending = nil
	return nil
}

var seedDriverSequence atomic.Uint64

func openSeedDB(t *testing.T) (*seedDriverState, *sql.DB) {
	t.Helper()
	state := &seedDriverState{}
	name := fmt.Sprintf("ossein-seed-test-%d", seedDriverSequence.Add(1))
	sql.Register(name, &seedDriver{state: state})
	db, err := sql.Open(name, "memory")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return state, db
}

var _ driver.ConnBeginTx = (*seedConnection)(nil)
var _ driver.ExecerContext = (*seedConnection)(nil)
