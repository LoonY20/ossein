package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

func TestTimeoutRendersAStructuredError(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/slow", func(c *ossein.Context) error {
		time.Sleep(2 * time.Second)
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body %q)", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
	if !strings.Contains(response.Body.String(), "timeout") {
		t.Fatalf("body = %q, want a timeout code", response.Body.String())
	}
}

// TestTimeoutUsesTheApplicationErrorContract keeps a custom error shape in charge.
func TestTimeoutUsesTheApplicationErrorContract(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(c *ossein.Context, err error) {
		_ = ossein.JSON(c.Response, http.StatusServiceUnavailable, map[string]any{"ok": false})
	})
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/slow", func(c *ossein.Context) error {
		time.Sleep(2 * time.Second)
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the custom handler's 503", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"ok":false`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestTimeoutKeepsResponseTrackingVisible is field-notes finding 11: reaching for
// http.TimeoutHandler hides *ossein.ResponseWriter from the handler, which silently
// disables the committed-response guard and the access log's status.
func TestTimeoutKeepsResponseTrackingVisible(t *testing.T) {
	app := ossein.New()
	var found bool
	app.Use(middleware.Timeout(time.Second))
	app.Get("/quick", func(c *ossein.Context) error {
		_, found = ossein.ResponseWriterFrom(c.Response)
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quick", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if !found {
		t.Fatal("the handler could not reach *ossein.ResponseWriter through the timeout")
	}
}

// TestTimeoutKeepsTheCommittedResponseGuardWorking is the consequence of the above:
// an error returned after a partial write must still be suppressed.
func TestTimeoutKeepsTheCommittedResponseGuardWorking(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(time.Second))
	app.Get("/partial", func(c *ossein.Context) error {
		if _, err := c.Response.Write([]byte(`{"partial":true}`)); err != nil {
			return err
		}
		return ossein.NewHTTPError(http.StatusInternalServerError, "late", "failed midway")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/partial", nil))

	if body := response.Body.String(); body != `{"partial":true}` {
		t.Fatalf("body = %q, want no appended error document", body)
	}
}

// TestTimeoutAllowsStreamingWhileFlushing keeps server-sent events working through
// the wrapper, which the standard library's timeout handler prevents by buffering.
func TestTimeoutAllowsStreamingWhileFlushing(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(5 * time.Second))
	app.Get("/events", func(c *ossein.Context) error {
		controller := http.NewResponseController(c.Response)
		for i := 0; i < 3; i++ {
			if _, err := c.Response.Write([]byte("data: tick\n\n")); err != nil {
				return err
			}
			if err := controller.Flush(); err != nil {
				return err
			}
		}
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/events", nil))

	if got := strings.Count(response.Body.String(), "data: tick"); got != 3 {
		t.Fatalf("ticks = %d, want 3", got)
	}
	if !response.Flushed {
		t.Fatal("expected the response to have been flushed")
	}
}

// TestTimeoutCancelsTheRequestContext lets a well-behaved handler return early
// instead of running to completion in the background.
func TestTimeoutCancelsTheRequestContext(t *testing.T) {
	app := ossein.New()
	observed := make(chan error, 1)
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/waits", func(c *ossein.Context) error {
		<-c.Context().Done()
		observed <- c.Context().Err()
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/waits", nil))

	select {
	case err := <-observed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("context error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never observed cancellation")
	}
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.Code)
	}
}

// TestTimeoutDiscardsALateWrite keeps a handler that finishes after the deadline
// from corrupting the timeout response, and from racing with it.
func TestTimeoutDiscardsALateWrite(t *testing.T) {
	released := make(chan struct{})
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late", func(c *ossein.Context) error {
		<-c.Context().Done()
		// Deliberately ignore cancellation and answer anyway.
		_, _ = c.Response.Write([]byte("LATE OUTPUT"))
		close(released)
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/late", nil))

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never finished")
	}

	if strings.Contains(response.Body.String(), "LATE OUTPUT") {
		t.Fatalf("body = %q, want the late write discarded", response.Body.String())
	}
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.Code)
	}
}

// TestTimeoutDiscardsALateStatus covers a handler that sets a status after the
// deadline, which would otherwise overwrite the 504 the client already received.
func TestTimeoutDiscardsALateStatus(t *testing.T) {
	released := make(chan struct{})
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late-status", func(c *ossein.Context) error {
		<-c.Context().Done()
		defer close(released)
		// Ignore cancellation and answer anyway.
		return c.NoContent(http.StatusCreated)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/late-status", nil))

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never finished")
	}

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want the 504 to stand", response.Code)
	}
}

// TestTimeoutLeavesACommittedResponseAlone keeps a streaming handler that overruns
// from having a timeout document appended to bytes already sent.
func TestTimeoutLeavesACommittedResponseAlone(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/stream", func(c *ossein.Context) error {
		if _, err := c.Response.Write([]byte("first chunk")); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))

	if body := response.Body.String(); body != "first chunk" {
		t.Fatalf("body = %q, want only what was already sent", body)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", response.Code)
	}
}

// TestTimeoutDoesNotRenderOverACommittedResponse isolates the guard in the timeout
// itself. The default error handler has its own committed-response check, so with it
// in place this passes whether or not the timeout checks as well.
func TestTimeoutDoesNotRenderOverACommittedResponse(t *testing.T) {
	app := ossein.New()
	app.SetErrorHandler(func(c *ossein.Context, err error) {
		_, _ = c.Response.Write([]byte("APPENDED"))
	})
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/stream", func(c *ossein.Context) error {
		if _, err := c.Response.Write([]byte("first chunk")); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))

	if body := response.Body.String(); body != "first chunk" {
		t.Fatalf("body = %q, want nothing appended to the committed response", body)
	}
}

// TestTimeoutForwardsAPanic is the difference between a middleware and a crash: the
// handler runs on another goroutine, so a panic there would otherwise bypass every
// recover and take the process down.
func TestTimeoutForwardsAPanic(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Recover(), middleware.Timeout(time.Second))
	app.Get("/boom", func(*ossein.Context) error {
		panic("kaboom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "internal_error") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestTimeoutLogsAPanicAfterTheDeadline keeps a panic that arrives too late to
// answer from being swallowed silently, which is what the standard library does.
func TestTimeoutLogsAPanicAfterTheDeadline(t *testing.T) {
	logger, logs := logCapture()

	panicked := make(chan struct{})
	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late-panic", func(c *ossein.Context) error {
		<-c.Context().Done()
		defer close(panicked)
		panic("too late to report")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/late-panic", nil))

	select {
	case <-panicked:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never panicked")
	}
	// The panic is delivered asynchronously; wait briefly for the log.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "too late to report") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.Code)
	}
	if !strings.Contains(logs.String(), "too late to report") {
		t.Fatalf("log = %q, want the late panic recorded", logs.String())
	}
}

func TestTimeoutPassesQuickRequestsThrough(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(time.Second))
	app.Get("/quick", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quick", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestTimeoutIsRecordedByTheAccessLog confirms the composed stack reports the
// status the client received.
func TestTimeoutIsRecordedByTheAccessLog(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(
		middleware.AccessLog(),
		middleware.Recover(),
		middleware.Timeout(20*time.Millisecond),
	)
	app.Get("/slow", func(c *ossein.Context) error {
		time.Sleep(2 * time.Second)
		return nil
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))

	if !strings.Contains(logs.String(), "status=504") {
		t.Fatalf("log = %q, want the 504 recorded", logs.String())
	}
}

// TestTimeoutIsRaceFreeUnderConcurrency exercises the guard that serialises a late
// handler write against the timeout response.
func TestTimeoutIsRaceFreeUnderConcurrency(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(5 * time.Millisecond))
	app.Get("/racy", func(c *ossein.Context) error {
		<-c.Context().Done()
		for i := 0; i < 20; i++ {
			_, _ = c.Response.Write([]byte("late"))
		}
		return nil
	})

	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/racy", nil))
			if response.Code != http.StatusGatewayTimeout {
				t.Errorf("status = %d, want 504", response.Code)
			}
		}()
	}
	group.Wait()
}

// TestTimeoutRejectsANonPositiveDuration keeps a misconfiguration from silently
// disabling the deadline.
func TestTimeoutRejectsANonPositiveDuration(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic for a non-positive timeout")
		}
	}()
	middleware.Timeout(0)
}
