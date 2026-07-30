package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSnakeCaseHandlesAcronyms(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"User", "user"},
		{"UserProfile", "user_profile"},
		{"HTTPServer", "http_server"},
		{"APIKey", "api_key"},
		{"UserAPIToken", "user_api_token"},
	}
	for _, item := range cases {
		if got := snakeCase(item.input); got != item.expected {
			t.Errorf("snakeCase(%q) = %q, expected %q", item.input, got, item.expected)
		}
	}
}

func TestNewProject(t *testing.T) {
	withinTempDir(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"new", "demo"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("Run() = %d, stderr = %s", code, stderr.String())
	}

	mainFile, err := os.ReadFile(filepath.Join("demo", "cmd", "server", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainFile), `"demo/internal/http"`) {
		t.Fatalf("starter module was not substituted:\n%s", mainFile)
	}
	configFile, err := os.ReadFile(filepath.Join("demo", "internal", "config", "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configFile), "LoadEnvFileIfExists") {
		t.Fatalf("starter config does not load .env:\n%s", configFile)
	}
}

func TestGenerators(t *testing.T) {
	withinTempDir(t)
	var stdout, stderr bytes.Buffer
	tests := []struct {
		command string
		name    string
		path    string
		content string
	}{
		{"make:controller", "user-profile", "internal/http/controllers/user_profile.go", "type UserProfileController struct"},
		{"make:middleware", "auth", "internal/http/middleware/auth.go", "func Auth("},
		{"make:request", "create-user", "internal/http/requests/create_user.go", "type CreateUser struct"},
	}
	for _, test := range tests {
		stdout.Reset()
		stderr.Reset()
		code := Run([]string{test.command, test.name}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("%s Run() = %d, stderr = %s", test.command, code, stderr.String())
		}
		content, err := os.ReadFile(filepath.FromSlash(test.path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), test.content) {
			t.Fatalf("unexpected %s output:\n%s", test.command, content)
		}
	}

	if code := Run([]string{"make:controller", "user-profile"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("duplicate generator Run() = %d", code)
	}
}

func TestGeneratedProjectBuildsAndListsRoutes(t *testing.T) {
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot, err := filepath.Abs(filepath.Join(packageDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	withinTempDir(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"new", "demo"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("new Run() = %d, stderr = %s", code, stderr.String())
	}

	projectDir, err := filepath.Abs("demo")
	if err != nil {
		t.Fatal(err)
	}
	runGoTool(t, projectDir, "mod", "edit", "-require=github.com/LoonY20/ossein@v0.0.0")
	runGoTool(t, projectDir, "mod", "edit", "-replace=github.com/LoonY20/ossein="+moduleRoot)
	runGoTool(t, projectDir, "test", "./...")

	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"routes"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("routes Run() = %d, stderr = %s", code, stderr.String())
	}
	for _, expected := range []string{"METHOD", "/health", "health"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("route output does not contain %q:\n%s", expected, stdout.String())
		}
	}

	command := exec.Command("go", "run", "./cmd/server", "migrate")
	command.Dir = projectDir
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("unwired migration command unexpectedly started the application")
	}
	if !strings.Contains(string(output), `unknown application command "migrate"`) {
		t.Fatalf("unexpected migration command failure:\n%s", output)
	}
}

func TestGeneratedProjectWiresServices(t *testing.T) {
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot, err := filepath.Abs(filepath.Join(packageDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	withinTempDir(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"new", "demo"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("new Run() = %d, stderr = %s", code, stderr.String())
	}

	projectDir, err := filepath.Abs("demo")
	if err != nil {
		t.Fatal(err)
	}
	runGoTool(t, projectDir, "mod", "edit", "-require=github.com/LoonY20/ossein@v0.0.0")
	runGoTool(t, projectDir, "mod", "edit", "-replace=github.com/LoonY20/ossein="+moduleRoot)

	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"wire"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("wire Run() = %d, stderr = %s", code, stderr.String())
	}

	generated, err := os.ReadFile(filepath.Join("internal", "wiring", "wiring_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "package wiring") {
		t.Fatalf("generated wiring = %s", generated)
	}

	// The project must still compile with the generated file in the tree.
	runGoTool(t, projectDir, "build", "./...")
}

func TestCommandValidationAndMetadata(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"wat"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("Run() = %d", code)
	}
	stdout.Reset()
	if code := Run(nil, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("help Run() = %d, stdout = %s", code, stdout.String())
	}
	stdout.Reset()
	if code := Run([]string{"version"}, strings.NewReader(""), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), Version) {
		t.Fatalf("version Run() = %d, stdout = %s", code, stdout.String())
	}

	for _, args := range [][]string{
		{"new"},
		{"new", ".hidden"},
		{"make:request"},
		{"make:request", "---"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := Run(args, strings.NewReader(""), &stdout, &stderr); code == 0 {
			t.Fatalf("Run(%v) unexpectedly succeeded", args)
		}
	}
}

func TestApplicationCommandsAreForwarded(t *testing.T) {
	var captured []string
	executeGoCommand := func(
		args []string,
		_ io.Reader,
		_, _ io.Writer,
	) int {
		captured = append([]string(nil), args...)
		return 17
	}

	tests := [][]string{
		{"routes"},
		{"migrate", "--limit", "2"},
		{"migrate:rollback", "--steps=3"},
		{"migrate:status"},
		{"db:seed"},
		{"wire"},
	}
	for _, args := range tests {
		captured = nil
		code := run(
			context.Background(),
			args,
			strings.NewReader(""),
			io.Discard,
			io.Discard,
			executeGoCommand,
			func(context.Context, io.Reader, io.Writer, io.Writer) int {
				t.Fatal("unexpected dev execution")
				return 0
			},
		)
		if code != 17 {
			t.Fatalf("Run(%v) = %d", args, code)
		}
		expected := append([]string{"run", "./cmd/server"}, args...)
		if !reflect.DeepEqual(captured, expected) {
			t.Fatalf("Run(%v) forwarded %#v, want %#v", args, captured, expected)
		}
	}

	devCalled := false
	code := run(
		context.Background(),
		[]string{"dev"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
		executeGoCommand,
		func(context.Context, io.Reader, io.Writer, io.Writer) int {
			devCalled = true
			return 23
		},
	)
	if code != 23 || !devCalled {
		t.Fatalf("dev Run() = %d, called=%t", code, devCalled)
	}
}

func TestNewRejectsExistingProject(t *testing.T) {
	withinTempDir(t)
	if err := os.Mkdir("demo", 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"new", "demo"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("Run() = %d", code)
	}
}

func TestRunGoCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runGoCommand([]string{"version"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("go version exit = %d, stderr = %s", code, stderr.String())
	}

	t.Setenv("PATH", "")
	stderr.Reset()
	if code := runGoCommand([]string{"version"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("missing go exit = %d", code)
	}
}

func withinTempDir(t *testing.T) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previous)
	})
}

func runGoTool(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("go", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
