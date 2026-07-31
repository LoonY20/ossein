package ossein

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestContextAddsRequestIDAndLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	app := New(
		WithLogger(logger),
		WithRequestIDGenerator(func() string { return "generated-request-id" }),
	)

	app.Get("/", func(ctx *Context) error {
		if got := ctx.RequestID(); got != "generated-request-id" {
			t.Fatalf("expected generated request ID, got %q", got)
		}
		ctx.Logger().Info("handled request")
		return ctx.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header().Get("X-Request-ID"); got != "generated-request-id" {
		t.Fatalf("expected response request ID header, got %q", got)
	}

	output := logs.String()
	for _, expected := range []string{"handled request", "request_id=generated-request-id", "method=GET", "path=/"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected log output to contain %q, got %q", expected, output)
		}
	}
}

func TestContextWithLoggerIsFoundByLoggerFromContext(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ctx := ContextWithLogger(context.Background(), logger)

	found := LoggerFromContext(ctx)
	if found != logger {
		t.Fatalf("LoggerFromContext returned %p, want the logger that was stored (%p)", found, logger)
	}

	// The point of storing it is that background code logs through the
	// application's handler rather than through slog.Default.
	found.Info("worker started")
	if !strings.Contains(logs.String(), "worker started") {
		t.Fatalf("log output = %q, want the message written through the stored handler", logs.String())
	}
}

// TestContextWithLoggerIgnoresNilLogger pins the documented behavior: a nil
// logger returns the same context, rather than wrapping it in a node holding a
// typed nil that every reader would then have to defend against.
func TestContextWithLoggerIgnoresNilLogger(t *testing.T) {
	type marker struct{}

	parent := context.WithValue(context.Background(), marker{}, "kept")
	ctx := ContextWithLogger(parent, nil)

	if ctx != parent {
		t.Fatal("expected the parent context itself, not a wrapper around it")
	}
	if LoggerFromContext(ctx) != slog.Default() {
		t.Fatal("expected the slog.Default fallback for a context with no logger")
	}
	LoggerFromContext(ctx).Info("still usable")
}

func TestContextWithLoggerAcceptsNilContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	//lint:ignore SA1012 the nil context is the case under test.
	ctx := ContextWithLogger(nil, logger) //nolint:staticcheck
	if ctx == nil {
		t.Fatal("ContextWithLogger returned a nil context")
	}
	if LoggerFromContext(ctx) != logger {
		t.Fatal("logger was not stored on the substituted background context")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("ctx.Err() = %v, want nil for a background context", err)
	}
}

func TestRequestContextPreservesIncomingRequestID(t *testing.T) {
	app := New(WithRequestIDGenerator(func() string { return "generated" }))
	app.HandleHTTPFunc(http.MethodGet, "/native", func(w http.ResponseWriter, r *http.Request) {
		if got := RequestIDFromContext(r.Context()); got != "incoming" {
			t.Fatalf("expected incoming request ID, got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/native", nil)
	request.Header.Set("X-Request-ID", "incoming")
	response := httptest.NewRecorder()

	app.ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != "incoming" {
		t.Fatalf("expected incoming request ID response header, got %q", got)
	}
}
