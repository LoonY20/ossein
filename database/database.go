// Package database integrates standard library database/sql connections with
// Ossein configuration, dependency injection, and application lifecycle.
//
// Ossein does not select or bundle a SQL driver. Applications import the driver
// they need and choose it through Config.Driver.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	ossein "github.com/LoonY20/ossein"
)

const defaultPingTimeout = 5 * time.Second

// Config describes a database/sql connection and its pool.
//
// Config can be nested in an application config loaded with ossein.LoadConfig.
// Changing DB_DRIVER and DB_DSN selects another registered database/sql driver
// without changing application wiring.
type Config struct {
	Driver          string        `env:"DB_DRIVER" required:"true"`
	DSN             string        `env:"DB_DSN" required:"true"`
	MaxOpenConns    int           `env:"DB_MAX_OPEN_CONNS" default:"25"`
	MaxIdleConns    int           `env:"DB_MAX_IDLE_CONNS" default:"5"`
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" default:"30m"`
	ConnMaxIdleTime time.Duration `env:"DB_CONN_MAX_IDLE_TIME" default:"5m"`
	PingTimeout     time.Duration `env:"DB_PING_TIMEOUT" default:"5s"`
}

// Open validates config, creates a database/sql pool, and applies pool limits.
// It does not contact the database; Register adds a startup ping to Ossein's
// lifecycle.
func Open(config Config) (*sql.DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	db, err := sql.Open(config.Driver, config.DSN)
	if err != nil {
		return nil, fmt.Errorf("ossein database: open driver %q: %w", config.Driver, err)
	}
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	return db, nil
}

// Register opens a database/sql pool, exposes *sql.DB through Ossein's service
// container, verifies connectivity during application startup, and closes the
// pool during shutdown.
func Register(app *ossein.App, config Config) (*sql.DB, error) {
	if app == nil {
		return nil, errors.New("ossein database: app cannot be nil")
	}

	db, err := Open(config)
	if err != nil {
		return nil, err
	}
	if err := ossein.Instance(app, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ossein database: register pool: %w", err)
	}

	pingTimeout := config.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = defaultPingTimeout
	}
	app.OnStart(func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			return fmt.Errorf("ossein database: ping: %w", err)
		}
		return nil
	})
	app.OnStop(func(context.Context) error {
		if err := db.Close(); err != nil {
			return fmt.Errorf("ossein database: close: %w", err)
		}
		return nil
	})

	return db, nil
}

// Validate checks configuration values without opening a pool.
func (c Config) Validate() error {
	var configErrors []error
	if strings.TrimSpace(c.Driver) == "" {
		configErrors = append(configErrors, errors.New("ossein database: driver is required"))
	}
	if strings.TrimSpace(c.DSN) == "" {
		configErrors = append(configErrors, errors.New("ossein database: DSN is required"))
	}
	if c.MaxOpenConns < 0 {
		configErrors = append(configErrors, errors.New("ossein database: max open connections cannot be negative"))
	}
	if c.MaxIdleConns < 0 {
		configErrors = append(configErrors, errors.New("ossein database: max idle connections cannot be negative"))
	}
	if c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		configErrors = append(configErrors, errors.New("ossein database: max idle connections cannot exceed max open connections"))
	}
	if c.ConnMaxLifetime < 0 {
		configErrors = append(configErrors, errors.New("ossein database: connection max lifetime cannot be negative"))
	}
	if c.ConnMaxIdleTime < 0 {
		configErrors = append(configErrors, errors.New("ossein database: connection max idle time cannot be negative"))
	}
	if c.PingTimeout < 0 {
		configErrors = append(configErrors, errors.New("ossein database: ping timeout cannot be negative"))
	}
	return errors.Join(configErrors...)
}
