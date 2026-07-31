package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

func TestRecoverRendersAStructuredError(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}

	var envelope ossein.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body %q)", err, response.Body.String())
	}
	if envelope.Error.Code != "internal_error" {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
	if strings.Contains(response.Body.String(), "kaboom") {
		t.Fatalf("body = %q, want no internal detail leaked", response.Body.String())
	}
}

// TestRecoverUsesTheApplicationErrorContract keeps a custom ErrorHandler in charge
// of the shape, so a recovered panic matches every other error the API reports.
func TestRecoverUsesTheApplicationErrorContract(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(c *ossein.Context, err error) {
		_ = ossein.JSON(c.Response, http.StatusServiceUnavailable, map[string]any{
			"ok": false,
		})
	})
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the custom handler's 503", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"ok":false`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestRecoverLogsThePanicAndStack(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	recorded := logs.String()
	if !strings.Contains(recorded, "kaboom") {
		t.Fatalf("log = %q, want the panic value", recorded)
	}
	if !strings.Contains(recorded, "stack") {
		t.Fatalf("log = %q, want a stack trace", recorded)
	}
	if !strings.Contains(recorded, "request_id") {
		t.Fatalf("log = %q, want the request-scoped logger's fields", recorded)
	}
}

// TestRecoverLeavesACommittedResponseAlone keeps a panic mid-stream from appending
// an error document to bytes the client already has.
//
// The application's error handler is replaced with one that writes
// unconditionally, because the default handler has its own committed-response
// guard: without that, this test would pass whether or not Recover checks.
func TestRecoverLeavesACommittedResponseAlone(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(c *ossein.Context, err error) {
		_, _ = c.Response.Write([]byte("APPENDED"))
	})
	app.Use(middleware.Recover())
	app.Get("/partial", func(c *ossein.Context) error {
		if _, err := c.Response.Write([]byte("partial output")); err != nil {
			return err
		}
		panic("too late")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/partial", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", response.Code)
	}
	if body := response.Body.String(); body != "partial output" {
		t.Fatalf("body = %q, want no appended error document", body)
	}
}

// TestRecoverRepanicsOnAbortHandler keeps the standard library's own signal for
// abandoning a response working.
func TestRecoverRepanicsOnAbortHandler(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Recover())
	app.Get("/abort", func(*ossein.Context) error {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		recovered := recover()
		if recovered != http.ErrAbortHandler {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler to pass through", recovered)
		}
	}()
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}

func TestRecoverPassesNormalRequestsThrough(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Recover())
	app.Get("/ok", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ok", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestRecoverHandlesANilPanicValue covers panic(nil), which Go 1.21 turns into a
// runtime error rather than a silent nil recover.
func TestRecoverHandlesANilPanicValue(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Recover())
	app.Get("/nil", func(*ossein.Context) error {
		panic(nil)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/nil", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}
