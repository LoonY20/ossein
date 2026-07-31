package middleware_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

// TestTimeoutGuardsTheHeaderMap covers the crash this middleware could cause on an
// ordinary timing coincidence rather than a pathological handler. Rendering the 504
// sets a Content-Type, and a handler answering with JSON sets one too; with the
// header map unguarded those are concurrent map writes, which is a fatal error no
// recover can catch.
func TestTimeoutGuardsTheHeaderMap(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(4 * time.Millisecond))
	app.Get("/edge", func(c *ossein.Context) error {
		// Straddle the deadline, which is what a timeout set near p99 does.
		time.Sleep(4 * time.Millisecond)
		return c.JSON(http.StatusOK, map[string]string{"handler": "won"})
	})

	var group sync.WaitGroup
	for i := 0; i < 200; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/edge", nil))
			switch response.Code {
			case http.StatusOK, http.StatusGatewayTimeout:
			default:
				t.Errorf("status = %d, want 200 or 504", response.Code)
			}
		}()
	}
	group.Wait()
}

// TestTimeoutDiscardsLateHeaderChanges keeps a handler that sets headers after the
// deadline from altering the response already sent.
func TestTimeoutDiscardsLateHeaderChanges(t *testing.T) {
	released := make(chan struct{})
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late-header", func(c *ossein.Context) error {
		<-c.Context().Done()
		defer close(released)
		c.Response.Header().Set("X-Late", "yes")
		return nil
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/late-header", nil))

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never finished")
	}

	if got := response.Result().Header.Get("X-Late"); got != "" {
		t.Fatalf("X-Late = %q, want the late header discarded", got)
	}
}

// TestTimeoutDoesNotReportAClientDisconnectAsATimeout keeps an abandoned request
// from inflating the 504 rate, which is the signal a timeout exists to produce.
func TestTimeoutDoesNotReportAClientDisconnectAsATimeout(t *testing.T) {
	logger, logs := logCapture()

	entered := make(chan struct{})
	app := ossein.New(ossein.WithLogger(logger))
	app.Use(middleware.AccessLog(), middleware.Timeout(10*time.Second))
	app.Get("/abandoned", func(c *ossein.Context) error {
		close(entered)
		<-c.Context().Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/abandoned", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	go func() {
		<-entered
		cancel()
	}()
	app.ServeHTTP(response, request)

	if response.Code == http.StatusGatewayTimeout {
		t.Fatal("a client disconnect was reported as a gateway timeout")
	}
	if strings.Contains(logs.String(), "status=504") {
		t.Fatalf("log = %q, want no 504 for an abandoned request", logs.String())
	}
}

// TestTimeoutLeaksOneGoroutinePerHungHandler pins the cost of a handler that never
// returns. The handler goroutine is unavoidable, but nothing else may park.
func TestTimeoutLeaksOneGoroutinePerHungHandler(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(10 * time.Millisecond))
	app.Get("/hung", func(*ossein.Context) error {
		select {}
	})

	settle := func() {
		for i := 0; i < 10; i++ {
			runtime.Gosched()
			time.Sleep(10 * time.Millisecond)
		}
	}

	settle()
	before := runtime.NumGoroutine()

	const requests = 10
	for i := 0; i < requests; i++ {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hung", nil))
		if response.Code != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504", response.Code)
		}
	}

	settle()
	leaked := runtime.NumGoroutine() - before

	if leaked > requests {
		t.Fatalf("leaked %d goroutines for %d hung requests; only the handler's own "+
			"goroutine may remain", leaked, requests)
	}
}

// TestTimeoutPreservesTheRequestContext keeps the request-scoped logger, request ID,
// and error handler reachable inside the handler. Deriving the deadline from a fresh
// background context would strip all three and no other test would notice.
func TestTimeoutPreservesTheRequestContext(t *testing.T) {
	var requestID string
	var sawHandler bool

	app := ossein.New()
	app.Use(middleware.Timeout(time.Second))
	app.Get("/identity", func(c *ossein.Context) error {
		requestID = c.RequestID()
		// WriteError only finds the application's handler through the request
		// context, so this proves it survived the wrapper.
		sawHandler = strings.Contains(c.Request.Header.Get("X-Request-ID"), "") &&
			c.RequestID() != ""
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/identity", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if requestID == "" || !sawHandler {
		t.Fatal("the handler lost the request-scoped context through the timeout")
	}
}

// TestTimeoutLateWriteReportsSuccess pins the io.Writer contract for a discarded
// write: returning a short count with a nil error would derail io.Copy.
func TestTimeoutLateWriteReportsSuccess(t *testing.T) {
	const payload = 4096
	copied := make(chan int64, 1)
	failed := make(chan error, 1)

	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late-copy", func(c *ossein.Context) error {
		<-c.Context().Done()
		written, err := io.Copy(c.Response, strings.NewReader(strings.Repeat("x", payload)))
		copied <- written
		failed <- err
		return nil
	})

	app.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/late-copy", nil))

	select {
	case written := <-copied:
		if written != payload {
			t.Fatalf("io.Copy wrote %d, want %d reported", written, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never finished")
	}
	if err := <-failed; err != nil {
		t.Fatalf("io.Copy error = %v, want nil for a discarded write", err)
	}
}

// statusRecorder records every WriteHeader call, which httptest.ResponseRecorder
// does not: it silently ignores the second one.
type statusRecorder struct {
	http.ResponseWriter
	mu       sync.Mutex
	statuses []int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.mu.Lock()
	w.statuses = append(w.statuses, status)
	w.mu.Unlock()
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) recorded() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.statuses...)
}

// TestTimeoutSendsExactlyOneStatus keeps a late WriteHeader from reaching the real
// writer, which a recorder hides by ignoring repeat calls.
func TestTimeoutSendsExactlyOneStatus(t *testing.T) {
	released := make(chan struct{})
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/late-status", func(c *ossein.Context) error {
		<-c.Context().Done()
		defer close(released)
		return c.NoContent(http.StatusCreated)
	})

	writer := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	app.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/late-status", nil))

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never finished")
	}

	statuses := writer.recorded()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %v, want exactly one reaching the writer", statuses)
	}
	if statuses[0] != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", statuses[0])
	}
}

// hijackableRecorder supports Hijack so the timeout's behaviour on an upgraded
// connection can be observed.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (w *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

// TestTimeoutDoesNotAnswerOnAHijackedConnection keeps a websocket upgrade that
// outlives the deadline from having a 504 written into a connection the handler now
// owns.
func TestTimeoutDoesNotAnswerOnAHijackedConnection(t *testing.T) {
	upgraded := make(chan struct{})
	app := ossein.New()
	app.Use(middleware.Timeout(20 * time.Millisecond))
	app.Get("/ws", func(c *ossein.Context) error {
		if _, _, err := http.NewResponseController(c.Response).Hijack(); err != nil {
			return err
		}
		close(upgraded)
		<-c.Context().Done()
		return nil
	})

	writer := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	app.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/ws", nil))

	select {
	case <-upgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never upgraded")
	}

	if !writer.hijacked {
		t.Fatal("the hijack never reached the underlying writer")
	}
	if writer.Code == http.StatusGatewayTimeout {
		t.Fatal("a 504 was written into a hijacked connection")
	}
	if body := writer.Body.String(); body != "" {
		t.Fatalf("body = %q, want nothing written to a hijacked connection", body)
	}
}

// TestTimeoutReportsAnUnsupportedHijack keeps the failure visible to the handler
// rather than being mistaken for a successful upgrade.
func TestTimeoutReportsAnUnsupportedHijack(t *testing.T) {
	var hijackErr error
	app := ossein.New()
	app.Use(middleware.Timeout(time.Second))
	app.Get("/ws", func(c *ossein.Context) error {
		_, _, hijackErr = http.NewResponseController(c.Response).Hijack()
		return c.NoContent(http.StatusNoContent)
	})

	// httptest.ResponseRecorder does not support hijacking.
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ws", nil))

	if hijackErr == nil {
		t.Fatal("expected an error when the writer cannot be hijacked")
	}
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the handler's response to still be sent", response.Code)
	}
}

// TestTimeoutDeadlineIsTheOnlyTriggerForRejection keeps writes flowing while the
// request is merely being cancelled for another reason.
func TestTimeoutDeadlineIsTheOnlyTriggerForRejection(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.Timeout(10 * time.Second))
	app.Get("/cancelled", func(c *ossein.Context) error {
		<-c.Context().Done()
		if !errors.Is(c.Context().Err(), context.Canceled) {
			return ossein.NewHTTPError(http.StatusInternalServerError, "unexpected", "not cancelled")
		}
		// The deadline has not passed, so this must still reach the client.
		_, err := c.Response.Write([]byte("answered anyway"))
		return err
	})

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/cancelled", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	app.ServeHTTP(response, request)

	if body := response.Body.String(); !strings.Contains(body, "answered anyway") {
		t.Fatalf("body = %q, want the handler's write to reach the client", body)
	}
}
