package ossein

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type createUserRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (r *createUserRequest) Validate() error {
	validation := NewValidationError()
	if strings.TrimSpace(r.Name) == "" {
		validation.Add("name", "is required")
	}
	if !strings.Contains(r.Email, "@") {
		validation.Add("email", "must be a valid email address")
	}
	return validation.OrNil()
}

func TestBindJSONRunsValidation(t *testing.T) {
	app := New()
	app.Post("/users", func(ctx *Context) error {
		var request createUserRequest
		if err := ctx.BindJSON(&request); err != nil {
			return err
		}
		return ctx.JSON(http.StatusCreated, request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"","email":"invalid"}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, response.Code)
	}

	expected := "{\"error\":{\"code\":\"validation_failed\",\"message\":\"The request data is invalid\",\"fields\":{\"email\":[\"must be a valid email address\"],\"name\":[\"is required\"]}}}\n"
	if got := response.Body.String(); got != expected {
		t.Fatalf("unexpected body %q", got)
	}
}

func TestBindJSONRejectsUnknownFields(t *testing.T) {
	app := New()
	app.Post("/users", func(ctx *Context) error {
		var request createUserRequest
		return ctx.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Erik","email":"erik@example.com","admin":true}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
}

func TestBindJSONRejectsOversizedBody(t *testing.T) {
	app := New(WithMaxBindBytes(16))
	app.Post("/users", func(ctx *Context) error {
		var request createUserRequest
		return ctx.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, "request_too_large") {
		t.Fatalf("expected request_too_large error code, got %q", body)
	}
}

func TestBindJSONRejectsNonJSONContentType(t *testing.T) {
	app := New()
	app.Post("/users", func(ctx *Context) error {
		var request createUserRequest
		return ctx.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected status %d, got %d", http.StatusUnsupportedMediaType, response.Code)
	}
	if body := response.Body.String(); !strings.Contains(body, "unsupported_media_type") {
		t.Fatalf("expected unsupported_media_type error code, got %q", body)
	}
}

func TestBindJSONAcceptsJSONContentTypes(t *testing.T) {
	contentTypes := []string{
		"application/json",
		"application/json; charset=utf-8",
		"application/vnd.api+json",
	}
	for _, contentType := range contentTypes {
		app := New()
		app.Post("/users", func(ctx *Context) error {
			var request createUserRequest
			if err := ctx.BindJSON(&request); err != nil {
				return err
			}
			return ctx.JSON(http.StatusCreated, request)
		})

		request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
		request.Header.Set("Content-Type", contentType)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)

		if response.Code != http.StatusCreated {
			t.Fatalf("content type %q: expected status %d, got %d: %s", contentType, http.StatusCreated, response.Code, response.Body.String())
		}
	}
}

func TestBindJSONSuccess(t *testing.T) {
	app := New()
	app.Post("/users", func(ctx *Context) error {
		var request createUserRequest
		if err := ctx.BindJSON(&request); err != nil {
			return err
		}
		return ctx.JSON(http.StatusCreated, request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, response.Code, response.Body.String())
	}
}
