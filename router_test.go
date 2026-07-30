package ossein

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteGroups(t *testing.T) {
	app := New()
	middlewareCalled := false

	app.Group("/api", func(api *Router) {
		api.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				middlewareCalled = true
				next.ServeHTTP(w, r)
			})
		})

		api.Group("/v1", func(v1 *Router) {
			v1.Get("/users/{id}", func(ctx *Context) error {
				return ctx.JSON(http.StatusOK, map[string]string{"id": ctx.Param("id")})
			})
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/users/42", nil))

	if !middlewareCalled {
		t.Fatal("expected group middleware to run")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if got := response.Body.String(); got != "{\"id\":\"42\"}\n" {
		t.Fatalf("unexpected body %q", got)
	}
}

func headerMiddleware(name, value string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(name, value)
			next.ServeHTTP(w, r)
		})
	}
}

func TestGroupUseAppliesToRoutesRegisteredEarlier(t *testing.T) {
	app := New()
	app.Group("/api", func(api *Router) {
		api.Get("/first", func(ctx *Context) error {
			return ctx.NoContent(http.StatusNoContent)
		})
		api.Use(headerMiddleware("X-Group", "yes"))
		api.Get("/second", func(ctx *Context) error {
			return ctx.NoContent(http.StatusNoContent)
		})
	})

	for _, path := range []string{"/api/first", "/api/second"} {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Header().Get("X-Group") != "yes" {
			t.Fatalf("group middleware did not run for %s", path)
		}
	}
}

func TestGroupUseAppliesToNestedGroupsCreatedEarlier(t *testing.T) {
	app := New()
	app.Group("/api", func(api *Router) {
		api.Group("/v1", func(v1 *Router) {
			v1.Get("/users", func(ctx *Context) error {
				return ctx.NoContent(http.StatusNoContent)
			})
		})
		api.Use(headerMiddleware("X-Parent", "yes"))
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if response.Header().Get("X-Parent") != "yes" {
		t.Fatal("parent group middleware did not apply to nested group route")
	}
}

func TestGroupRootRoutePreservesTrailingSlash(t *testing.T) {
	app := New()
	app.Group("/api", func(api *Router) {
		api.Get("/", func(ctx *Context) error {
			return ctx.NoContent(http.StatusNoContent)
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, response.Code)
	}
}

func TestGroupMethodAndNativeHandlerHelpers(t *testing.T) {
	app := New()
	app.Group("/api/", func(api *Router) {
		api.HandleHTTPFunc(http.MethodGet, "/native", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		})
		api.Post("/post", noContentHandler)
		api.Put("/put", noContentHandler)
		api.Patch("/patch", noContentHandler)
		api.Delete("/delete", noContentHandler)
	})

	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/native", http.StatusAccepted},
		{http.MethodPost, "/api/post", http.StatusNoContent},
		{http.MethodPut, "/api/put", http.StatusNoContent},
		{http.MethodPatch, "/api/patch", http.StatusNoContent},
		{http.MethodDelete, "/api/delete", http.StatusNoContent},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status {
			t.Fatalf("%s %s status = %d", test.method, test.path, response.Code)
		}
	}
}

func TestGroupRejectsInvalidPrefix(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid prefix to panic")
		}
	}()
	New().Group("api", nil)
}

func noContentHandler(ctx *Context) error {
	return ctx.NoContent(http.StatusNoContent)
}
