package seed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

const command = "db:seed"

// IsCommand reports whether command is handled by HandleCommand.
func IsCommand(value string) bool {
	return value == command
}

// HandleCommand executes db:seed from application arguments.
func HandleCommand(
	ctx context.Context,
	args []string,
	db *sql.DB,
	seeders []Seeder,
	output io.Writer,
) (handled bool, err error) {
	if len(args) == 0 || !IsCommand(args[0]) {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("ossein seed: db:seed does not accept arguments")
	}
	if db == nil {
		return true, errors.New("ossein seed: database cannot be nil")
	}
	if output == nil {
		output = io.Discard
	}
	count, err := Run(ctx, db, seeders...)
	if err != nil {
		return true, err
	}
	_, err = fmt.Fprintf(output, "Ran %d seeder(s).\n", count)
	return true, err
}
