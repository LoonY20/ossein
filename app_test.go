package ossein

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetRoute(t *testing.T) {
	app := New()
	app.Get("/users/{id}", func(ctx *Context) error {
		if got := ctx.Param("id"); got != "42" {
			t.Fatalf("expected path value 42, got %q", got)
		}
		return ctx.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	response := httptest.NewRecorder()

	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, response.Code)
	}
}

func TestMiddlewareOrder(t *testing.T) {
	app := New()
	order := make([]string, 0, 3)

	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "first")
			next.ServeHTTP(w, r)
		})
	})

	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "second")
			next.ServeHTTP(w, r)
		})
	})

	app.Get("/", func(ctx *Context) error {
		order = append(order, "handler")
		return ctx.NoContent(http.StatusOK)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	expected := []string{"first", "second", "handler"}
	for i := range expected {
		if order[i] != expected[i] {
			t.Fatalf("expected order %v, got %v", expected, order)
		}
	}
}

func TestNativeHTTPHandlerEscapeHatch(t *testing.T) {
	app := New()
	app.HandleHTTPFunc(http.MethodGet, "/native", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/native", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, response.Code)
	}
}

func TestJSON(t *testing.T) {
	response := httptest.NewRecorder()

	err := JSON(response, http.StatusCreated, map[string]string{"status": "ok"})
	if err != nil {
		t.Fatalf("unexpected JSON error: %v", err)
	}

	if response.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, response.Code)
	}

	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type %q", got)
	}

	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected body %q", got)
	}
}

func TestHandlerChainBuildsOnce(t *testing.T) {
	app := New()
	builds := 0
	app.Use(func(next http.Handler) http.Handler {
		builds++
		return next
	})
	app.Get("/", func(ctx *Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if builds != 1 {
		t.Fatalf("middleware chain built %d times, expected once", builds)
	}
}

func TestConflictingRoutesFailStart(t *testing.T) {
	app := New()
	handler := func(ctx *Context) error {
		return ctx.NoContent(http.StatusNoContent)
	}
	app.Get("/dup", handler)
	app.Get("/dup", handler)

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("Start did not report the conflicting route")
	}
	if !strings.Contains(err.Error(), "/dup") {
		t.Fatalf("conflict error does not mention the route: %v", err)
	}
}

func TestRouteRegistrationAfterServingPanics(t *testing.T) {
	app := New()
	app.Get("/", func(ctx *Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	defer func() {
		if recover() == nil {
			t.Fatal("route registration after serving did not panic")
		}
	}()
	app.Get("/late", func(ctx *Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})
}

func TestUseAfterServingPanics(t *testing.T) {
	app := New()
	app.Get("/", func(ctx *Context) error {
		return ctx.NoContent(http.StatusNoContent)
	})
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	defer func() {
		if recover() == nil {
			t.Fatal("Use after serving did not panic")
		}
	}()
	app.Use(func(next http.Handler) http.Handler { return next })
}
