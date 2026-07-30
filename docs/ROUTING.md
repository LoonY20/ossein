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

## Unmatched routes and methods

A request that matches no route, or matches a path with a different method, is
rendered by the application rather than by `ServeMux`, so a JSON API keeps its
error contract at the edges:

```json
{"error":{"code":"not_found","message":"The requested resource does not exist"}}
{"error":{"code":"method_not_allowed","message":"The request method is not supported for this resource"}}
```

The `Allow` header that `ServeMux` computes is preserved on a `405`. Replace
either response with an ordinary handler:

```go
app.SetNotFoundHandler(func(c *ossein.Context) error {
    return ossein.NotFound("route_missing", "Check the API reference")
})

app.SetMethodNotAllowedHandler(func(c *ossein.Context) error {
    c.Logger().Warn("method not allowed", "allow", c.Response.Header().Get("Allow"))
    return ossein.NewHTTPError(http.StatusMethodNotAllowed, "wrong_method", "See Allow")
})
```

Passing `nil` to either restores the default. Both are ordinary handlers, so a
returned error goes through the application's `ErrorHandler` like any other, and
an application that customizes its error shape changes these responses with it.

Both must be set before the application starts serving requests, like routes and
middleware.

Only genuine routing misses are intercepted, which has three consequences worth
knowing:

- A matched handler that answers `404` itself keeps its own response, and the
  implicit subtree and path-cleaning redirects `ServeMux` performs are untouched.
- A route that delegates to another `http.ServeMux` owns everything below it, so
  that mux's own `404` reaches the client unchanged.
- Registering a catch-all such as `app.Get("/", spa)` means the router always
  matches a `GET`, so the not-found handler never runs for one. A different
  method on an unmatched path then produces `405`, not `404`, because `/` is
  registered for `GET` only.

A handler that returns without writing anything still produces the status it
replaced, so a miss can never answer `200`.

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
