package migrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestHandleCommandWorkflow(t *testing.T) {
	_, db := openMigrationDB(t)
	runner, err := New(db, PostgreSQL())
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time {
		return time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	}
	migrations := testMigrations()
	var output bytes.Buffer

	handled, err := HandleCommand(
		context.Background(),
		[]string{"migrate", "--limit", "1"},
		runner,
		migrations,
		&output,
	)
	if err != nil || !handled {
		t.Fatalf("migrate handled=%t, err=%v", handled, err)
	}
	if output.String() != "Applied 1 migration(s).\n" {
		t.Fatalf("migrate output = %q", output.String())
	}

	output.Reset()
	handled, err = HandleCommand(
		context.Background(),
		[]string{"migrate:status"},
		runner,
		migrations,
		&output,
	)
	if err != nil || !handled {
		t.Fatalf("status handled=%t, err=%v", handled, err)
	}
	for _, expected := range []string{
		"STATE", "VERSION", "NAME", "APPLIED AT",
		"applied", "1", "create_users", "2026-07-30T12:00:00Z",
		"pending", "2", "add_email", "-",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status output does not contain %q:\n%s", expected, output.String())
		}
	}

	output.Reset()
	handled, err = HandleCommand(
		nil,
		[]string{"migrate:rollback", "--steps=1"},
		runner,
		migrations,
		&output,
	)
	if err != nil || !handled {
		t.Fatalf("rollback handled=%t, err=%v", handled, err)
	}
	if output.String() != "Rolled back 1 migration(s).\n" {
		t.Fatalf("rollback output = %q", output.String())
	}
}

func TestHandleCommandValidation(t *testing.T) {
	_, db := openMigrationDB(t)
	runner, err := New(db, SQLite())
	if err != nil {
		t.Fatal(err)
	}
	migrations := testMigrations()

	handled, err := HandleCommand(context.Background(), nil, runner, migrations, io.Discard)
	if err != nil || handled {
		t.Fatalf("empty command handled=%t, err=%v", handled, err)
	}
	handled, err = HandleCommand(
		context.Background(),
		[]string{"serve"},
		runner,
		migrations,
		io.Discard,
	)
	if err != nil || handled {
		t.Fatalf("other command handled=%t, err=%v", handled, err)
	}
	if !IsCommand("migrate:status") || IsCommand("serve") {
		t.Fatal("IsCommand returned an unexpected result")
	}

	tests := []struct {
		name   string
		args   []string
		runner *Runner
	}{
		{name: "nil runner", args: []string{"migrate"}, runner: nil},
		{name: "invalid flag", args: []string{"migrate", "--wat"}, runner: runner},
		{name: "unexpected argument", args: []string{"migrate", "now"}, runner: runner},
		{name: "negative limit", args: []string{"migrate", "--limit=-1"}, runner: runner},
		{name: "zero steps", args: []string{"migrate:rollback", "--steps=0"}, runner: runner},
		{name: "status argument", args: []string{"migrate:status", "--verbose"}, runner: runner},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handled, err := HandleCommand(
				context.Background(),
				test.args,
				test.runner,
				migrations,
				nil,
			)
			if !handled || err == nil {
				t.Fatalf("handled=%t, err=%v", handled, err)
			}
		})
	}
}

func TestHandleCommandPropagatesOutputError(t *testing.T) {
	_, db := openMigrationDB(t)
	runner, err := New(db, MySQL())
	if err != nil {
		t.Fatal(err)
	}
	handled, err := HandleCommand(
		context.Background(),
		[]string{"migrate"},
		runner,
		testMigrations(),
		failingWriter{},
	)
	if !handled || err == nil {
		t.Fatalf("handled=%t, err=%v", handled, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
