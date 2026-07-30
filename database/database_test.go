package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
)

type driverState struct {
	opened  atomic.Int32
	pinged  atomic.Int32
	closed  atomic.Int32
	pingErr error
}

type testDriver struct {
	state *driverState
}

func (d *testDriver) Open(string) (driver.Conn, error) {
	d.state.opened.Add(1)
	return &testConnection{state: d.state}, nil
}

type testConnection struct {
	state *driverState
}

func (*testConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (c *testConnection) Close() error {
	c.state.closed.Add(1)
	return nil
}

func (*testConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("not implemented")
}

func (c *testConnection) Ping(context.Context) error {
	c.state.pinged.Add(1)
	return c.state.pingErr
}

func TestConfigLoadsThroughOssein(t *testing.T) {
	type appConfig struct {
		Database Config
	}
	values := map[string]string{
		"DB_DRIVER":             "test",
		"DB_DSN":                "memory",
		"DB_MAX_OPEN_CONNS":     "12",
		"DB_MAX_IDLE_CONNS":     "4",
		"DB_CONN_MAX_LIFETIME":  "1h",
		"DB_CONN_MAX_IDLE_TIME": "10m",
		"DB_PING_TIMEOUT":       "2s",
	}
	config, err := ossein.LoadConfigFromEnv[appConfig](func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Driver != "test" || config.Database.MaxOpenConns != 12 ||
		config.Database.ConnMaxLifetime != time.Hour || config.Database.PingTimeout != 2*time.Second {
		t.Fatalf("database config = %#v", config.Database)
	}
}

func TestRegisterIntegratesDIAndLifecycle(t *testing.T) {
	state := &driverState{}
	name := registerTestDriver(state)
	app := ossein.New()

	db, err := Register(app, Config{
		Driver:          name,
		DSN:             "memory",
		MaxOpenConns:    7,
		MaxIdleConns:    3,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 30 * time.Second,
		PingTimeout:     time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ossein.Resolve[*sql.DB](app)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != db {
		t.Fatal("DI did not return the registered pool")
	}
	if stats := db.Stats(); stats.MaxOpenConnections != 7 {
		t.Fatalf("max open connections = %d", stats.MaxOpenConnections)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.opened.Load() != 1 || state.pinged.Load() != 1 {
		t.Fatalf("opened=%d pinged=%d", state.opened.Load(), state.pinged.Load())
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.closed.Load() != 1 {
		t.Fatalf("closed=%d", state.closed.Load())
	}
}

func TestRegisterReturnsPingFailure(t *testing.T) {
	expected := errors.New("database unavailable")
	state := &driverState{pingErr: expected}
	app := ossein.New()
	_, err := Register(app, validConfig(registerTestDriver(state)))
	if err != nil {
		t.Fatal(err)
	}
	err = app.Start(context.Background())
	if !errors.Is(err, expected) {
		t.Fatalf("start error = %v", err)
	}
	_ = app.Stop(context.Background())
}

func TestValidationAndRegistrationErrors(t *testing.T) {
	invalid := Config{
		MaxOpenConns:    -1,
		MaxIdleConns:    -1,
		ConnMaxLifetime: -1,
		ConnMaxIdleTime: -1,
		PingTimeout:     -1,
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected joined validation errors")
	}
	if _, err := Register(nil, validConfig("missing")); err == nil {
		t.Fatal("expected nil app error")
	}
	if _, err := Open(validConfig("missing")); err == nil {
		t.Fatal("expected unknown driver error")
	}

	state := &driverState{}
	app := ossein.New()
	if err := ossein.Instance(app, &sql.DB{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(app, validConfig(registerTestDriver(state))); err == nil {
		t.Fatal("expected duplicate DI registration error")
	}
}

func TestConfigRejectsIdleConnectionsAboveOpenLimit(t *testing.T) {
	config := validConfig("driver")
	config.MaxOpenConns = 2
	config.MaxIdleConns = 3
	if err := config.Validate(); err == nil {
		t.Fatal("expected pool limit validation error")
	}
}

func validConfig(driverName string) Config {
	return Config{
		Driver:       driverName,
		DSN:          "memory",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		PingTimeout:  time.Second,
	}
}

var driverSequence atomic.Uint64

func registerTestDriver(state *driverState) string {
	name := fmt.Sprintf("ossein-test-%d", driverSequence.Add(1))
	sql.Register(name, &testDriver{state: state})
	return name
}

var _ driver.Pinger = (*testConnection)(nil)
var _ io.Closer = (*testConnection)(nil)
