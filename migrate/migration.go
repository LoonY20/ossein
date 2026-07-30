// Package migrate provides dialect-aware, transactional SQL migrations on top
// of database/sql.
package migrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Migration is one ordered database schema change.
type Migration struct {
	Version int64
	Name    string
	Up      []string
	Down    []string
}

// ValidateMigrations validates and sorts migrations by ascending version.
func ValidateMigrations(migrations []Migration) ([]Migration, error) {
	sorted := append([]Migration(nil), migrations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	seen := make(map[int64]struct{}, len(sorted))
	var migrationErrors []error
	for i := range sorted {
		migration := &sorted[i]
		migration.Name = strings.TrimSpace(migration.Name)
		if migration.Version <= 0 {
			migrationErrors = append(migrationErrors, fmt.Errorf("ossein migrate: version must be positive: %d", migration.Version))
		}
		if _, exists := seen[migration.Version]; exists {
			migrationErrors = append(migrationErrors, fmt.Errorf("ossein migrate: duplicate version %d", migration.Version))
		}
		seen[migration.Version] = struct{}{}
		if migration.Name == "" {
			migrationErrors = append(migrationErrors, fmt.Errorf("ossein migrate: migration %d has an empty name", migration.Version))
		}
		migration.Up = cleanStatements(migration.Up)
		migration.Down = cleanStatements(migration.Down)
		if len(migration.Up) == 0 {
			migrationErrors = append(migrationErrors, fmt.Errorf("ossein migrate: migration %d has no up statements", migration.Version))
		}
		if len(migration.Down) == 0 {
			migrationErrors = append(migrationErrors, fmt.Errorf("ossein migrate: migration %d has no down statements", migration.Version))
		}
	}
	return sorted, errors.Join(migrationErrors...)
}

func cleanStatements(statements []string) []string {
	cleaned := make([]string, 0, len(statements))
	for _, statement := range statements {
		if statement = strings.TrimSpace(statement); statement != "" {
			cleaned = append(cleaned, statement)
		}
	}
	return cleaned
}
