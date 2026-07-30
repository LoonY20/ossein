// Package data composes Ossein's database application commands.
package data

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"

	"github.com/LoonY20/ossein/migrate"
	"github.com/LoonY20/ossein/seed"
)

// Commands owns application-specific migration and seeding inputs.
type Commands struct {
	DB           *sql.DB
	Driver       string
	MigrationFS  fs.FS
	MigrationDir string
	Seeders      []seed.Seeder
}

// HandleCommand executes migrate, migrate:rollback, migrate:status, or db:seed.
//
// Migration files are loaded lazily so normal server startup and db:seed do
// not depend on the process working directory.
func (c Commands) HandleCommand(
	ctx context.Context,
	args []string,
	output io.Writer,
) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if seed.IsCommand(args[0]) {
		if c.DB == nil {
			return true, errors.New("ossein data: database cannot be nil")
		}
		return seed.HandleCommand(ctx, args, c.DB, c.Seeders, output)
	}
	if !migrate.IsCommand(args[0]) {
		return false, nil
	}
	if c.DB == nil {
		return true, errors.New("ossein data: database cannot be nil")
	}
	if c.MigrationFS == nil {
		return true, errors.New("ossein data: migration filesystem cannot be nil")
	}

	directory := c.MigrationDir
	if directory == "" {
		directory = "migrations"
	}
	migrations, err := migrate.LoadFS(c.MigrationFS, directory)
	if err != nil {
		return true, err
	}
	dialect, err := migrate.DialectForDriver(c.Driver)
	if err != nil {
		return true, err
	}
	runner, err := migrate.New(c.DB, dialect)
	if err != nil {
		return true, err
	}
	return migrate.HandleCommand(ctx, args, runner, migrations, output)
}
