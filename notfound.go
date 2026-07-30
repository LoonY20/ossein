package ossein

import "net/http"

// plainTextContentType is what the standard library announces for the bodies it
// writes through http.Error.
const plainTextContentType = "text/plain; charset=utf-8"

// SetNotFoundHandler replaces the handler used when no route matches the
// request path. Passing nil restores Ossein's default, which reports a 404
// through the application's ErrorHandler.
//
// The handler is an ordinary HandlerFunc: returning an error renders it through
// the ErrorHandler exactly as it would from a route. A handler that returns
// without writing still produces a 404, so a miss can never answer 200.
//
// Like route and middleware registration, this must happen before the
// application starts serving requests.
func (a *App) SetNotFoundHandler(handler HandlerFunc) {
	if a.frozen.Load() {
		panic("ossein: the not-found handler must be set before the application starts serving requests")
	}
	if handler == nil {
		a.notFoundHandler = defaultNotFoundHandler
		return
	}
	a.notFoundHandler = handler
}

// SetMethodNotAllowedHandler replaces the handler used when a route matches the
// request path but not its method. Passing nil restores Ossein's default, which
// reports a 405 through the application's ErrorHandler.
//
// Ossein preserves the Allow header computed by the standard library ServeMux,
// so a replacement can read or extend it.
//
// Like route and middleware registration, this must happen before the
// application starts serving requests.
func (a *App) SetMethodNotAllowedHandler(handler HandlerFunc) {
	if a.frozen.Load() {
		panic("ossein: the method-not-allowed handler must be set before the application starts serving requests")
	}
	if handler == nil {
		a.methodNotAllowedHandler = defaultMethodNotAllowedHandler
		return
	}
	a.methodNotAllowedHandler = handler
}

func defaultNotFoundHandler(*Context) error {
	return NotFound("not_found", "The requested resource does not exist")
}

func defaultMethodNotAllowedHandler(*Context) error {
	return NewHTTPError(
		http.StatusMethodNotAllowed,
		"method_not_allowed",
		"The request method is not supported for this resource",
	)
}

// dispatch serves the ServeMux while taking over the two responses the mux
// writes on its own behalf.
//
// The mux performs the real dispatch, so route wildcards, subtree redirects, and
// path cleaning all keep working. Only the response is watched, and only until
// one of the application's own routes takes the request: markRouted records
// that, which is what separates a routing miss from any response an application
// handler produces.
func (a *App) dispatch() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		watcher := &missWatcher{ResponseWriter: w}
		a.mux.ServeHTTP(watcher, r)
		if watcher.status == 0 {
			return
		}
		a.serveMiss(w, r, watcher.status)
	})
}

// markRouted flags that one of the application's routes handled the request.
//
// It wraps each registered route from the outside, so the writer it sees is the
// one dispatch installed. Recording the match here rather than reading
// http.Request.Pattern keeps the decision independent of handlers that delegate
// to another ServeMux with the same request, which would otherwise clear that
// field as a side effect.
func markRouted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if watcher, ok := w.(*missWatcher); ok {
			watcher.routed = true
		}
		next.ServeHTTP(w, r)
	})
}

// serveMiss renders a routing miss through the application's own handlers.
func (a *App) serveMiss(w http.ResponseWriter, r *http.Request, status int) {
	header := w.Header()
	// The standard library announced a plain-text body that was discarded. Only
	// its own value is removed, so a Content-Type set by application middleware
	// survives. Allow is kept deliberately: it is the mux's computation. Security
	// headers, including the nosniff the standard library adds, are never
	// removed.
	if header.Get("Content-Type") == plainTextContentType {
		header.Del("Content-Type")
	}

	handler := a.notFoundHandler
	if status == http.StatusMethodNotAllowed {
		handler = a.methodNotAllowedHandler
	}

	ctx := NewContext(w, r)
	ctx.maxBindBytes = a.maxBindBytes
	if err := handler(ctx); err != nil {
		a.errorHandler(ctx, err)
		return
	}

	// The mux's response was suppressed, so a handler that wrote nothing would
	// leave an implicit 200 behind. Commit the status it replaced instead.
	if writer, ok := ResponseWriterFrom(w); ok && !writer.Written() {
		w.WriteHeader(status)
	}
}

// missWatcher suppresses the body ServeMux writes for an unmatched request and
// records which of the two responses it was. A zero status means the request was
// served normally.
//
// Unwrap keeps http.ResponseController and ResponseWriterFrom working through
// the wrapper, so flushing, hijacking, and committed-response tracking are
// unaffected.
type missWatcher struct {
	http.ResponseWriter
	routed bool
	status int
}

func (w *missWatcher) WriteHeader(status int) {
	if w.status == 0 && w.isRoutingMiss(status) {
		w.status = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *missWatcher) Write(content []byte) (int, error) {
	if w.status != 0 {
		return len(content), nil
	}
	return w.ResponseWriter.Write(content)
}

// isRoutingMiss reports whether ServeMux, rather than an application handler,
// produced this status.
func (w *missWatcher) isRoutingMiss(status int) bool {
	if w.routed {
		return false
	}
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

// Unwrap exposes the wrapped writer to http.ResponseController and
// ResponseWriterFrom.
func (w *missWatcher) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
