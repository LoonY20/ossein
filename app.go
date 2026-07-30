package ossein

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Middleware wraps an HTTP handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// HandlerFunc is an Ossein handler. Returning an error delegates response handling
// to the application's ErrorHandler.
type HandlerFunc func(*Context) error

// ErrorHandler converts handler errors into HTTP responses.
type ErrorHandler func(*Context, error)

// Option configures an App during construction.
type Option func(*App)

// WithLogger configures the application's base slog logger.
func WithLogger(logger *slog.Logger) Option {
	return func(app *App) {
		if logger != nil {
			app.logger = logger
		}
	}
}

// WithRequestIDHeader changes the HTTP header used for request IDs.
func WithRequestIDHeader(header string) Option {
	return func(app *App) {
		if header != "" {
			app.requestIDHeader = header
		}
	}
}

// WithRequestIDGenerator replaces the request ID generator.
// This is useful when applications need a specific ID format.
func WithRequestIDGenerator(generator func() string) Option {
	return func(app *App) {
		if generator != nil {
			app.requestIDGenerator = generator
		}
	}
}

// WithShutdownTimeout configures the timeout used for graceful HTTP shutdown
// and lifecycle stop hooks.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(app *App) {
		if timeout > 0 {
			app.shutdownTimeout = timeout
		}
	}
}

// WithMaxBindBytes limits how many request body bytes Context.BindJSON reads.
// The default limit is 1 MiB.
func WithMaxBindBytes(limit int64) Option {
	return func(app *App) {
		if limit > 0 {
			app.maxBindBytes = limit
		}
	}
}

// App is the root Ossein application.
type App struct {
	mux                *http.ServeMux
	middleware         []Middleware
	errorHandler       ErrorHandler
	logger             *slog.Logger
	requestIDHeader    string
	requestIDGenerator func() string
	shutdownTimeout    time.Duration
	maxBindBytes       int64
	startHooks         []LifecycleHook
	stopHooks          []LifecycleHook
	services           *Container
	routesMu           sync.RWMutex
	routes             []*Route
	namedRoutes        map[string]*Route
	handlerOnce        sync.Once
	handler            http.Handler
	buildErr           error
	frozen             atomic.Bool
}

// New creates a new Ossein application backed by the standard library ServeMux.
func New(options ...Option) *App {
	app := &App{
		mux:                http.NewServeMux(),
		logger:             slog.Default(),
		requestIDHeader:    "X-Request-ID",
		requestIDGenerator: defaultRequestID,
		shutdownTimeout:    10 * time.Second,
		maxBindBytes:       defaultMaxBindBytes,
		services:           NewContainer(),
		namedRoutes:        make(map[string]*Route),
	}
	app.errorHandler = app.defaultErrorHandler

	for _, option := range options {
		if option != nil {
			option(app)
		}
	}

	return app
}

// Logger returns the application's base structured logger.
func (a *App) Logger() *slog.Logger {
	return a.logger
}

// Use registers application-wide middleware.
// Middleware executes in the same order it is registered.
// Use panics once the application has started serving requests, because the
// handler chain is built exactly once.
func (a *App) Use(middleware ...Middleware) {
	if a.frozen.Load() {
		panic("ossein: middleware must be registered before the application starts serving requests")
	}
	a.middleware = append(a.middleware, middleware...)
}

// SetErrorHandler replaces the application error handler.
// Passing nil restores Ossein's default JSON error handler.
func (a *App) SetErrorHandler(handler ErrorHandler) {
	if handler == nil {
		a.errorHandler = a.defaultErrorHandler
		return
	}

	a.errorHandler = handler
}

// Handle registers an Ossein handler for an HTTP method and route pattern.
// Route patterns use the Go standard library ServeMux syntax.
func (a *App) Handle(method, pattern string, handler HandlerFunc) *Route {
	return a.register(method, pattern, handler, nil)
}

// HandleHTTP registers an ordinary http.Handler without wrapping it in an Ossein Context.
// Application-wide middleware still applies.
func (a *App) HandleHTTP(method, pattern string, handler http.Handler) *Route {
	return a.registerHTTP(method, pattern, handler, nil)
}

// HandleHTTPFunc registers an ordinary http.HandlerFunc.
func (a *App) HandleHTTPFunc(method, pattern string, handler http.HandlerFunc) *Route {
	return a.HandleHTTP(method, pattern, handler)
}

// Get registers a GET route.
func (a *App) Get(pattern string, handler HandlerFunc) *Route {
	return a.Handle(http.MethodGet, pattern, handler)
}

// Post registers a POST route.
func (a *App) Post(pattern string, handler HandlerFunc) *Route {
	return a.Handle(http.MethodPost, pattern, handler)
}

// Put registers a PUT route.
func (a *App) Put(pattern string, handler HandlerFunc) *Route {
	return a.Handle(http.MethodPut, pattern, handler)
}

// Patch registers a PATCH route.
func (a *App) Patch(pattern string, handler HandlerFunc) *Route {
	return a.Handle(http.MethodPatch, pattern, handler)
}

// Delete registers a DELETE route.
func (a *App) Delete(pattern string, handler HandlerFunc) *Route {
	return a.Handle(http.MethodDelete, pattern, handler)
}

func (a *App) register(method, pattern string, handler HandlerFunc, group *Router) *Route {
	return a.registerHTTP(method, pattern, a.osseinHandler(handler), group)
}

// registerHTTP records a route definition. Routes enter the ServeMux when the
// application handler is built, so pattern conflicts surface as Start errors
// and group middleware can be registered anywhere inside a Group block.
func (a *App) registerHTTP(method, pattern string, handler http.Handler, group *Router) *Route {
	if a.frozen.Load() {
		panic("ossein: routes must be registered before the application starts serving requests")
	}
	route := &Route{app: a, Method: method, Pattern: pattern, handler: handler, group: group}
	a.routesMu.Lock()
	a.routes = append(a.routes, route)
	a.routesMu.Unlock()
	return route
}

// buildHandler registers routes on the ServeMux and assembles the middleware
// chain exactly once.
func (a *App) buildHandler() error {
	a.handlerOnce.Do(func() {
		a.frozen.Store(true)
		if err := a.registerRoutes(); err != nil {
			a.buildErr = err
			return
		}
		a.handler = a.requestContextMiddleware(applyMiddleware(a.mux, a.middleware))
	})
	return a.buildErr
}

func (a *App) registerRoutes() error {
	a.routesMu.RLock()
	routes := append([]*Route(nil), a.routes...)
	a.routesMu.RUnlock()

	for _, route := range routes {
		handler := applyMiddleware(route.handler, route.group.chain())
		if err := handleMuxPattern(a.mux, route.Method+" "+route.Pattern, handler); err != nil {
			return err
		}
	}
	return nil
}

// handleMuxPattern converts the standard library's registration panics, such
// as conflicting patterns, into errors.
func handleMuxPattern(mux *http.ServeMux, pattern string, handler http.Handler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("ossein: register route %q: %v", pattern, recovered)
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

func (a *App) osseinHandler(handler HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := NewContext(w, r)
		ctx.maxBindBytes = a.maxBindBytes
		if err := handler(ctx); err != nil {
			a.errorHandler(ctx, err)
		}
	})
}

func applyMiddleware(handler http.Handler, middleware []Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}

	return handler
}

// Handler returns the final HTTP handler including Ossein request context and
// application middleware. The chain is built exactly once; routes and
// middleware registered afterwards panic instead of being silently ignored.
// Handler panics when route registration fails; Start and Run report the same
// problem as an error first.
func (a *App) Handler() http.Handler {
	if err := a.buildHandler(); err != nil {
		panic(err)
	}
	return a.handler
}

// ServeHTTP lets App satisfy http.Handler.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.Handler().ServeHTTP(w, r)
}

// Run starts an HTTP server and blocks until it stops.
func (a *App) Run(address string) error {
	if err := a.Start(context.Background()); err != nil {
		return err
	}

	server := &http.Server{
		Addr:    address,
		Handler: a.Handler(),
	}

	serverErr := server.ListenAndServe()
	if errors.Is(serverErr, http.ErrServerClosed) {
		serverErr = nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	stopErr := a.Stop(stopCtx)

	return errors.Join(serverErr, stopErr)
}

// RunContext starts an HTTP server and gracefully shuts it down when ctx is cancelled.
func (a *App) RunContext(ctx context.Context, address string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := a.Start(ctx); err != nil {
		return err
	}

	server := &http.Server{
		Addr:    address,
		Handler: a.Handler(),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	var serverErr error

	select {
	case serverErr = <-errCh:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			serverErr = shutdownErr
		} else {
			serverErr = <-errCh
		}
	}

	if errors.Is(serverErr, http.ErrServerClosed) {
		serverErr = nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	stopErr := a.Stop(stopCtx)

	return errors.Join(serverErr, stopErr)
}
