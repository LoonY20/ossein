package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const statementSeparator = "-- ossein:split"

var migrationFilePattern = regexp.MustCompile(`^([0-9]+)_([a-zA-Z0-9_-]+)\.(up|down)\.sql$`)

// LoadFS loads paired .up.sql and .down.sql migrations from directory.
//
// File names use VERSION_NAME.up.sql and VERSION_NAME.down.sql. Multiple SQL
// statements are separated by a line containing "-- ossein:split".
func LoadFS(filesystem fs.FS, directory string) ([]Migration, error) {
	if filesystem == nil {
		return nil, errors.New("ossein migrate: filesystem cannot be nil")
	}
	entries, err := fs.ReadDir(filesystem, directory)
	if err != nil {
		return nil, fmt.Errorf("ossein migrate: read %s: %w", directory, err)
	}

	type pair struct {
		migration Migration
		hasUp     bool
		hasDown   bool
	}
	pairs := make(map[int64]*pair)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ossein migrate: parse version in %s: %w", entry.Name(), err)
		}
		name := strings.ReplaceAll(matches[2], "-", "_")
		current, ok := pairs[version]
		if !ok {
			current = &pair{migration: Migration{Version: version, Name: name}}
			pairs[version] = current
		} else if current.migration.Name != name {
			return nil, fmt.Errorf("ossein migrate: version %d uses multiple names", version)
		}

		content, err := fs.ReadFile(filesystem, path.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("ossein migrate: read %s: %w", entry.Name(), err)
		}
		statements := splitStatements(string(content))
		if matches[3] == "up" {
			if current.hasUp {
				return nil, fmt.Errorf("ossein migrate: duplicate up migration for version %d", version)
			}
			current.hasUp = true
			current.migration.Up = statements
		} else {
			if current.hasDown {
				return nil, fmt.Errorf("ossein migrate: duplicate down migration for version %d", version)
			}
			current.hasDown = true
			current.migration.Down = statements
		}
	}

	migrations := make([]Migration, 0, len(pairs))
	for version, current := range pairs {
		if !current.hasUp || !current.hasDown {
			return nil, fmt.Errorf("ossein migrate: version %d requires both up and down files", version)
		}
		migrations = append(migrations, current.migration)
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return ValidateMigrations(migrations)
}

func splitStatements(content string) []string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var statements []string
	var current strings.Builder
	flush := func() {
		if statement := strings.TrimSpace(current.String()); statement != "" {
			statements = append(statements, statement)
		}
		current.Reset()
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == statementSeparator {
			flush()
			continue
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	flush()
	return statements
}
