// Package seed executes ordered, transactional database seeders.
package seed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LoonY20/ossein/database"
)

// Seeder is one named database population step.
//
// Seed runs in its own transaction. Returning an error rolls back that seeder
// and stops the remaining sequence.
type Seeder struct {
	Name string
	Seed func(context.Context, *sql.Tx) error
}

// Run validates and executes seeders in order.
func Run(ctx context.Context, db *sql.DB, seeders ...Seeder) (int, error) {
	if db == nil {
		return 0, errors.New("ossein seed: database cannot be nil")
	}
	validated, err := validate(seeders)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, seeder := range validated {
		err := database.WithinTransaction(ctx, db, nil, func(
			ctx context.Context,
			tx *sql.Tx,
		) error {
			if err := seeder.Seed(ctx, tx); err != nil {
				return fmt.Errorf("ossein seed: run %s: %w", seeder.Name, err)
			}
			return nil
		})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func validate(seeders []Seeder) ([]Seeder, error) {
	validated := append([]Seeder(nil), seeders...)
	seen := make(map[string]struct{}, len(validated))
	var validationErrors []error
	for index := range validated {
		validated[index].Name = strings.TrimSpace(validated[index].Name)
		if validated[index].Name == "" {
			validationErrors = append(
				validationErrors,
				fmt.Errorf("ossein seed: seeder %d has an empty name", index+1),
			)
			continue
		}
		if _, exists := seen[validated[index].Name]; exists {
			validationErrors = append(
				validationErrors,
				fmt.Errorf("ossein seed: duplicate seeder %q", validated[index].Name),
			)
		}
		seen[validated[index].Name] = struct{}{}
		if validated[index].Seed == nil {
			validationErrors = append(
				validationErrors,
				fmt.Errorf("ossein seed: seeder %s has no function", validated[index].Name),
			)
		}
	}
	return validated, errors.Join(validationErrors...)
}
