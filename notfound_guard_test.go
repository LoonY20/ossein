package ossein

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPathValuesSurviveDispatch is the regression guard for how 404 handling is
// implemented: intercepting the miss must not cost route wildcards.
func TestPathValuesSurviveDispatch(t *testing.T) {
	app := New()
	app.Get("/users/{id}/posts/{postID}", func(c *Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"id":     c.Param("id"),
			"postID": c.Param("postID"),
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/42/posts/7", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"id":"42"`) || !strings.Contains(body, `"postID":"7"`) {
		t.Fatalf("body = %q, want both path values populated", body)
	}
}

// TestHandlerOwn404IsNotReplaced separates a matched route that answers 404 from
// a genuine routing miss.
func TestHandlerOwn404IsNotReplaced(t *testing.T) {
	app := New()
	app.Get("/users/{id}", func(*Context) error {
		return NotFound("user_not_found", "No such user")
	})
	app.SetNotFoundHandler(func(*Context) error {
		return NewHTTPError(http.StatusTeapot, "route_missing", "no route matched")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/999", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, "user_not_found") {
		t.Fatalf("body = %q, want the handler's own error", body)
	}
}

// TestResponseWriterIsReachableFromHandlers keeps the already-committed guard
// working: handlers must still see the Ossein ResponseWriter even though
// dispatch wraps the writer to watch for routing misses.
func TestResponseWriterIsReachableFromHandlers(t *testing.T) {
	app := New()
	var found bool
	app.Get("/probe", func(c *Context) error {
		_, found = ResponseWriterFrom(c.Response)
		return c.NoContent(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/probe", nil))
	if !found {
		t.Fatal("handlers can no longer reach *ossein.ResponseWriter")
	}
}

// TestErrorAfterCommitStillSuppressed depends on the guard above.
func TestErrorAfterCommitStillSuppressed(t *testing.T) {
	app := New()
	app.Get("/partial", func(c *Context) error {
		if _, err := c.Response.Write([]byte(`{"partial":true}`)); err != nil {
			return err
		}
		return NewHTTPError(http.StatusInternalServerError, "late", "failed midway")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/partial", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", response.Code)
	}
	if body := response.Body.String(); body != `{"partial":true}` {
		t.Fatalf("body = %q, want no appended error envelope", body)
	}
}

// TestStreamingStillFlushesThroughDispatch keeps server-sent events working
// through the writer chain dispatch introduces.
func TestStreamingStillFlushesThroughDispatch(t *testing.T) {
	app := New()
	app.Get("/events", func(c *Context) error {
		c.Response.Header().Set("Content-Type", "text/event-stream")
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

// TestNotFoundResponseHasNoLeftoverPlainTextHeaders checks that headers set for
// the discarded standard-library body do not leak into the JSON response.
func TestNotFoundResponseHasNoLeftoverPlainTextHeaders(t *testing.T) {
	app := newRoutedApp()

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if contentType := response.Header().Get("Content-Type"); strings.Contains(contentType, "text/plain") {
		t.Fatalf("Content-Type = %q, want no leftover text/plain", contentType)
	}
	if body := response.Body.String(); strings.Contains(body, "404 page not found") {
		t.Fatalf("body = %q, want the standard library body discarded", body)
	}
}
