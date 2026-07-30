package ossein

import (
	"net/http"
	"testing"
)

func TestRouteRegistryAndNamedURL(t *testing.T) {
	app := New()
	app.Get("/users/{id}", func(*Context) error { return nil }).Named("users.show")
	app.Post("/files/{path...}", func(*Context) error { return nil }).Named("files.store")

	routes := app.Routes()
	if len(routes) != 2 || routes[0].Name != "users.show" {
		t.Fatalf("unexpected routes: %#v", routes)
	}

	path, err := app.URL("users.show", map[string]string{"id": "hello world"})
	if err != nil || path != "/users/hello%20world" {
		t.Fatalf("URL() = %q, %v", path, err)
	}

	path, err = app.URL("files.store", map[string]string{"path": "docs/read me.txt"})
	if err != nil || path != "/files/docs/read%20me.txt" {
		t.Fatalf("wildcard URL() = %q, %v", path, err)
	}

	route, ok := app.NamedRoute("users.show")
	if !ok || route.Method != http.MethodGet {
		t.Fatalf("NamedRoute() = %#v, %v", route, ok)
	}
}

func TestNamedRouteRequiresUniqueName(t *testing.T) {
	app := New()
	app.Get("/one", func(*Context) error { return nil }).Named("shared")

	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate route name to panic")
		}
	}()
	app.Get("/two", func(*Context) error { return nil }).Named("shared")
}

func TestNamedRouteReportsMissingParameters(t *testing.T) {
	app := New()
	app.Get("/users/{id}", func(*Context) error { return nil }).Named("users.show")
	if _, err := app.URL("users.show", nil); err == nil {
		t.Fatal("expected missing parameter error")
	}
}

func TestRouteRegistrySortingRenamingAndMissingName(t *testing.T) {
	app := New()
	second := app.Post("/z", func(*Context) error { return nil }).Named("old")
	app.Get("/a", func(*Context) error { return nil })
	second.Named("new")

	if _, ok := app.NamedRoute("old"); ok {
		t.Fatal("old route name should be removed")
	}
	if _, ok := app.NamedRoute("missing"); ok {
		t.Fatal("missing route should not be found")
	}
	if _, err := app.URL("missing", nil); err == nil {
		t.Fatal("expected missing named route error")
	}
	routes := app.SortedRoutes()
	if routes[0].Pattern != "/a" || routes[1].Name != "new" {
		t.Fatalf("unexpected sorted routes: %#v", routes)
	}
}

func TestNamedRouteRejectsEmptyName(t *testing.T) {
	app := New()
	route := app.Get("/", func(*Context) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("expected empty route name to panic")
		}
	}()
	route.Named(" ")
}
