package ossein

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type requestIDContextKey struct{}
type loggerContextKey struct{}
type requestStateContextKey struct{}

// requestState carries the application settings that plain net/http helpers such
// as WriteError need, since they receive only an http.ResponseWriter and a
// *http.Request.
//
// rendering marks that the application's error handler is already on the stack,
// which stops a handler that delegates back to WriteError from recursing.
type requestState struct {
	errorHandler ErrorHandler
	maxBindBytes int64
	rendering    bool
}

func (a *App) requestContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get(a.requestIDHeader))
		if requestID == "" || len(requestID) > 128 {
			requestID = a.requestIDGenerator()
		}

		w.Header().Set(a.requestIDHeader, requestID)

		logger := a.logger.With(
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
		)

		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		ctx = context.WithValue(ctx, loggerContextKey{}, logger)
		// Carrying the handler and the bind limit lets WriteError render through
		// the application's error contract from plain net/http middleware, which
		// has no *Context and no reference to the App.
		ctx = context.WithValue(ctx, requestStateContextKey{}, &requestState{
			errorHandler: a.errorHandler,
			maxBindBytes: a.maxBindBytes,
		})

		next.ServeHTTP(NewResponseWriter(w), r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the Ossein request ID stored in a standard context.Context.
// It is useful from native net/http handlers and lower-level application code.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// ContextWithLogger returns a context carrying logger, so LoggerFromContext finds it.
//
// It exists for work that is not a request: a queue worker, a scheduled task, a
// command. Passing nil returns ctx unchanged.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// LoggerFromContext returns the request-scoped logger when available.
// It falls back to slog.Default for contexts not created by Ossein.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
			return logger
		}
	}
	return slog.Default()
}

// requestStateFromContext returns the application settings recorded for this
// request, or nil when the request was not served by an Ossein application.
func requestStateFromContext(ctx context.Context) *requestState {
	state, _ := ctx.Value(requestStateContextKey{}).(*requestState)
	return state
}

func defaultRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}

	return fmt.Sprintf("%x", time.Now().UnixNano())
}
