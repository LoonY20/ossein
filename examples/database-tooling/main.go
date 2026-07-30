// Command database-tooling demonstrates how Ossein composes with pgx and sqlx
// through the standard library instead of framework-specific adapters.
//
// The pgx stdlib driver powers the ordinary database/sql pool that
// database.Register manages, and sqlx wraps that same pool for richer struct
// scanning. One pool, one lifecycle, no adapter layer.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/database"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type config struct {
	HTTP struct {
		Address string `env:"HTTP_ADDRESS" default:":8080"`
	}

	Database database.Config
}

type user struct {
	ID   int64  `db:"id" json:"id"`
	Name string `db:"name" json:"name"`
}

type userRepository struct {
	db *sqlx.DB
}

func newUserRepository(db *sqlx.DB) *userRepository {
	return &userRepository{db: db}
}

func (r *userRepository) find(ctx context.Context, id int64) (user, error) {
	var found user
	err := r.db.GetContext(ctx, &found, "SELECT id, name FROM users WHERE id = $1", id)
	return found, err
}

func main() {
	if err := ossein.LoadEnvFileIfExists(".env"); err != nil {
		log.Fatal(err)
	}
	settings, err := ossein.LoadConfig[config]()
	if err != nil {
		log.Fatal(err)
	}

	app := ossein.New()

	// database.Register opens a standard library pool on the configured
	// driver (DB_DRIVER=pgx) and ties ping and close to the app lifecycle.
	db, err := database.Register(app, settings.Database)
	if err != nil {
		log.Fatal(err)
	}

	// sqlx wraps the same *sql.DB, so repositories share the managed pool.
	if err := ossein.Instance(app, sqlx.NewDb(db, settings.Database.Driver)); err != nil {
		log.Fatal(err)
	}
	if err := app.Provide(newUserRepository); err != nil {
		log.Fatal(err)
	}

	users, err := ossein.Resolve[*userRepository](app)
	if err != nil {
		log.Fatal(err)
	}

	app.Get("/users/{id}", func(ctx *ossein.Context) error {
		id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
		if err != nil {
			return ossein.BadRequest("invalid_id", "User ID must be an integer")
		}
		found, err := users.find(ctx.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			return ossein.NotFound("user_not_found", "User not found")
		}
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, found)
	}).Named("users.show")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunContext(ctx, settings.HTTP.Address); err != nil {
		log.Fatal(err)
	}
}
