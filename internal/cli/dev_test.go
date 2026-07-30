package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWatchStateTracksApplicationFiles(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "go.mod"), "module example\n\ngo 1.23.0\n")
	writeTestFile(t, filepath.Join(directory, ".env"), "HTTP_ADDRESS=:8080\n")
	writeTestFile(t, filepath.Join(directory, "main.go"), "package main\n")
	writeTestFile(t, filepath.Join(directory, "README.md"), "ignored")
	writeTestFile(t, filepath.Join(directory, ".git", "config"), "ignored")
	writeTestFile(t, filepath.Join(directory, "tmp", "generated.go"), "ignored")

	state, err := watchState(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"go.mod", ".env", "main.go"} {
		if _, exists := state[expected]; !exists {
			t.Fatalf("watch state does not contain %q: %#v", expected, state)
		}
	}
	for _, ignored := range []string{"README.md", ".git/config", "tmp/generated.go"} {
		if _, exists := state[ignored]; exists {
			t.Fatalf("watch state unexpectedly contains %q", ignored)
		}
	}

	unchanged, err := watchState(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !sameState(state, unchanged) {
		t.Fatal("identical snapshots differ")
	}
	writeTestFile(t, filepath.Join(directory, "main.go"), "package main\n// changed\n")
	changed, err := watchState(directory)
	if err != nil {
		t.Fatal(err)
	}
	if sameState(state, changed) {
		t.Fatal("changed snapshots are equal")
	}
	if sameState(state, map[string]fileState{}) {
		t.Fatal("snapshots with different lengths are equal")
	}
}

func TestDevRebuildsAndStopsApplication(t *testing.T) {
	project := t.TempDir()
	logPath := filepath.Join(project, "starts.log")
	t.Setenv("OSSEIN_DEV_TEST_LOG", logPath)
	writeTestFile(t, filepath.Join(project, "go.mod"), "module devtest\n\ngo 1.23.0\n")
	sourcePath := filepath.Join(project, "cmd", "server", "main.go")
	source := longRunningDevSource
	writeTestFile(t, sourcePath, source)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout, stderr safeBuffer
	done := make(chan int, 1)
	go func() {
		done <- runDevWithOptions(
			ctx,
			strings.NewReader(""),
			&stdout,
			&stderr,
			devOptions{
				directory:       project,
				pollInterval:    25 * time.Millisecond,
				shutdownTimeout: time.Second,
			},
		)
	}()

	waitForStartCount(t, logPath, 1)
	writeTestFile(t, sourcePath, source+"\n// trigger rebuild\n")
	waitForStartCount(t, logPath, 2)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("runDevWithOptions() = %d, stderr = %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dev supervisor did not stop")
	}
	if !strings.Contains(stdout.String(), "changes detected; restarting") {
		content, _ := os.ReadFile(logPath)
		t.Fatalf(
			"dev output does not report restart:\nstdout:\n%s\nstderr:\n%s\nstarts: %q",
			stdout.String(),
			stderr.String(),
			content,
		)
	}
}

func TestDevRecoversAfterBuildFailure(t *testing.T) {
	project := t.TempDir()
	logPath := filepath.Join(project, "starts.log")
	t.Setenv("OSSEIN_DEV_TEST_LOG", logPath)
	writeTestFile(t, filepath.Join(project, "go.mod"), "module devtest\n\ngo 1.23.0\n")
	sourcePath := filepath.Join(project, "cmd", "server", "main.go")
	writeTestFile(t, sourcePath, "package main\nfunc broken")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout, stderr safeBuffer
	done := make(chan int, 1)
	go func() {
		done <- runDevWithOptions(
			ctx,
			strings.NewReader(""),
			&stdout,
			&stderr,
			testDevOptions(project),
		)
	}()

	waitForText(t, &stderr, "build failed")
	writeTestFile(t, sourcePath, longRunningDevSource)
	waitForStartCount(t, logPath, 1)
	cancel()
	waitForDevDone(t, done, &stderr)
}

func TestDevWaitsAfterCleanProcessExit(t *testing.T) {
	project := t.TempDir()
	logPath := filepath.Join(project, "starts.log")
	t.Setenv("OSSEIN_DEV_TEST_LOG", logPath)
	writeTestFile(t, filepath.Join(project, "go.mod"), "module devtest\n\ngo 1.23.0\n")
	sourcePath := filepath.Join(project, "cmd", "server", "main.go")
	writeTestFile(t, sourcePath, exitingDevSource)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout, stderr safeBuffer
	done := make(chan int, 1)
	go func() {
		done <- runDevWithOptions(
			ctx,
			strings.NewReader(""),
			&stdout,
			&stderr,
			testDevOptions(project),
		)
	}()

	waitForStartCount(t, logPath, 1)
	waitForText(t, &stdout, "waiting for changes")
	writeTestFile(t, sourcePath, longRunningDevSource)
	waitForStartCount(t, logPath, 2)
	cancel()
	waitForDevDone(t, done, &stderr)
}

func TestDevDetectsChangesMadeDuringBuild(t *testing.T) {
	project := t.TempDir()
	logPath := filepath.Join(project, "starts.log")
	t.Setenv("OSSEIN_DEV_TEST_LOG", logPath)
	writeTestFile(t, filepath.Join(project, "go.mod"), "module devtest\n\ngo 1.23.0\n")
	sourcePath := filepath.Join(project, "cmd", "server", "main.go")
	writeTestFile(t, sourcePath, longRunningDevSource)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout, stderr safeBuffer
	options := testDevOptions(project)
	edited := false
	options.afterBuild = func() {
		if edited {
			return
		}
		edited = true
		changed := longRunningDevSource + "\n// edited while the build was running\n"
		if err := os.WriteFile(sourcePath, []byte(changed), 0o600); err != nil {
			t.Error(err)
		}
	}
	done := make(chan int, 1)
	go func() {
		done <- runDevWithOptions(ctx, strings.NewReader(""), &stdout, &stderr, options)
	}()

	waitForStartCount(t, logPath, 2)
	cancel()
	waitForDevDone(t, done, &stderr)
}

func TestDevRejectsMissingDirectory(t *testing.T) {
	code := runDevWithOptions(
		context.Background(),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
		devOptions{directory: filepath.Join(t.TempDir(), "missing")},
	)
	if code != 1 {
		t.Fatalf("runDevWithOptions() = %d", code)
	}
}

func waitForStartCount(t *testing.T, path string, expected int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Fields(string(content))
			if len(lines) >= expected {
				return
			}
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	content, _ := os.ReadFile(path)
	t.Fatalf("start count did not reach %d: %q", expected, content)
}

func waitForText(t *testing.T, output *safeBuffer, expected string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), expected) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("output does not contain %q:\n%s", expected, output.String())
}

func waitForDevDone(t *testing.T, done <-chan int, stderr *safeBuffer) {
	t.Helper()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("dev supervisor = %d, stderr = %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dev supervisor did not stop")
	}
}

func testDevOptions(directory string) devOptions {
	return devOptions{
		directory:       directory,
		pollInterval:    25 * time.Millisecond,
		shutdownTimeout: time.Second,
	}
}

type safeBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *safeBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(content)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWatchedDevFile(t *testing.T) {
	for _, name := range []string{"main.go", ".env", "config.yaml", "schema.sql"} {
		if !watchedDevFile(name) {
			t.Errorf("%s should be watched", name)
		}
	}
	for _, name := range []string{"README.md", "server.exe", "coverage.out"} {
		if watchedDevFile(name) {
			t.Errorf("%s should not be watched", name)
		}
	}
	if !ignoredDevDirectory("vendor") || ignoredDevDirectory("internal") {
		t.Fatal("unexpected ignored directory result")
	}
}

const longRunningDevSource = `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	file, err := os.OpenFile(os.Getenv("OSSEIN_DEV_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(file, os.Getpid())
	_ = file.Close()
	for {
		time.Sleep(time.Hour)
	}
}
`

const exitingDevSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	file, err := os.OpenFile(os.Getenv("OSSEIN_DEV_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(file, os.Getpid())
	_ = file.Close()
}
`
