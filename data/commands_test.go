package data

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCommandsIgnoreUnrelatedArguments(t *testing.T) {
	handled, err := (Commands{}).HandleCommand(
		context.Background(),
		[]string{"routes"},
		io.Discard,
	)
	if handled || err != nil {
		t.Fatalf("HandleCommand() = %t, %v", handled, err)
	}
}

func TestCommandsRunEmptySeederSet(t *testing.T) {
	var output bytes.Buffer
	handled, err := (Commands{DB: &sql.DB{}}).HandleCommand(
		context.Background(),
		[]string{"db:seed"},
		&output,
	)
	if !handled || err != nil || output.String() != "Ran 0 seeder(s).\n" {
		t.Fatalf("HandleCommand() = %t, %v, %q", handled, err, output.String())
	}
}

func TestCommandsValidateRecognizedInputs(t *testing.T) {
	tests := []struct {
		name     string
		commands Commands
		args     []string
		contains string
	}{
		{
			name: "seed database",
			args: []string{"db:seed"}, contains: "database cannot be nil",
		},
		{
			name: "migration database",
			args: []string{"migrate"}, contains: "database cannot be nil",
		},
		{
			name:     "migration filesystem",
			commands: Commands{DB: &sql.DB{}, Driver: "pgx"},
			args:     []string{"migrate"}, contains: "filesystem cannot be nil",
		},
		{
			name: "unsupported driver",
			commands: Commands{
				DB: &sql.DB{}, Driver: "oracle",
				MigrationFS: validMigrations(),
			},
			args: []string{"migrate"}, contains: "unsupported database driver",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handled, err := test.commands.HandleCommand(
				context.Background(),
				test.args,
				io.Discard,
			)
			if !handled || err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("HandleCommand() = %t, %v", handled, err)
			}
		})
	}
}

func validMigrations() fstest.MapFS {
	return fstest.MapFS{
		"migrations/000001_create.up.sql": {
			Data: []byte("CREATE TABLE things (id BIGINT)"),
		},
		"migrations/000001_create.down.sql": {
			Data: []byte("DROP TABLE things"),
		},
	}
}
