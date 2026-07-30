package ossein

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRoutedApp() *App {
	app := New()
	app.Get("/items", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	app.Post("/items", func(c *Context) error { return c.NoContent(http.StatusCreated) })
	return app
}

func TestUnmatchedRouteRendersJSONError(t *testing.T) {
	app := newRoutedApp()

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("body = %q, want the not_found error envelope", body)
	}
}

func TestMethodNotAllowedRendersJSONErrorAndKeepsAllow(t *testing.T) {
	app := newRoutedApp()

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/items", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
	allow := response.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("Allow = %q, want it to list GET and POST", allow)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":"method_not_allowed"`) {
		t.Fatalf("body = %q, want the method_not_allowed error envelope", body)
	}
}

func TestCustomNotFoundHandler(t *testing.T) {
	app := newRoutedApp()
	app.SetNotFoundHandler(func(c *Context) error {
		return c.JSON(http.StatusNotFound, map[string]string{"detail": "nothing here"})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, "nothing here") {
		t.Fatalf("body = %q", body)
	}
}

func TestCustomMethodNotAllowedHandlerSeesAllow(t *testing.T) {
	app := newRoutedApp()
	app.SetMethodNotAllowedHandler(func(c *Context) error {
		return c.JSON(http.StatusMethodNotAllowed, map[string]string{
			"allow": c.Response.Header().Get("Allow"),
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/items", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, "GET") {
		t.Fatalf("body = %q, want the Allow header visible to the handler", body)
	}
}

// TestNotFoundHandlerErrorsUseErrorHandler proves the fallback handlers are
// ordinary handlers: returning an error routes through the error pipeline.
func TestNotFoundHandlerErrorsUseErrorHandler(t *testing.T) {
	app := newRoutedApp()
	app.SetNotFoundHandler(func(*Context) error {
		return NewHTTPError(http.StatusGone, "gone", "This API was retired")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if response.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":"gone"`) {
		t.Fatalf("body = %q", body)
	}
}

// TestDefaultNotFoundRespectsCustomErrorHandler proves the default fallbacks go
// through the application's error handler rather than writing JSON directly, so
// an application that changes its error shape changes 404s with it.
func TestDefaultNotFoundRespectsCustomErrorHandler(t *testing.T) {
	app := newRoutedApp()
	app.SetErrorHandler(func(c *Context, err error) {
		var httpErr *HTTPError
		status := http.StatusInternalServerError
		if errors.As(err, &httpErr) {
			status = httpErr.Status
		}
		_ = JSON(c.Response, status, map[string]any{"ok": false, "detail": err.Error()})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, `"ok":false`) {
		t.Fatalf("body = %q, want the custom error shape", body)
	}
}

// TestApplicationMiddlewareRunsForUnmatchedRoutes keeps access logging and
// similar concerns working on the miss path.
func TestApplicationMiddlewareRunsForUnmatchedRoutes(t *testing.T) {
	app := newRoutedApp()
	var ran bool
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = true
			next.ServeHTTP(w, r)
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if !ran {
		t.Fatal("application middleware did not run for an unmatched route")
	}
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

// TestSubtreeRedirectSurvivesNotFoundHandling is the regression guard for the
// implementation strategy: ServeMux's implicit redirects must not be mistaken
// for misses.
func TestSubtreeRedirectSurvivesNotFoundHandling(t *testing.T) {
	app := New()
	app.Get("/items/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	app.SetNotFoundHandler(func(*Context) error {
		return NewHTTPError(http.StatusTeapot, "should_not_happen", "not found ran")
	})

	cases := []struct {
		name     string
		target   string
		status   int
		location string
	}{
		{"subtree redirect", "/items", http.StatusMovedPermanently, "/items/"},
		{"unclean path", "/items/../items/x", http.StatusMovedPermanently, "/items/x"},
		{"exact subtree", "/items/", http.StatusNoContent, ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, testCase.target, nil))

			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d (body %q)",
					response.Code, testCase.status, response.Body.String())
			}
			if testCase.location != "" {
				if location := response.Header().Get("Location"); location != testCase.location {
					t.Fatalf("Location = %q, want %q", location, testCase.location)
				}
			}
		})
	}
}

func TestMatchedRoutesAreUnaffected(t *testing.T) {
	app := newRoutedApp()

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

// TestNilFallbackHandlersRestoreDefaults mirrors SetErrorHandler(nil).
func TestNilFallbackHandlersRestoreDefaults(t *testing.T) {
	app := newRoutedApp()
	app.SetNotFoundHandler(func(*Context) error { return nil })
	app.SetNotFoundHandler(nil)
	app.SetMethodNotAllowedHandler(nil)

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("body = %q", body)
	}
}
