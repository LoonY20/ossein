package ossein

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExpectedHTTPError(t *testing.T) {
	app := New()
	app.Get("/users/{id}", func(ctx *Context) error {
		return NotFound("user_not_found", "User not found")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/42", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
	}
	if got := response.Body.String(); got != "{\"error\":{\"code\":\"user_not_found\",\"message\":\"User not found\"}}\n" {
		t.Fatalf("unexpected body %q", got)
	}
}

func TestUnexpectedErrorIsNotLeaked(t *testing.T) {
	app := New()
	app.Get("/", func(ctx *Context) error {
		return errors.New("database password leaked here")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, response.Code)
	}
	if got := response.Body.String(); got != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal Server Error\"}}\n" {
		t.Fatalf("unexpected body %q", got)
	}
}

func TestCustomErrorHandler(t *testing.T) {
	app := New()
	app.SetErrorHandler(func(ctx *Context, err error) {
		_ = ctx.JSON(http.StatusTeapot, map[string]string{"error": err.Error()})
	})
	app.Get("/", func(ctx *Context) error { return errors.New("boom") })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusTeapot {
		t.Fatalf("expected status %d, got %d", http.StatusTeapot, response.Code)
	}
}
