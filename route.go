package ossein

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Route describes a registered HTTP route.
type Route struct {
	app     *App
	Method  string
	Pattern string
	Name    string

	handler http.Handler
	group   *Router
}

// Named assigns a unique name to a route and returns it for fluent setup.
func (r *Route) Named(name string) *Route {
	if r == nil || r.app == nil {
		return r
	}
	name = strings.TrimSpace(name)
	if name == "" {
		panic("ossein: route name cannot be empty")
	}

	r.app.routesMu.Lock()
	defer r.app.routesMu.Unlock()
	if existing, ok := r.app.namedRoutes[name]; ok && existing != r {
		panic("ossein: route name " + name + " is already registered")
	}
	if r.Name != "" {
		delete(r.app.namedRoutes, r.Name)
	}
	r.Name = name
	r.app.namedRoutes[name] = r
	return r
}

// Routes returns a stable snapshot of registered routes in declaration order.
func (a *App) Routes() []Route {
	a.routesMu.RLock()
	defer a.routesMu.RUnlock()
	routes := make([]Route, len(a.routes))
	for i, route := range a.routes {
		routes[i] = *route
		routes[i].app = nil
		routes[i].handler = nil
		routes[i].group = nil
	}
	return routes
}

// NamedRoute returns a copy of the route registered with name.
func (a *App) NamedRoute(name string) (Route, bool) {
	a.routesMu.RLock()
	defer a.routesMu.RUnlock()
	route, ok := a.namedRoutes[name]
	if !ok {
		return Route{}, false
	}
	result := *route
	result.app = nil
	result.handler = nil
	result.group = nil
	return result, true
}

// URL builds a path for a named route. Parameters replace ServeMux wildcards.
func (a *App) URL(name string, params map[string]string) (string, error) {
	route, ok := a.NamedRoute(name)
	if !ok {
		return "", fmt.Errorf("ossein: named route %q is not registered", name)
	}
	path := route.Pattern
	for {
		start := strings.IndexByte(path, '{')
		if start < 0 {
			break
		}
		endOffset := strings.IndexByte(path[start:], '}')
		if endOffset < 0 {
			return "", errors.New("ossein: invalid route pattern")
		}
		end := start + endOffset
		token := path[start+1 : end]
		key := strings.TrimSuffix(token, "...")
		value, exists := params[key]
		if !exists {
			return "", fmt.Errorf("ossein: missing parameter %q for route %q", key, name)
		}
		escaped := url.PathEscape(value)
		if strings.HasSuffix(token, "...") {
			parts := strings.Split(value, "/")
			for i := range parts {
				parts[i] = url.PathEscape(parts[i])
			}
			escaped = strings.Join(parts, "/")
		}
		path = path[:start] + escaped + path[end+1:]
	}
	return path, nil
}

// SortedRoutes returns routes sorted by pattern and method for deterministic output.
func (a *App) SortedRoutes() []Route {
	routes := a.Routes()
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Pattern == routes[j].Pattern {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Pattern < routes[j].Pattern
	})
	return routes
}
