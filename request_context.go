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
type errorHandlerContextKey struct{}

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
		// Carrying the error handler lets WriteError render through the
		// application's error contract from plain net/http middleware, which has
		// no *Context and no reference to the App.
		ctx = context.WithValue(ctx, errorHandlerContextKey{}, a.errorHandler)

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

// errorHandlerFromContext returns the application's error handler when the
// request was served by an Ossein application.
func errorHandlerFromContext(ctx context.Context) ErrorHandler {
	if ctx == nil {
		return nil
	}
	handler, _ := ctx.Value(errorHandlerContextKey{}).(ErrorHandler)
	return handler
}

func defaultRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}

	return fmt.Sprintf("%x", time.Now().UnixNano())
}
