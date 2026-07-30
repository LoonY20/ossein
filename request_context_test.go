package ossein

import (
	"bytes"
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
