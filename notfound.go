package ossein

import "net/http"

// SetNotFoundHandler replaces the handler used when no route matches the
// request path. Passing nil restores Ossein's default, which reports a 404
// through the application's ErrorHandler.
//
// The handler is an ordinary HandlerFunc: returning an error renders it through
// the ErrorHandler exactly as it would from a route.
func (a *App) SetNotFoundHandler(handler HandlerFunc) {
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
func (a *App) SetMethodNotAllowedHandler(handler HandlerFunc) {
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
// The mux performs the real dispatch, so route wildcards, subtree redirects,
// and path cleaning all keep working. Only the response is watched: ServeMux
// leaves Request.Pattern empty exactly when no route matched, which separates a
// routing miss from a matched handler that answers 404 itself.
func (a *App) dispatch() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		watcher := &missWatcher{ResponseWriter: w, request: r}
		a.mux.ServeHTTP(watcher, r)
		if watcher.status == 0 {
			return
		}
		a.serveMiss(w, r, watcher.status)
	})
}

// serveMiss renders a routing miss through the application's own handlers.
func (a *App) serveMiss(w http.ResponseWriter, r *http.Request, status int) {
	// The standard library announced a plain-text body that was discarded.
	// Content-Type is dropped so the application's response describes itself;
	// Allow is deliberately kept, because it is the mux's own computation.
	header := w.Header()
	header.Del("Content-Type")
	header.Del("X-Content-Type-Options")

	handler := a.notFoundHandler
	if status == http.StatusMethodNotAllowed {
		handler = a.methodNotAllowedHandler
	}

	ctx := NewContext(w, r)
	ctx.maxBindBytes = a.maxBindBytes
	if err := handler(ctx); err != nil {
		a.errorHandler(ctx, err)
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
	request *http.Request
	status  int
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
// produced this status. ServeMux sets Request.Pattern before invoking a matched
// handler, so an empty pattern means nothing matched.
func (w *missWatcher) isRoutingMiss(status int) bool {
	if w.request.Pattern != "" {
		return false
	}
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

// Unwrap exposes the wrapped writer to http.ResponseController and
// ResponseWriterFrom.
func (w *missWatcher) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
