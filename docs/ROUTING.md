# Routing

Ossein routing is built on Go's `http.ServeMux` and uses its Go 1.22+ patterns.

```go
app.Get("/users/{id}", show)
app.Post("/users", store)
app.Delete("/users/{id}", destroy)
```

## Groups and middleware

```go
app.Group("/api", func(api *ossein.Router) {
	api.Use(auth)

	api.Group("/v1", func(v1 *ossein.Router) {
		v1.Get("/users/{id}", show).Named("api.users.show")
	})
})
```

Group middleware applies to every route declared in the group and its nested
groups, regardless of where `Use` appears inside the `Group` block: routes are
registered when the application handler is built, not at declaration time.
Routes and middleware cannot be added once the application starts serving
requests.

## Named routes

Every registration returns a `*ossein.Route`:

```go
app.Get("/users/{id}", show).Named("users.show")
```

Names must be non-empty and unique. Duplicate names panic during application
setup. Conflicting route patterns are reported as an error by `Start`, `Run`,
and `RunContext` before the server accepts traffic.

Generate a path with escaped values:

```go
path, err := app.URL("users.show", map[string]string{
	"id": "42",
})
```

Catch-all parameters are supported:

```go
app.Get("/files/{path...}", download).Named("files.download")
```

## Registry

`app.Routes()` returns a declaration-order snapshot. `app.SortedRoutes()` sorts
by pattern and method for deterministic CLI or documentation output.

Native `http.Handler` routes are recorded in the same registry:

```go
app.HandleHTTP("GET", "/native", handler)
```
