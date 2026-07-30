package ossein

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplicationOptionsAndMethodHelpers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	app := New(
		WithLogger(logger),
		WithRequestIDHeader("X-Correlation-ID"),
		WithShutdownTimeout(time.Second),
	)
	if app.Logger() != logger {
		t.Fatal("expected configured logger")
	}
	if app.shutdownTimeout != time.Second {
		t.Fatalf("shutdown timeout = %s", app.shutdownTimeout)
	}

	methods := []struct {
		method   string
		register func(string, HandlerFunc) *Route
	}{
		{http.MethodPut, app.Put},
		{http.MethodPatch, app.Patch},
		{http.MethodDelete, app.Delete},
	}
	for _, item := range methods {
		item.register("/"+strings.ToLower(item.method), func(ctx *Context) error {
			return ctx.NoContent(http.StatusNoContent)
		})
	}
	for _, item := range methods {
		path := "/" + strings.ToLower(item.method)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(item.method, path, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", item.method, response.Code)
		}
		if response.Header().Get("X-Correlation-ID") == "" {
			t.Fatalf("%s response is missing custom request ID header", item.method)
		}
	}
}

func TestSetErrorHandlerNilRestoresDefault(t *testing.T) {
	app := New()
	app.SetErrorHandler(func(ctx *Context, _ error) {
		_ = ctx.NoContent(http.StatusTeapot)
	})
	app.SetErrorHandler(nil)
	app.Get("/", func(*Context) error {
		return Unauthorized("unauthorized", "Authentication required")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRunContextStartsAndStopsLifecycle(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	started := make(chan struct{})
	stopped := make(chan struct{})
	app.OnStart(func(context.Context) error {
		close(started)
		return nil
	})
	app.OnStop(func(context.Context) error {
		close(stopped)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- app.RunContext(ctx, "127.0.0.1:0")
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("application did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunContext() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("stop hook was not called")
	}
}

func TestRunStopsLifecycleAfterListenError(t *testing.T) {
	app := New()
	stopped := false
	app.OnStop(func(context.Context) error {
		stopped = true
		return nil
	})

	if err := app.Run("invalid-address"); err == nil {
		t.Fatal("expected listen error")
	}
	if !stopped {
		t.Fatal("expected stop hook after listen error")
	}
}

func TestLifecycleStartErrorIncludesHookAndSkipsLaterHooks(t *testing.T) {
	app := New()
	expected := errors.New("unavailable")
	laterCalled := false
	app.OnStart(
		nil,
		func(context.Context) error { return expected },
		func(context.Context) error {
			laterCalled = true
			return nil
		},
	)
	err := app.Start(nil)
	if !errors.Is(err, expected) || !strings.Contains(err.Error(), "hook 2") {
		t.Fatalf("unexpected start error: %v", err)
	}
	if laterCalled {
		t.Fatal("expected startup to stop after first error")
	}
}

func TestConcurrentSingletonResolution(t *testing.T) {
	app := New()
	var calls atomic.Int32
	if err := app.Provide(func() *userService {
		calls.Add(1)
		time.Sleep(time.Millisecond)
		return &userService{}
	}, Singleton()); err != nil {
		t.Fatal(err)
	}

	const goroutines = 32
	values := make([]*userService, goroutines)
	errs := make([]error, goroutines)
	var group sync.WaitGroup
	for i := range values {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			values[index], errs[index] = Resolve[*userService](app)
		}(i)
	}
	group.Wait()

	for i := range values {
		if errs[i] != nil {
			t.Fatalf("resolve %d: %v", i, errs[i])
		}
		if values[i] != values[0] {
			t.Fatalf("resolution %d returned a different singleton", i)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("constructor calls = %d", calls.Load())
	}
}
