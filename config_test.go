package ossein

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigFromEnv(t *testing.T) {
	type Config struct {
		App struct {
			Name    string        `env:"APP_NAME" required:"true"`
			Debug   bool          `env:"APP_DEBUG" default:"false"`
			Timeout time.Duration `env:"APP_TIMEOUT" default:"5s"`
		}
		Port int `env:"PORT" default:"8080"`
	}

	values := map[string]string{
		"APP_NAME":  "Ossein",
		"APP_DEBUG": "true",
	}

	config, err := LoadConfigFromEnv[Config](func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("unexpected config error: %v", err)
	}

	if config.App.Name != "Ossein" {
		t.Fatalf("expected app name Ossein, got %q", config.App.Name)
	}
	if !config.App.Debug {
		t.Fatal("expected debug to be true")
	}
	if config.App.Timeout != 5*time.Second {
		t.Fatalf("expected timeout 5s, got %s", config.App.Timeout)
	}
	if config.Port != 8080 {
		t.Fatalf("expected port 8080, got %d", config.Port)
	}
}

func TestLoadConfigRequiresTaggedValue(t *testing.T) {
	type Config struct {
		DatabaseURL string `env:"DATABASE_URL" required:"true"`
	}

	_, err := LoadConfigFromEnv[Config](func(string) (string, bool) {
		return "", false
	})
	if err == nil {
		t.Fatal("expected required config error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected error to mention DATABASE_URL, got %v", err)
	}
}

func TestLoadConfigRejectsNonStructTarget(t *testing.T) {
	_, err := LoadConfigFromEnv[string](nil)
	if err == nil {
		t.Fatal("expected non-struct config error")
	}
}

func TestLoadConfigSupportsScalarTypes(t *testing.T) {
	type Config struct {
		String   string        `env:"STRING"`
		Bool     bool          `env:"BOOL"`
		Int8     int8          `env:"INT8"`
		Uint16   uint16        `env:"UINT16"`
		Float32  float32       `env:"FLOAT32"`
		Duration time.Duration `env:"DURATION"`
		Ignored  string        `env:"-"`
		Untagged string
	}
	values := map[string]string{
		"STRING": "value", "BOOL": "true", "INT8": "-8",
		"UINT16": "16", "FLOAT32": "3.5", "DURATION": "250ms",
	}
	config, err := LoadConfigFromEnv[Config](func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.String != "value" || !config.Bool || config.Int8 != -8 ||
		config.Uint16 != 16 || config.Float32 != 3.5 || config.Duration != 250*time.Millisecond {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestLoadConfigReportsAllParsingErrors(t *testing.T) {
	type Config struct {
		Bool     bool          `env:"BOOL"`
		Int      int           `env:"INT"`
		Uint     uint          `env:"UINT"`
		Float    float64       `env:"FLOAT"`
		Duration time.Duration `env:"DURATION"`
		Slice    []string      `env:"SLICE"`
	}
	values := map[string]string{
		"BOOL": "nope", "INT": "nope", "UINT": "-1",
		"FLOAT": "nope", "DURATION": "soon", "SLICE": "a,b",
	}
	_, err := LoadConfigFromEnv[Config](func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err == nil {
		t.Fatal("expected parsing errors")
	}
	for _, expected := range []string{"Bool", "Int", "Uint", "Float", "Duration", "Slice"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error does not contain %s: %v", expected, err)
		}
	}
}

func TestLoadConfigUsesProcessEnvironment(t *testing.T) {
	type Config struct {
		Name string `env:"OSSEIN_TEST_NAME"`
	}
	t.Setenv("OSSEIN_TEST_NAME", "process")
	config, err := LoadConfig[Config]()
	if err != nil {
		t.Fatal(err)
	}
	if config.Name != "process" {
		t.Fatalf("name = %q", config.Name)
	}
	if _, exists := os.LookupEnv("OSSEIN_TEST_NAME"); !exists {
		t.Fatal("test environment was not set")
	}
}
