package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultPollInterval    = 300 * time.Millisecond
	defaultShutdownTimeout = 5 * time.Second
)

type devOptions struct {
	directory       string
	pollInterval    time.Duration
	shutdownTimeout time.Duration

	// afterBuild is a test hook invoked after a successful build, before the
	// binary starts. It simulates file changes racing the build.
	afterBuild func()
}

type fileState struct {
	size    int64
	modTime time.Time
}

type synchronizedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w synchronizedWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(content)
}

func runDev(
	ctx context.Context,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	return runDevWithOptions(ctx, stdin, stdout, stderr, devOptions{
		directory:       ".",
		pollInterval:    defaultPollInterval,
		shutdownTimeout: defaultShutdownTimeout,
	})
}

func runDevWithOptions(
	ctx context.Context,
	stdin io.Reader,
	stdout, stderr io.Writer,
	options devOptions,
) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.directory == "" {
		options.directory = "."
	}
	if options.pollInterval <= 0 {
		options.pollInterval = defaultPollInterval
	}
	if options.shutdownTimeout <= 0 {
		options.shutdownTimeout = defaultShutdownTimeout
	}
	var outputMu sync.Mutex
	stdout = synchronizedWriter{mu: &outputMu, writer: stdout}
	stderr = synchronizedWriter{mu: &outputMu, writer: stderr}

	tempDirectory, err := os.MkdirTemp("", "ossein-dev-*")
	if err != nil {
		fmt.Fprintln(stderr, "ossein dev:", err)
		return 1
	}
	defer os.RemoveAll(tempDirectory)
	binary := filepath.Join(tempDirectory, "server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	for {
		if ctx.Err() != nil {
			return 0
		}

		// Snapshot before the build so edits made while the compiler runs
		// still differ from the baseline and trigger a restart.
		state, err := watchState(options.directory)
		if err != nil {
			fmt.Fprintln(stderr, "ossein dev:", err)
			return 1
		}

		fmt.Fprintln(stdout, "[ossein] building")
		build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/server")
		build.Dir = options.directory
		build.Stdout = stdout
		build.Stderr = stderr
		if err := build.Run(); err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintln(stderr, "[ossein] build failed; waiting for changes")
			if _, err := waitForChange(ctx, options, state); err != nil {
				return devWaitError(stderr, err)
			}
			continue
		}
		if options.afterBuild != nil {
			options.afterBuild()
		}

		command := exec.Command(binary)
		command.Dir = options.directory
		command.Stdin = stdin
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Start(); err != nil {
			fmt.Fprintln(stderr, "ossein dev:", err)
			return 1
		}
		fmt.Fprintln(stdout, "[ossein] running")

		exited := make(chan error, 1)
		go func() {
			exited <- command.Wait()
		}()
		ticker := time.NewTicker(options.pollInterval)
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				ticker.Stop()
				stopProcess(command, exited, options.shutdownTimeout)
				return 0
			case processErr := <-exited:
				ticker.Stop()
				if processErr != nil {
					fmt.Fprintf(stderr, "[ossein] process exited: %v\n", processErr)
				} else {
					fmt.Fprintln(stdout, "[ossein] process exited")
				}
				fmt.Fprintln(stdout, "[ossein] waiting for changes")
				if _, err := waitForChange(ctx, options, state); err != nil {
					return devWaitError(stderr, err)
				}
				restart = true
			case <-ticker.C:
				current, snapshotErr := watchState(options.directory)
				if snapshotErr != nil {
					ticker.Stop()
					stopProcess(command, exited, options.shutdownTimeout)
					fmt.Fprintln(stderr, "ossein dev:", snapshotErr)
					return 1
				}
				if !sameState(state, current) {
					ticker.Stop()
					fmt.Fprintln(stdout, "[ossein] changes detected; restarting")
					stopProcess(command, exited, options.shutdownTimeout)
					restart = true
				}
			}
		}
	}
}

func waitForChange(
	ctx context.Context,
	options devOptions,
	previous map[string]fileState,
) (map[string]fileState, error) {
	ticker := time.NewTicker(options.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			current, err := watchState(options.directory)
			if err != nil {
				return nil, err
			}
			if !sameState(previous, current) {
				return current, nil
			}
		}
	}
}

func devWaitError(stderr io.Writer, err error) int {
	if errors.Is(err, context.Canceled) {
		return 0
	}
	fmt.Fprintln(stderr, "ossein dev:", err)
	return 1
}

func stopProcess(command *exec.Cmd, exited <-chan error, timeout time.Duration) {
	if command == nil || command.Process == nil {
		return
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exited:
	case <-timer.C:
		_ = command.Process.Kill()
		<-exited
	}
}

func watchState(directory string) (map[string]fileState, error) {
	state := make(map[string]fileState)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != directory && ignoredDevDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !watchedDevFile(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		state[filepath.ToSlash(relative)] = fileState{
			size: info.Size(), modTime: info.ModTime(),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", directory, err)
	}
	return state, nil
}

func ignoredDevDirectory(name string) bool {
	switch name {
	case ".git", ".idea", ".vscode", "bin", "node_modules", "tmp", "vendor":
		return true
	default:
		return strings.HasPrefix(name, ".ossein-")
	}
}

func watchedDevFile(name string) bool {
	switch name {
	case ".env", "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".json", ".sql", ".toml", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func sameState(left, right map[string]fileState) bool {
	if len(left) != len(right) {
		return false
	}
	for path, state := range left {
		if other, exists := right[path]; !exists || other != state {
			return false
		}
	}
	return true
}
