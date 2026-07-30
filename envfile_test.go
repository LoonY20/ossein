package ossein

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := `
# application settings
APP_NAME="Ossein\nApp"
export HTTP_ADDRESS = ':9090'
DB_DSN=postgres://localhost/app#fragment
EMPTY=
COMMENTED=value # comment
QUOTED_COMMENT="hello world" # comment
APP_NAME=last-file-value
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_ADDRESS", ":8080")
	for _, key := range []string{
		"APP_NAME", "DB_DSN", "EMPTY", "COMMENTED", "QUOTED_COMMENT",
	} {
		_ = os.Unsetenv(key)
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}

	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"APP_NAME":       "last-file-value",
		"HTTP_ADDRESS":   ":8080",
		"DB_DSN":         "postgres://localhost/app#fragment",
		"EMPTY":          "",
		"COMMENTED":      "value",
		"QUOTED_COMMENT": "hello world",
	}
	for key, value := range expected {
		if actual := os.Getenv(key); actual != value {
			t.Fatalf("%s = %q, want %q", key, actual, value)
		}
	}
}

func TestLoadEnvFileErrorsAreAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("VALID=value\nnot valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("VALID")
	t.Cleanup(func() { _ = os.Unsetenv("VALID") })
	if err := LoadEnvFile(path); err == nil {
		t.Fatal("expected parse error")
	}
	if _, exists := os.LookupEnv("VALID"); exists {
		t.Fatal("partially applied invalid environment file")
	}

	if err := os.WriteFile(path, []byte("VALUE='unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err == nil {
		t.Fatal("expected quote error")
	}
}

func TestLoadEnvFileIfExists(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	if err := LoadEnvFileIfExists(missing); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
}
