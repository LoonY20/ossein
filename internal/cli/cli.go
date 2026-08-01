// Package cli implements the Ossein developer command.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const usage = `Ossein developer tools

Usage:
  ossein new <name>                  Create an Ossein application
  ossein dev                         Run the application
  ossein routes                      List registered routes
  ossein migrate [--limit N]         Apply pending migrations
  ossein migrate:rollback [--steps N] Roll back migrations
  ossein migrate:status              Show migration status
  ossein db:seed                     Run application seeders
  ossein wire                        Generate explicit service wiring (experimental)
  ossein make:controller <name>      Generate a controller
  ossein make:middleware <name>      Generate middleware
  ossein make:request <name>         Generate a request type
  ossein version                     Print the CLI version
`

// Version is the current Ossein CLI version.
const Version = "0.2.0"

// Run executes a CLI command and returns a process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdin, stdout, stderr)
}

// RunContext executes a CLI command with cancellation.
func RunContext(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	return run(ctx, args, stdin, stdout, stderr, runGoCommand, runDev)
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	executeGoCommand func([]string, io.Reader, io.Writer, io.Writer) int,
	executeDev func(context.Context, io.Reader, io.Writer, io.Writer) int,
) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "ossein", Version)
		return 0
	case "new":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: ossein new <name>")
			return 2
		}
		if err := newProject(args[1]); err != nil {
			fmt.Fprintln(stderr, "ossein:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Created Ossein application %s\n\nNext steps:\n  cd %s\n  go mod tidy\n  ossein dev\n", args[1], args[1])
		return 0
	case "dev":
		return executeDev(ctx, stdin, stdout, stderr)
	case "routes":
		return executeGoCommand([]string{"run", "./cmd/server", "routes"}, stdin, stdout, stderr)
	case "migrate", "migrate:rollback", "migrate:status", "db:seed", "wire":
		// Every application command addresses ./cmd/server and writes relative
		// paths, so it only means anything from the module root. A generated file
		// carries a //go:generate directive, and go generate runs each directive in
		// the directory of the file that holds it — which for generated wiring is
		// internal/wiring, not the root. Moving there first is what makes the
		// directive work instead of failing the whole module's go generate.
		if err := changeToModuleRoot(); err != nil {
			fmt.Fprintln(stderr, "ossein:", err)
			return 1
		}
		commandArgs := append([]string{"run", "./cmd/server"}, args...)
		return executeGoCommand(commandArgs, stdin, stdout, stderr)
	case "make:controller", "make:middleware", "make:request":
		if len(args) != 2 {
			fmt.Fprintf(stderr, "usage: ossein %s <name>\n", args[0])
			return 2
		}
		path, err := generate(args[0], args[1])
		if err != nil {
			fmt.Fprintln(stderr, "ossein:", err)
			return 1
		}
		fmt.Fprintln(stdout, "Created", path)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runGoCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	command := exec.Command("go", args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "ossein:", err)
		return 1
	}
	return 0
}

func newProject(name string) error {
	if !validProjectName.MatchString(name) {
		return fmt.Errorf("project name must contain only letters, numbers, dots, dashes, or underscores")
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("%s already exists", name)
	} else if !os.IsNotExist(err) {
		return err
	}

	module := strings.ReplaceAll(name, "\\", "/")
	files := map[string]string{
		"go.mod":                       fmt.Sprintf(projectGoMod, module),
		".gitignore":                   projectGitignore,
		".env.example":                 projectEnv,
		"README.md":                    fmt.Sprintf(projectReadme, name),
		"cmd/server/main.go":           strings.ReplaceAll(projectMain, "APP_MODULE", module),
		"internal/config/config.go":    projectConfig,
		"internal/http/routes.go":      projectRoutes,
		"internal/http/health.go":      projectHealth,
		"internal/http/health_test.go": projectHealthTest,
	}
	for path, content := range files {
		fullPath := filepath.Join(name, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func generate(command, name string) (string, error) {
	typeName := exportedName(name)
	if typeName == "" {
		return "", fmt.Errorf("name must contain letters or numbers")
	}
	fileName := snakeCase(typeName) + ".go"
	var path, content string
	switch command {
	case "make:controller":
		path = filepath.Join("internal", "http", "controllers", fileName)
		content = fmt.Sprintf(controllerTemplate, typeName, typeName)
	case "make:middleware":
		path = filepath.Join("internal", "http", "middleware", fileName)
		content = fmt.Sprintf(middlewareTemplate, typeName, typeName)
	case "make:request":
		path = filepath.Join("internal", "http", "requests", fileName)
		content = fmt.Sprintf(requestTemplate, typeName, typeName)
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(path), nil
}

func exportedName(value string) string {
	parts := regexp.MustCompile(`[^a-zA-Z0-9]+`).Split(value, -1)
	var result strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		result.WriteString(string(runes))
	}
	return result.String()
}

// snakeCase converts an exported name to snake_case, keeping acronyms
// together: HTTPServer becomes http_server and APIKey becomes api_key.
func snakeCase(value string) string {
	runes := []rune(value)
	var result []rune
	for i, current := range runes {
		if unicode.IsUpper(current) && i > 0 {
			previousIsUpper := unicode.IsUpper(runes[i-1])
			nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if !previousIsUpper || nextIsLower {
				result = append(result, '_')
			}
		}
		result = append(result, unicode.ToLower(current))
	}
	return string(result)
}

var validProjectName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

const projectGoMod = `module %s

go 1.23.0
`

const projectGitignore = `.env
bin/
tmp/
`

const projectEnv = `APP_NAME=Ossein App
HTTP_ADDRESS=:8080
`

const projectReadme = `# %s

Generated with Ossein.

## Start

` + "```bash" + `
go mod tidy
go run ./cmd/server
` + "```" + `
`

const projectMain = `package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	ossein "github.com/LoonY20/ossein"
	appconfig "APP_MODULE/internal/config"
	httpapp "APP_MODULE/internal/http"
)

func main() {
	config, err := appconfig.Load()
	if err != nil {
		log.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil)).With("app", config.App.Name)
	app := ossein.New(ossein.WithLogger(logger))
	httpapp.RegisterRoutes(app)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "routes":
			writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(writer, "METHOD\tPATTERN\tNAME")
			for _, route := range app.SortedRoutes() {
				fmt.Fprintf(writer, "%s\t%s\t%s\n", route.Method, route.Pattern, route.Name)
			}
			_ = writer.Flush()
			return
		case "wire":
			if err := ossein.WriteWiringFile(app, "internal/wiring/wiring_gen.go", "wiring"); err != nil {
				log.Fatal(err)
			}
			fmt.Println("Created internal/wiring/wiring_gen.go")
			return
		default:
			log.Fatalf("unknown application command %q", os.Args[1])
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunContext(ctx, config.HTTP.Address); err != nil {
		log.Fatal(err)
	}
}
`

const projectConfig = `package config

import ossein "github.com/LoonY20/ossein"

type Config struct {
	App struct {
		Name string ` + "`env:\"APP_NAME\" default:\"Ossein App\"`" + `
	}
	HTTP struct {
		Address string ` + "`env:\"HTTP_ADDRESS\" default:\":8080\"`" + `
	}
}

func Load() (Config, error) {
	if err := ossein.LoadEnvFileIfExists(".env"); err != nil {
		return Config{}, err
	}
	return ossein.LoadConfig[Config]()
}
`

const projectRoutes = `package http

import ossein "github.com/LoonY20/ossein"

func RegisterRoutes(app *ossein.App) {
	app.Get("/health", Health).Named("health")
}
`

const projectHealth = `package http

import (
	"net/http"

	ossein "github.com/LoonY20/ossein"
)

func Health(ctx *ossein.Context) error {
	return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
`

const projectHealthTest = `package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ossein "github.com/LoonY20/ossein"
)

func TestHealth(t *testing.T) {
	app := ossein.New()
	RegisterRoutes(app)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}
`

const controllerTemplate = `package controllers

import (
	"net/http"

	ossein "github.com/LoonY20/ossein"
)

type %sController struct{}

func (c *%sController) Index(ctx *ossein.Context) error {
	return ctx.JSON(http.StatusOK, []any{})
}
`

const middlewareTemplate = `package middleware

import (
	"net/http"

	ossein "github.com/LoonY20/ossein"
)

func %s(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

var _ ossein.Middleware = %s
`

const requestTemplate = `package requests

import ossein "github.com/LoonY20/ossein"

type %s struct {
	// Add JSON fields here.
}

func (request *%s) Validate() error {
	validation := ossein.NewValidationError()
	return validation.OrNil()
}
`

// changeToModuleRoot moves to the nearest ancestor directory holding a go.mod.
//
// It is a no-op when the working directory is already the root, which is the usual
// case; it exists for the one that is not, a //go:generate directive running from
// the package that holds the generated file.
func changeToModuleRoot() error {
	root, err := findModuleRoot()
	if err != nil {
		return err
	}
	return os.Chdir(root)
}

// findModuleRoot walks up from the working directory looking for go.mod.
func findModuleRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("no go.mod found in this directory or any parent")
		}
		directory = parent
	}
}
