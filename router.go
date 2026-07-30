package ossein

import (
	"net/http"
	"strings"
)

// Router represents a route group with a shared prefix and middleware stack.
type Router struct {
	app        *App
	parent     *Router
	prefix     string
	middleware []Middleware
}

// Group creates a route group rooted at prefix.
func (a *App) Group(prefix string, configure func(*Router)) {
	router := &Router{app: a, prefix: normalizePrefix(prefix)}
	if configure != nil {
		configure(router)
	}
}

// Group creates a nested route group.
func (r *Router) Group(prefix string, configure func(*Router)) {
	child := &Router{
		app:    r.app,
		parent: r,
		prefix: joinRoutePattern(r.prefix, normalizePrefix(prefix)),
	}

	if configure != nil {
		configure(child)
	}
}

// Use registers middleware for routes declared on this group and its child
// groups. Middleware is resolved when the application handler is built, so it
// applies regardless of where inside the Group block it is registered.
func (r *Router) Use(middleware ...Middleware) {
	if r.app.frozen.Load() {
		panic("ossein: middleware must be registered before the application starts serving requests")
	}
	r.middleware = append(r.middleware, middleware...)
}

// chain returns the middleware registered from the root group down to this
// group, in registration order.
func (r *Router) chain() []Middleware {
	if r == nil {
		return nil
	}
	parent := r.parent.chain()
	if len(parent) == 0 {
		return r.middleware
	}
	return append(append([]Middleware(nil), parent...), r.middleware...)
}

// Handle registers an Ossein handler on the group.
func (r *Router) Handle(method, pattern string, handler HandlerFunc) *Route {
	return r.app.register(method, joinRoutePattern(r.prefix, pattern), handler, r)
}

// HandleHTTP registers an ordinary http.Handler on the group.
func (r *Router) HandleHTTP(method, pattern string, handler http.Handler) *Route {
	return r.app.registerHTTP(method, joinRoutePattern(r.prefix, pattern), handler, r)
}

// HandleHTTPFunc registers an ordinary http.HandlerFunc on the group.
func (r *Router) HandleHTTPFunc(method, pattern string, handler http.HandlerFunc) *Route {
	return r.HandleHTTP(method, pattern, handler)
}

func (r *Router) Get(pattern string, handler HandlerFunc) *Route {
	return r.Handle(http.MethodGet, pattern, handler)
}
func (r *Router) Post(pattern string, handler HandlerFunc) *Route {
	return r.Handle(http.MethodPost, pattern, handler)
}
func (r *Router) Put(pattern string, handler HandlerFunc) *Route {
	return r.Handle(http.MethodPut, pattern, handler)
}
func (r *Router) Patch(pattern string, handler HandlerFunc) *Route {
	return r.Handle(http.MethodPatch, pattern, handler)
}
func (r *Router) Delete(pattern string, handler HandlerFunc) *Route {
	return r.Handle(http.MethodDelete, pattern, handler)
}

func normalizePrefix(prefix string) string {
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		panic("ossein: route group prefix must start with /")
	}
	return strings.TrimRight(prefix, "/")
}

func joinRoutePattern(prefix, pattern string) string {
	if prefix == "" {
		if pattern == "" {
			return "/"
		}
		return pattern
	}

	if pattern == "" {
		return prefix
	}

	if pattern == "/" {
		return prefix + "/"
	}

	return prefix + "/" + strings.TrimLeft(pattern, "/")
}
