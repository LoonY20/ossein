package ossein

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// HTTPError represents an expected HTTP failure.
type HTTPError struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return http.StatusText(e.Status)
}

// Unwrap exposes the underlying cause for errors.Is/errors.As.
func (e *HTTPError) Unwrap() error {
	return e.Cause
}

// WithCause attaches an internal cause while preserving the public HTTP response.
func (e *HTTPError) WithCause(err error) *HTTPError {
	e.Cause = err
	return e
}

// NewHTTPError creates an expected HTTP error.
func NewHTTPError(status int, code, message string) *HTTPError {
	return &HTTPError{Status: status, Code: code, Message: message}
}

// BadRequest creates a 400 HTTP error.
func BadRequest(code, message string) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, code, message)
}

// Unauthorized creates a 401 HTTP error.
func Unauthorized(code, message string) *HTTPError {
	return NewHTTPError(http.StatusUnauthorized, code, message)
}

// Forbidden creates a 403 HTTP error.
func Forbidden(code, message string) *HTTPError {
	return NewHTTPError(http.StatusForbidden, code, message)
}

// NotFound creates a 404 HTTP error.
func NotFound(code, message string) *HTTPError {
	return NewHTTPError(http.StatusNotFound, code, message)
}

// Conflict creates a 409 HTTP error.
func Conflict(code, message string) *HTTPError {
	return NewHTTPError(http.StatusConflict, code, message)
}

// ErrorEnvelope is the JSON document Ossein's default error handler writes.
// It is exported so applications can decode it in tests and clients, and reuse
// the shape from a custom ErrorHandler.
type ErrorEnvelope struct {
	Error ErrorResponse `json:"error"`
}

// ErrorResponse describes a single failure. Fields is populated for validation
// errors and omitted otherwise.
type ErrorResponse struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Fields  map[string][]string `json:"fields,omitempty"`
}

// DefaultErrorHandler renders Ossein's standard error document: a
// *ValidationError as 422 with its fields, an *HTTPError as its own status, and
// anything else as a logged 500 that does not leak internal detail.
//
// It is the delegation target for a custom ErrorHandler that only wants to own
// some errors:
//
//	app.SetErrorHandler(func(c *ossein.Context, err error) {
//		var domain *DomainError
//		if errors.As(err, &domain) {
//			_ = c.JSON(domain.Status, domain.Payload())
//			return
//		}
//		ossein.DefaultErrorHandler(c, err)
//	})
//
// Like the default handler, it refuses to write over a response that is already
// committed.
func DefaultErrorHandler(ctx *Context, err error) {
	defaultErrorResponse(ctx, err, ctx.Logger())
}

// WriteError renders err through the application's ErrorHandler from ordinary
// net/http code, so middleware answers with the same error contract as handlers.
//
// Middleware is plain func(http.Handler) http.Handler and has no *Context, which
// would otherwise leave it hand-rolling an error body that drifts from the
// application's. The handler is carried on the request context, so no reference
// to the App is needed:
//
//	func RequireAPIKey(keys map[string]string) ossein.Middleware {
//		return func(next http.Handler) http.Handler {
//			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//				if _, ok := keys[r.Header.Get("X-API-Key")]; !ok {
//					ossein.WriteError(w, r, ossein.Unauthorized("invalid_api_key", "API key is not valid"))
//					return
//				}
//				next.ServeHTTP(w, r)
//			})
//		}
//	}
//
// WriteError applies to middleware registered through App.Use or Router.Use.
// Middleware composed around App.Handler(), such as a wrapper installed on the
// http.Server, runs before the request context exists; there WriteError falls
// back to the default document and records the reason at debug level.
//
// A custom ErrorHandler may call WriteError to hand an error back: the second
// entry renders the default document instead of recursing. DefaultErrorHandler
// expresses that intent directly and is preferred.
//
// The committed-response guard reads state recorded on *ossein.ResponseWriter.
// Ossein installs one per request, so the guard holds inside an application.
// Elsewhere, wrap the writer once with NewResponseWriter and reuse it if the
// guard matters.
//
// A nil error is reported as an internal error rather than producing a blank
// response. Both w and r must be non-nil.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		err = errors.New("ossein: WriteError called with a nil error")
	}

	requestCtx := r.Context()
	state := requestStateFromContext(requestCtx)

	if state == nil {
		LoggerFromContext(requestCtx).Debug(
			"ossein: rendering an error outside an application request; " +
				"the application error handler is not reachable here",
		)
		defaultErrorResponse(NewContext(w, r), err, LoggerFromContext(requestCtx))
		return
	}

	// Re-entering means the application's handler delegated back here. Render
	// the default document rather than calling it again, which would recurse
	// until the goroutine stack is exhausted.
	if state.rendering || state.errorHandler == nil {
		ctx := NewContext(w, r)
		ctx.maxBindBytes = state.maxBindBytes
		defaultErrorResponse(ctx, err, LoggerFromContext(requestCtx))
		return
	}

	nested := *state
	nested.rendering = true
	marked := r.WithContext(context.WithValue(requestCtx, requestStateContextKey{}, &nested))

	ctx := NewContext(w, marked)
	ctx.maxBindBytes = nested.maxBindBytes
	state.errorHandler(ctx, err)
}

// defaultErrorResponse renders Ossein's standard error document. It is shared by
// DefaultErrorHandler and by WriteError when no application handler is
// reachable.
func defaultErrorResponse(ctx *Context, err error, logger *slog.Logger) {
	status := http.StatusInternalServerError
	response := ErrorResponse{
		Code:    "internal_error",
		Message: "Internal Server Error",
	}
	unexpected := true

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		status = http.StatusUnprocessableEntity
		response = ErrorResponse{
			Code:    "validation_failed",
			Message: "The request data is invalid",
			Fields:  validationErr.Fields,
		}
		unexpected = false
	} else {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) {
			status = httpErr.Status
			response.Code = httpErr.Code
			response.Message = httpErr.Message
			unexpected = false
		}
	}

	if unexpected {
		logger.Error("unhandled request error", "error", err)
	}

	if writer, ok := ResponseWriterFrom(ctx.Response); ok && writer.Written() {
		logger.Error(
			"response already committed; skipping error response",
			"error", err,
			"status", writer.Status(),
		)
		return
	}

	_ = ctx.JSON(status, ErrorEnvelope{Error: response})
}
