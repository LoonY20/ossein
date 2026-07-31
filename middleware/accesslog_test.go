package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

func logCapture() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buffer
}

func TestAccessLogRecordsStatusAndSize(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	app.Get("/items", func(c *ossein.Context) error {
		return c.JSON(http.StatusCreated, map[string]string{"id": "1"})
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))

	recorded := logs.String()
	for _, want := range []string{"status=201", "method=GET", "path=/items", "request_id="} {
		if !strings.Contains(recorded, want) {
			t.Fatalf("log = %q, want %q", recorded, want)
		}
	}
	if !strings.Contains(recorded, "bytes=") {
		t.Fatalf("log = %q, want the response size", recorded)
	}
	if !strings.Contains(recorded, "duration_ms=") {
		t.Fatalf("log = %q, want the duration", recorded)
	}
}

// TestAccessLogRecordsTheFallbackStatus keeps a response with no explicit
// WriteHeader from being logged as status zero.
func TestAccessLogRecordsTheFallbackStatus(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	app.Get("/implicit", func(c *ossein.Context) error {
		_, err := c.Response.Write([]byte("body"))
		return err
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/implicit", nil))

	if !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("log = %q, want the implicit 200", logs.String())
	}
}

// TestAccessLogRecordsAHandlerThatWroteNothing keeps a handler that returns without
// writing from being logged as an impossible zero status.
func TestAccessLogRecordsAHandlerThatWroteNothing(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	app.Get("/silent", func(*ossein.Context) error { return nil })

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/silent", nil))

	recorded := logs.String()
	if strings.Contains(recorded, "status=0") {
		t.Fatalf("log = %q, want the 200 net/http will send, not zero", recorded)
	}
	if !strings.Contains(recorded, "status=200") {
		t.Fatalf("log = %q, want status=200", recorded)
	}
}

// TestAccessLogLogsErrorsAtErrorLevel separates server failures from ordinary
// traffic, so a log level filter is useful.
func TestAccessLogLogsErrorsAtErrorLevel(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	app.Get("/broken", func(*ossein.Context) error {
		return ossein.NewHTTPError(http.StatusInternalServerError, "broken", "broken")
	})
	app.Get("/missing", func(*ossein.Context) error {
		return ossein.NotFound("missing", "missing")
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/broken", nil))
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("log = %q, want a 500 logged at error level", logs.String())
	}

	logs.Reset()
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
	recorded := logs.String()
	if strings.Contains(recorded, "level=ERROR") {
		t.Fatalf("log = %q, want a 404 below error level", recorded)
	}
	if !strings.Contains(recorded, "level=WARN") {
		t.Fatalf("log = %q, want a client error at warn level", recorded)
	}
}

// TestAccessLogSkipsPaths keeps health checks out of the log.
func TestAccessLogSkipsPaths(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog(middleware.SkipPaths("/healthz")))
	app.Get("/healthz", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	app.Get("/items", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want nothing for a skipped path", logs.String())
	}

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))
	if !strings.Contains(logs.String(), "/items") {
		t.Fatalf("log = %q, want the unskipped path recorded", logs.String())
	}
}

// TestAccessLogOutsideAnApplicationHasNoStatus documents the boundary: the status
// and size come from tracking Ossein installs per request, so a middleware mounted
// outside an application has nothing to report.
func TestAccessLogOutsideAnApplicationHasNoStatus(t *testing.T) {
	logger, logs := logCapture()
	previous := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(previous)

	handler := middleware.AccessLog()(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the handler's 418", response.Code)
	}
	if !strings.Contains(logs.String(), "status=0") {
		t.Fatalf("log = %q, want an unknown status reported as zero", logs.String())
	}
}

// TestAccessLogRunsAfterTheResponse confirms the recorded status is the final one,
// which is the whole reason the framework tracks it.
func TestAccessLogRunsAfterTheResponse(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	// An unmatched route is answered by the framework's own fallback, after the
	// middleware has already been entered.
	app.Get("/known", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/unknown", nil))

	if !strings.Contains(logs.String(), "status=404") {
		t.Fatalf("log = %q, want the fallback's 404", logs.String())
	}
}
