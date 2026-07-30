package ossein

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Default timeouts applied to the server Run and RunContext build.
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
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
	mux                     *http.ServeMux
	middleware              []Middleware
	errorHandler            ErrorHandler
	logger                  *slog.Logger
	requestIDHeader         string
	requestIDGenerator      func() string
	shutdownTimeout         time.Duration
	maxBindBytes            int64
	startHooks              []LifecycleHook
	stopHooks               []LifecycleHook
	services                *Container
	notFoundHandler         HandlerFunc
	methodNotAllowedHandler HandlerFunc
	routesMu                sync.RWMutex
	routes                  []*Route
	namedRoutes             map[string]*Route
	handlerOnce             sync.Once
	handler                 http.Handler
	buildErr                error
	frozen                  atomic.Bool
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
	app.errorHandler = DefaultErrorHandler
	app.notFoundHandler = defaultNotFoundHandler
	app.methodNotAllowedHandler = defaultMethodNotAllowedHandler

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
//
// The request path reads this handler, so like route and middleware
// registration it must be set before the application starts serving requests.
func (a *App) SetErrorHandler(handler ErrorHandler) {
	if a.frozen.Load() {
		panic("ossein: the error handler must be set before the application starts serving requests")
	}
	if handler == nil {
		a.errorHandler = DefaultErrorHandler
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
		a.handler = a.requestContextMiddleware(applyMiddleware(a.dispatch(), a.middleware))
	})
	return a.buildErr
}

func (a *App) registerRoutes() error {
	a.routesMu.RLock()
	routes := append([]*Route(nil), a.routes...)
	a.routesMu.RUnlock()

	for _, route := range routes {
		handler := markRouted(applyMiddleware(route.handler, route.group.chain()))
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
//
// The server is built by Ossein with conservative timeouts; see Serve to supply
// a fully configured *http.Server instead.
func (a *App) Run(address string) error {
	return a.Serve(context.Background(), a.newDefaultServer(address))
}

// RunContext starts an HTTP server and gracefully shuts it down when ctx is cancelled.
func (a *App) RunContext(ctx context.Context, address string) error {
	return a.Serve(ctx, a.newDefaultServer(address))
}

// Serve runs a caller-owned *http.Server through Ossein's application
// lifecycle: it validates services and runs start hooks, serves until the
// server stops or ctx is cancelled, shuts the server down gracefully, and then
// runs stop hooks.
//
// Every server field is left as the caller set it, so timeouts, MaxHeaderBytes,
// BaseContext, ConnState, and ErrorLog are all available. Only Handler is
// filled in, and only when nil; Serve then owns it for the lifetime of the
// server. Installing it after start hooks keeps route conflicts reportable as
// errors instead of panics.
//
// Serve always speaks plain HTTP, matching http.Server.ListenAndServe: a
// TLSConfig alone does not select HTTPS. Use ServeTLS, or wrap a listener with
// tls.NewListener and pass it to ServeListener.
//
// A single server may be served once. Serving an already-closed server reports
// http.ErrServerClosed rather than reporting success for a run that never
// accepted a connection.
//
// Serve is the escape hatch for production servers; Run and RunContext are
// shorthand for the common case.
func (a *App) Serve(ctx context.Context, server *http.Server) error {
	if server == nil {
		return errors.New("ossein: server cannot be nil")
	}
	return a.serve(ctx, server, server.ListenAndServe)
}

// ServeTLS is Serve over HTTPS. Certificates come from certFile and keyFile, or
// from server.TLSConfig when both paths are empty, exactly as
// http.Server.ListenAndServeTLS behaves.
//
// A missing certificate is reported before the lifecycle starts, because the
// standard library would otherwise fail with `open :` and no mention of TLS.
func (a *App) ServeTLS(ctx context.Context, server *http.Server, certFile, keyFile string) error {
	if server == nil {
		return errors.New("ossein: server cannot be nil")
	}
	if certFile == "" && keyFile == "" && !hasCertificateSource(server.TLSConfig) {
		return errors.New(
			"ossein: ServeTLS needs a certificate: pass certFile and keyFile, " +
				"or set Certificates, GetCertificate, or GetConfigForClient on server.TLSConfig",
		)
	}
	return a.serve(ctx, server, func() error {
		return server.ListenAndServeTLS(certFile, keyFile)
	})
}

// hasCertificateSource reports whether a TLS config can produce a certificate
// without reading files.
func hasCertificateSource(config *tls.Config) bool {
	if config == nil {
		return false
	}
	return len(config.Certificates) > 0 ||
		config.GetCertificate != nil ||
		config.GetConfigForClient != nil
}

// ServeListener is Serve on an already-bound net.Listener, for socket
// activation, an ephemeral test port, or a listener wrapped with tls.NewListener.
// server.Addr is ignored.
func (a *App) ServeListener(
	ctx context.Context,
	server *http.Server,
	listener net.Listener,
) error {
	if server == nil {
		return errors.New("ossein: server cannot be nil")
	}
	if listener == nil {
		return errors.New("ossein: listener cannot be nil")
	}
	return a.serve(ctx, server, func() error { return server.Serve(listener) })
}

// serve is the shared lifecycle around every server entry point. listen blocks
// until the server stops.
func (a *App) serve(ctx context.Context, server *http.Server, listen func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// A cancelled context would otherwise run both hook sets and report a clean
	// run for a server that never listened.
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := a.Start(ctx); err != nil {
		return err
	}
	if server.Handler == nil {
		server.Handler = a.Handler()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- listen()
	}()

	var serverErr error
	select {
	case serverErr = <-errCh:
		// The server stopped on its own. ErrServerClosed here means something
		// other than this call closed it, so it is reported rather than
		// mistaken for a graceful shutdown.
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()

		// Shutdown closes listeners before waiting on connections, so listen
		// has returned even when the wait timed out.
		serverErr = <-errCh
		if errors.Is(serverErr, http.ErrServerClosed) {
			serverErr = nil
		}
		if shutdownErr != nil {
			serverErr = errors.Join(
				fmt.Errorf("ossein: graceful shutdown: %w", shutdownErr),
				serverErr,
			)
		}
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	stopErr := a.Stop(stopCtx)

	return errors.Join(serverErr, stopErr)
}

// newDefaultServer builds the server used by Run and RunContext.
//
// ReadHeaderTimeout and IdleTimeout bound connections that never send a
// complete request header and connections kept alive without further requests,
// which is what makes an unattended server safe to expose. They do not bound a
// slowly delivered request body; an application that needs that should set
// ReadTimeout through Serve.
//
// ReadTimeout and WriteTimeout are deliberately left unset: WriteTimeout is an
// absolute deadline for the whole response, so it would cut off server-sent
// events and long downloads, and ReadTimeout would cap large uploads.
//
// Handler stays nil so Serve installs it after Start, keeping route conflicts
// reportable as errors.
func (a *App) newDefaultServer(address string) *http.Server {
	return &http.Server{
		Addr:              address,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}
}
