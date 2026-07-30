package ossein

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeRejectsNilServer(t *testing.T) {
	app := New()
	if err := app.Serve(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil server")
	}
}

func TestServeListenerRejectsNilListener(t *testing.T) {
	app := New()
	if err := app.ServeListener(context.Background(), &http.Server{}, nil); err == nil {
		t.Fatal("expected an error for a nil listener")
	}
}

func TestServeEntryPointsRejectNilServer(t *testing.T) {
	app := New()
	if err := app.ServeTLS(context.Background(), nil, "cert.pem", "key.pem"); err == nil {
		t.Fatal("ServeTLS: expected an error for a nil server")
	}
	if err := app.ServeListener(context.Background(), nil, newLocalListener(t)); err == nil {
		t.Fatal("ServeListener: expected an error for a nil server")
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

	listener := newLocalListener(t)
	server := &http.Server{}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- app.ServeListener(ctx, server, listener)
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
			t.Fatalf("ServeListener() error = %v", err)
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
		t.Fatal("expected the application handler to be installed")
	}
}

// TestServeKeepsCallerConfiguration proves the caller's handler is the one that
// serves traffic, rather than only checking that the field was left alone.
func TestServeKeepsCallerConfiguration(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusTeapot) })

	served := make(chan struct{})
	callerHandler := http.NewServeMux()
	callerHandler.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		close(served)
		w.WriteHeader(http.StatusNoContent)
	})

	listener := newLocalListener(t)
	server := &http.Server{Handler: callerHandler, ReadTimeout: 42 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.ServeListener(ctx, server, listener)
	}()

	response, err := http.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the caller's handler never served the request")
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want the caller handler's 204", response.StatusCode)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ServeListener() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
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

	if err := app.Serve(context.Background(), &http.Server{Addr: "127.0.0.1:0"}); !errors.Is(err, expected) {
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

// TestServeRejectsCancelledContext keeps an already-cancelled context from
// looking like a clean run: hooks must not fire and the error must be reported.
func TestServeRejectsCancelledContext(t *testing.T) {
	app := New()
	startCalled := false
	app.OnStart(func(context.Context) error {
		startCalled = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := app.Serve(ctx, &http.Server{Addr: "127.0.0.1:0"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve() error = %v, want context.Canceled", err)
	}
	if startCalled {
		t.Fatal("start hooks must not run for an already-cancelled context")
	}
}

// TestServeReportsAlreadyClosedServer covers a server that cannot serve because
// it was already shut down. Swallowing ErrServerClosed here would report a
// successful run that never accepted a connection.
func TestServeReportsAlreadyClosedServer(t *testing.T) {
	app := New(WithShutdownTimeout(time.Second))
	server := &http.Server{Addr: "127.0.0.1:0"}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := app.Serve(context.Background(), server)
	if !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve() error = %v, want http.ErrServerClosed", err)
	}
}

// TestServeWrapsGracefulShutdownTimeout keeps a shutdown that exceeds its
// deadline identifiable, instead of surfacing a bare context error.
func TestServeWrapsGracefulShutdownTimeout(t *testing.T) {
	app := New(WithShutdownTimeout(50 * time.Millisecond))
	entered := make(chan struct{})
	release := make(chan struct{})
	app.Get("/slow", func(c *Context) error {
		close(entered)
		<-release
		return c.NoContent(http.StatusNoContent)
	})
	defer close(release)

	listener := newLocalListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- app.ServeListener(ctx, &http.Server{}, listener)
	}()

	go func() {
		response, err := http.Get("http://" + listener.Addr().String() + "/slow")
		if err == nil {
			_ = response.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler was never entered")
	}
	cancel()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected a shutdown timeout error")
		}
		if !strings.Contains(err.Error(), "graceful shutdown") {
			t.Fatalf("error = %v, want it to name the graceful shutdown", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
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

// TestRunReportsBuildErrorsInsteadOfPanicking keeps conflicting patterns
// reportable through the server entry points.
func TestRunReportsBuildErrorsInsteadOfPanicking(t *testing.T) {
	app := New()
	app.Get("/duplicate", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	app.Get("/duplicate", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	err := app.Run("127.0.0.1:0")
	if err == nil {
		t.Fatal("expected a route registration error")
	}
	if !strings.Contains(err.Error(), "register route") {
		t.Fatalf("error = %v, want a route registration error", err)
	}
}

// newLocalListener binds an ephemeral loopback port. Handing the listener to
// ServeListener avoids guessing a free port, which would be a race.
func newLocalListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}
