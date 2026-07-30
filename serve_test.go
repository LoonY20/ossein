package ossein

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestServeRejectsNilServer(t *testing.T) {
	app := New()
	if err := app.Serve(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil server")
	}
}

func TestServeFillsHandlerAndRunsLifecycle(t *testing.T) {
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

	server := &http.Server{Addr: "127.0.0.1:0"}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- app.Serve(ctx, server)
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
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("stop hook was not called")
	}
	if server.Handler == nil {
		t.Fatal("expected Serve to install the application handler")
	}
}

func TestServeKeepsCallerConfiguration(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	callerHandler := http.NewServeMux()
	server := &http.Server{
		Addr:        "127.0.0.1:0",
		Handler:     callerHandler,
		ReadTimeout: 42 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- app.Serve(ctx, server)
	}()

	// Give the server a moment to start before shutting it down.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
	}

	if server.Handler != http.Handler(callerHandler) {
		t.Fatal("expected Serve to keep the caller's handler")
	}
	if server.ReadTimeout != 42*time.Second {
		t.Fatalf("ReadTimeout = %v, want the caller's 42s", server.ReadTimeout)
	}
}

func TestServeRunsStopHooksAfterListenError(t *testing.T) {
	app := New()
	stopped := false
	app.OnStop(func(context.Context) error {
		stopped = true
		return nil
	})

	err := app.Serve(context.Background(), &http.Server{Addr: "invalid-address"})
	if err == nil {
		t.Fatal("expected a listen error")
	}
	if !stopped {
		t.Fatal("expected stop hooks to run after a listen error")
	}
}

func TestServeReturnsStartErrorWithoutServing(t *testing.T) {
	app := New()
	expected := errors.New("dependency unavailable")
	app.OnStart(func(context.Context) error { return expected })

	server := &http.Server{Addr: "127.0.0.1:0"}
	if err := app.Serve(context.Background(), server); !errors.Is(err, expected) {
		t.Fatalf("Serve() error = %v, want %v", err, expected)
	}
}

func TestServeAcceptsNilContext(t *testing.T) {
	app := New()
	// A nil context must not panic; the listen error is what ends the call.
	if err := app.Serve(nil, &http.Server{Addr: "invalid-address"}); err == nil {
		t.Fatal("expected a listen error")
	}
}

// TestDefaultServerAppliesSlowlorisTimeoutsOnly pins the defaults Run and
// RunContext build with. ReadHeaderTimeout and IdleTimeout bound connections
// that never send a complete request, while ReadTimeout and WriteTimeout stay
// unset on purpose: a WriteTimeout default would break server-sent events and
// long downloads, and a ReadTimeout default would break large uploads.
func TestDefaultServerAppliesSlowlorisTimeoutsOnly(t *testing.T) {
	app := New()
	server := app.newDefaultServer("127.0.0.1:8080")

	if server.Addr != "127.0.0.1:8080" {
		t.Fatalf("Addr = %q", server.Addr)
	}
	// The handler is installed by Serve, after Start, so that a route conflict
	// surfaces as a Start error rather than a Handler() panic.
	if server.Handler != nil {
		t.Fatal("expected the handler to be left for Serve to install")
	}
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("expected a non-zero ReadHeaderTimeout")
	}
	if server.IdleTimeout <= 0 {
		t.Fatal("expected a non-zero IdleTimeout")
	}
	if server.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, want 0 so large uploads keep working", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 so streaming keeps working", server.WriteTimeout)
	}
}
