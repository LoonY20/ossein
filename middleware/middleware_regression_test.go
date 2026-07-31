package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

// TestAccessLogRecordsAPanickingRequest covers the whole stack in the order the
// package documents, which is why that order puts AccessLog outermost: it must
// observe the status Recover produces, and a middleware only sees a status written
// below it.
func TestAccessLogRecordsAPanickingRequest(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(
		middleware.AccessLog(),
		middleware.Recover(),
		middleware.SecurityHeaders(),
	)
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	recorded := logs.String()
	if !strings.Contains(recorded, "request completed") {
		t.Fatalf("log = %q, want an access log line for the panicking request", recorded)
	}
	if !strings.Contains(recorded, "status=500") {
		t.Fatalf("log = %q, want the 500 recorded", recorded)
	}
}

// TestAccessLogInsideRecoverStillLogs pins the other ordering. The line is still
// emitted, because the log is deferred, but it reports the status before recovery
// since Recover writes the 500 only after AccessLog's frame has unwound.
func TestAccessLogInsideRecoverStillLogs(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(
		middleware.Recover(),
		middleware.AccessLog(),
	)
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if !strings.Contains(logs.String(), "request completed") {
		t.Fatalf("log = %q, want the request still logged", logs.String())
	}
}

// TestFullStackComposes checks the documented ordering on an ordinary request, so
// the three middlewares are exercised together at least once.
func TestFullStackComposes(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(
		middleware.AccessLog(),
		middleware.Recover(),
		middleware.SecurityHeaders(),
	)
	app.Get("/items", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "yes"})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q on the committed response", got)
	}
	if !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("log = %q", logs.String())
	}
}

// TestRecoverSurvivesAPanickingErrorHandler covers a double fault. Recover renders
// through the application's error handler, so a handler that panics is reached
// again from inside Recover's own deferred function, where nothing else can catch
// it.
func TestRecoverSurvivesAPanickingErrorHandler(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(*ossein.Context, error) {
		panic("the error handler is broken")
	})
	app.Use(middleware.Recover())
	// No panic in application code: an ordinary returned error is enough, because
	// the error handler is what fails.
	app.Get("/bad", func(*ossein.Context) error {
		return ossein.BadRequest("bad", "bad")
	})

	response := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("a panicking error handler escaped Recover: %v", recovered)
			}
		}()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/bad", nil))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete")
	}

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want a 500 fallback", response.Code)
	}
}

// TestRecoverPropagatesAbortFromTheErrorHandler keeps the standard library's signal
// for abandoning a response working when it comes from the second fault: the
// handler panics normally, and the error handler reporting it aborts.
func TestRecoverPropagatesAbortFromTheErrorHandler(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(*ossein.Context, error) {
		panic(http.ErrAbortHandler)
	})
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("the handler failed first")
	})

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", recovered)
		}
	}()
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
}

// TestRecoverDoesNotAppendAfterAPartialErrorResponse covers the second fault
// committing before it fails. The handler panics with nothing written, so recovery
// proceeds; the error handler then writes and panics, and the plain fallback must
// not append to what the client already has.
func TestRecoverDoesNotAppendAfterAPartialErrorResponse(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(c *ossein.Context, err error) {
		_, _ = c.Response.Write([]byte("partial error body"))
		panic("failed midway through reporting")
	})
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("the handler failed first")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if body := response.Body.String(); body != "partial error body" {
		t.Fatalf("body = %q, want nothing appended to the committed response", body)
	}
}

// TestSecurityHeadersReachTheCommittedResponse asserts on the snapshot taken when
// the response was committed, not the live header map. Headers written after the
// response is committed never reach the client, and the live map cannot tell the
// difference.
func TestSecurityHeadersReachTheCommittedResponse(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders())
	app.Get("/", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "yes"})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	committed := response.Result().Header
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := committed.Get(header); got != want {
			t.Fatalf("%s = %q on the committed response, want %q", header, got, want)
		}
	}
}

// TestSecurityHeadersRemoveADefaultEntirely distinguishes an absent header from one
// present with an empty value, which serialises as a bare "Header:" on the wire.
func TestSecurityHeadersRemoveADefaultEntirely(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeaderValues(
		map[string]string{"Referrer-Policy": ""},
	)))
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if values := response.Result().Header.Values("Referrer-Policy"); len(values) != 0 {
		t.Fatalf("Referrer-Policy present as %q, want the header absent", values)
	}
}

// TestSecurityHeadersDoNotOverrideAnEmptyHeader covers a value deliberately blanked
// earlier in the chain, which Get cannot distinguish from absent.
func TestSecurityHeadersDoNotOverrideAnEmptyHeader(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Frame-Options", "")
			next.ServeHTTP(w, r)
		})
	})
	app.Use(middleware.SecurityHeaders())
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Result().Header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want the earlier blank value kept", got)
	}
}

// TestSecurityHeadersAreSafeUnderConcurrency covers the shared configuration map,
// which is read on every request.
func TestSecurityHeadersAreSafeUnderConcurrency(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders())
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	var group sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := response.Result().Header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q", got)
			}
		}()
	}
	close(start)
	group.Wait()
}

// TestRecoverLogsARealStackTrace asserts the stack's content. Checking only that
// the attribute key appears passes even with the capture removed.
func TestRecoverLogsARealStackTrace(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.Recover())
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	recorded := logs.String()
	if !strings.Contains(recorded, "runtime/panic.go") &&
		!strings.Contains(recorded, "middleware_test.") {
		t.Fatalf("log = %q, want recognisable frames in the stack", recorded)
	}
	if !strings.Contains(recorded, "level=ERROR") {
		t.Fatalf("log = %q, want the panic logged at error level", recorded)
	}
}

// TestAccessLogMeasuresDurationAndSize asserts the values rather than the presence
// of their keys.
func TestAccessLogMeasuresDurationAndSize(t *testing.T) {
	logger, logs := logCapture()

	const body = "0123456789"
	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog())
	app.Get("/slow", func(c *ossein.Context) error {
		time.Sleep(25 * time.Millisecond)
		_, err := c.Response.Write([]byte(body))
		return err
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))

	recorded := logs.String()
	if !strings.Contains(recorded, "bytes=10") {
		t.Fatalf("log = %q, want bytes=%d", recorded, len(body))
	}
	// The handler sleeps 25ms, so anything under 10ms means the duration is not
	// measured across the handler, and a nanosecond value would be far larger.
	if !containsDurationAtLeast(recorded, 10) || !containsDurationAtMost(recorded, 10_000) {
		t.Fatalf("log = %q, want a plausible duration in milliseconds", recorded)
	}
}

func containsDurationAtLeast(log string, milliseconds int) bool {
	value, ok := durationFromLog(log)
	return ok && value >= milliseconds
}

func containsDurationAtMost(log string, milliseconds int) bool {
	value, ok := durationFromLog(log)
	return ok && value <= milliseconds
}

func durationFromLog(log string) (int, bool) {
	const key = "duration_ms="
	index := strings.Index(log, key)
	if index < 0 {
		return 0, false
	}
	rest := log[index+len(key):]
	end := strings.IndexAny(rest, " \n")
	if end >= 0 {
		rest = rest[:end]
	}
	value := 0
	for _, char := range rest {
		if char < '0' || char > '9' {
			return 0, false
		}
		value = value*10 + int(char-'0')
	}
	return value, true
}

// TestAccessLogSkipsOnlyExactPaths keeps a skipped health path from suppressing
// everything beneath it.
func TestAccessLogSkipsOnlyExactPaths(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog(middleware.SkipPaths("/healthz")))
	app.Get("/healthz", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	app.Get("/healthz/details", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want nothing for the skipped path", logs.String())
	}

	app.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/healthz/details", nil))
	if !strings.Contains(logs.String(), "/healthz/details") {
		t.Fatalf("log = %q, want a path below the skipped one recorded", logs.String())
	}
}

// TestOptionsToleratesNil keeps a nil option from panicking at construction.
func TestOptionsToleratesNil(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.AccessLog(nil), middleware.SecurityHeaders(nil))
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}
