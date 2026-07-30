package ossein

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rejectingMiddleware is the shape this feature exists for: plain standard
// library middleware that needs to refuse a request using the application's own
// error contract.
func rejectingMiddleware(err error) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Allow") == "" {
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TestWriteErrorRendersTheDefaultEnvelope(t *testing.T) {
	app := New()
	app.Use(rejectingMiddleware(Unauthorized("missing_api_key", "X-API-Key is required")))
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}

	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body %q)", err, response.Body.String())
	}
	if envelope.Error.Code != "missing_api_key" {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
	if envelope.Error.Message != "X-API-Key is required" {
		t.Fatalf("message = %q", envelope.Error.Message)
	}
}

// TestWriteErrorUsesCustomErrorHandler is the point of the feature: middleware
// and handlers must not answer with two different error contracts.
func TestWriteErrorUsesCustomErrorHandler(t *testing.T) {
	app := New()
	app.SetErrorHandler(func(c *Context, err error) {
		_ = JSON(c.Response, http.StatusTeapot, map[string]any{
			"ok":     false,
			"detail": err.Error(),
		})
	})
	app.Use(rejectingMiddleware(Unauthorized("missing_api_key", "X-API-Key is required")))
	app.Get("/", func(*Context) error {
		return Unauthorized("missing_api_key", "X-API-Key is required")
	})

	fromMiddleware := httptest.NewRecorder()
	app.ServeHTTP(fromMiddleware, httptest.NewRequest(http.MethodGet, "/", nil))

	allowed := httptest.NewRequest(http.MethodGet, "/", nil)
	allowed.Header.Set("X-Allow", "yes")
	fromHandler := httptest.NewRecorder()
	app.ServeHTTP(fromHandler, allowed)

	if fromMiddleware.Body.String() != fromHandler.Body.String() {
		t.Fatalf("middleware and handler disagree:\n  middleware = %q\n  handler    = %q",
			fromMiddleware.Body.String(), fromHandler.Body.String())
	}
	if fromMiddleware.Code != fromHandler.Code {
		t.Fatalf("status: middleware = %d, handler = %d", fromMiddleware.Code, fromHandler.Code)
	}
	if !strings.Contains(fromMiddleware.Body.String(), `"ok":false`) {
		t.Fatalf("middleware body = %q, want the custom shape", fromMiddleware.Body.String())
	}
}

// TestWriteErrorFromGroupMiddleware covers the group pipeline as well.
func TestWriteErrorFromGroupMiddleware(t *testing.T) {
	app := New()
	app.Group("/api", func(api *Router) {
		api.Use(rejectingMiddleware(Forbidden("forbidden", "Not your resource")))
		api.Get("/items", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/items", nil))

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestWriteErrorRendersValidationErrors keeps field-level errors intact.
func TestWriteErrorRendersValidationErrors(t *testing.T) {
	app := New()
	app.Use(rejectingMiddleware(NewValidationError().Add("token", "Token is malformed")))
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	var envelope ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := envelope.Error.Fields["token"]; len(got) != 1 || got[0] != "Token is malformed" {
		t.Fatalf("fields = %v", envelope.Error.Fields)
	}
}

// TestWriteErrorOutsideAnOsseinRequestFallsBack keeps the helper usable from
// middleware mounted outside the application, where no error handler is in the
// request context.
func TestWriteErrorOutsideAnOsseinRequestFallsBack(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	WriteError(response, request, NotFound("gone", "Nothing here"))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"code":"gone"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestWriteErrorRespectsCommittedResponses reuses the guard that protects
// handlers, so middleware cannot write over a response already in flight.
func TestWriteErrorRespectsCommittedResponses(t *testing.T) {
	app := New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte("already sent")); err != nil {
				t.Errorf("Write: %v", err)
			}
			WriteError(w, r, NewHTTPError(http.StatusInternalServerError, "late", "too late"))
		})
	})
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", response.Code)
	}
	if body := response.Body.String(); body != "already sent" {
		t.Fatalf("body = %q, want no appended envelope", body)
	}
}

// TestWriteErrorRejectsNilError keeps a programming mistake from producing a
// blank 500 with no diagnosis.
func TestWriteErrorRejectsNilError(t *testing.T) {
	app := New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			WriteError(w, r, nil)
		})
	})
	app.Get("/", func(c *Context) error { return c.NoContent(http.StatusNoContent) })

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// TestErrorEnvelopeMatchesTheWireFormat pins the exported types against the
// bytes handlers actually produce.
func TestErrorEnvelopeMatchesTheWireFormat(t *testing.T) {
	app := New()
	app.Get("/", func(*Context) error {
		return NewValidationError().Add("email", "Email is required")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	var envelope ErrorEnvelope
	decoder := json.NewDecoder(strings.NewReader(response.Body.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("the exported envelope does not match the wire format: %v (body %q)",
			err, response.Body.String())
	}
	if envelope.Error.Code != "validation_failed" {
		t.Fatalf("code = %q", envelope.Error.Code)
	}
}
