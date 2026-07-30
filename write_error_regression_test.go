package ossein

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCustomErrorHandlerDelegatingToWriteErrorTerminates covers the most likely
// mistake a handler author can make: "handle my own cases, let the framework
// render the rest". Without a guard this recurses into a stack overflow, which
// is a fatal error that no recover can catch.
func TestCustomErrorHandlerDelegatingToWriteErrorTerminates(t *testing.T) {
	app := New()
	app.SetErrorHandler(func(c *Context, err error) {
		var httpErr *HTTPError
		if asHTTPErrorForTest(err, &httpErr) && httpErr.Code == "mine" {
			_ = c.JSON(http.StatusTeapot, map[string]string{"handled": "mine"})
			return
		}
		// Anything else: hand it back to the framework.
		WriteError(c.Response, c.Request, err)
	})
	app.Get("/mine", func(*Context) error { return BadRequest("mine", "my own case") })
	app.Get("/other", func(*Context) error { return BadRequest("other", "framework case") })

	done := make(chan struct{})
	var mineCode, otherCode int
	var otherBody string
	go func() {
		defer close(done)

		mine := httptest.NewRecorder()
		app.ServeHTTP(mine, httptest.NewRequest(http.MethodGet, "/mine", nil))
		mineCode = mine.Code

		other := httptest.NewRecorder()
		app.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/other", nil))
		otherCode = other.Code
		otherBody = other.Body.String()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delegating to WriteError did not terminate")
	}

	if mineCode != http.StatusTeapot {
		t.Fatalf("own case status = %d, want 418", mineCode)
	}
	if otherCode != http.StatusBadRequest {
		t.Fatalf("delegated status = %d, want 400", otherCode)
	}
	if !strings.Contains(otherBody, `"code":"other"`) {
		t.Fatalf("delegated body = %q, want the default envelope", otherBody)
	}
}

// TestDefaultErrorHandlerIsAvailableForDelegation gives handler authors an
// explicit target, so reaching for WriteError is not the only option.
func TestDefaultErrorHandlerIsAvailableForDelegation(t *testing.T) {
	app := New()
	app.SetErrorHandler(func(c *Context, err error) {
		var httpErr *HTTPError
		if asHTTPErrorForTest(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			_ = c.JSON(http.StatusNotFound, map[string]string{"shape": "custom"})
			return
		}
		DefaultErrorHandler(c, err)
	})
	app.Get("/missing", func(*Context) error { return NotFound("gone", "gone") })
	app.Get("/broken", func(*Context) error { return Conflict("conflict", "clash") })

	custom := httptest.NewRecorder()
	app.ServeHTTP(custom, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if !strings.Contains(custom.Body.String(), `"shape":"custom"`) {
		t.Fatalf("custom body = %q", custom.Body.String())
	}

	delegated := httptest.NewRecorder()
	app.ServeHTTP(delegated, httptest.NewRequest(http.MethodGet, "/broken", nil))
	if delegated.Code != http.StatusConflict {
		t.Fatalf("delegated status = %d, want 409", delegated.Code)
	}
	if !strings.Contains(delegated.Body.String(), `"code":"conflict"`) {
		t.Fatalf("delegated body = %q, want the default envelope", delegated.Body.String())
	}
}

// TestWriteErrorGuardNeedsAnOsseinWriter documents the boundary of the
// committed-response guard. The guard reads state recorded on
// *ossein.ResponseWriter, so outside a request served by an application it only
// applies when the caller keeps such a writer across calls. A plain writer
// carries nowhere to record the first write, which no amount of wrapping inside
// WriteError can fix.
func TestWriteErrorGuardNeedsAnOsseinWriter(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	t.Run("wrapped writer is guarded", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writer := NewResponseWriter(recorder)

		WriteError(writer, request, BadRequest("one", "first"))
		WriteError(writer, request, Conflict("two", "second"))

		body := recorder.Body.String()
		if strings.Contains(body, "second") {
			t.Fatalf("body = %q, want only the first document", body)
		}
		if strings.Count(body, `"error"`) != 1 {
			t.Fatalf("body = %q, want exactly one error document", body)
		}
	})

	t.Run("plain writer cannot be guarded", func(t *testing.T) {
		recorder := httptest.NewRecorder()

		WriteError(recorder, request, BadRequest("one", "first"))
		WriteError(recorder, request, Conflict("two", "second"))

		// Recorded so the limitation is visible rather than assumed.
		if count := strings.Count(recorder.Body.String(), `"error"`); count != 2 {
			t.Logf("plain writer produced %d documents; the guard may now apply", count)
		}
	})
}

// TestWriteErrorRespectsMaxBindBytes keeps the binding limit consistent between
// the handler path and WriteError, since a custom error handler may read the
// request body.
func TestWriteErrorRespectsMaxBindBytes(t *testing.T) {
	const body = `{"name":"a name long enough to exceed the limit"}`

	var viaHandler, viaWriteError error
	app := New(WithMaxBindBytes(8))
	app.SetErrorHandler(func(c *Context, err error) {
		var target struct {
			Name string `json:"name"`
		}
		bindErr := c.BindJSON(&target)
		if strings.Contains(c.Request.URL.Path, "middleware") {
			viaWriteError = bindErr
		} else {
			viaHandler = bindErr
		}
		_ = c.NoContent(http.StatusInternalServerError)
	})
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "middleware") {
				WriteError(w, r, BadRequest("rejected", "rejected in middleware"))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	app.Post("/handler", func(*Context) error { return BadRequest("rejected", "rejected in handler") })

	for _, path := range []string{"/handler", "/middleware"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(httptest.NewRecorder(), request)
	}

	if viaHandler == nil {
		t.Fatal("expected the handler path to hit the bind limit")
	}
	if viaWriteError == nil {
		t.Fatalf("WriteError ignored WithMaxBindBytes: handler path got %v, WriteError got nil",
			viaHandler)
	}
}

// TestSetErrorHandlerPanicsAfterServing matches the freeze invariant the other
// setters enforce. The request path reads this field on every request, so an
// unsynchronized late write is a data race, and a snapshot taken at pipeline
// entry would disagree with the live field within one request.
func TestSetErrorHandlerPanicsAfterServing(t *testing.T) {
	app := New()
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic when replacing the error handler after serving")
		}
	}()
	app.SetErrorHandler(func(*Context, error) {})
}

// asHTTPErrorForTest keeps these tests readable without adding public API.
func asHTTPErrorForTest(err error, target **HTTPError) bool {
	candidate, ok := err.(*HTTPError)
	if ok {
		*target = candidate
	}
	return ok
}
