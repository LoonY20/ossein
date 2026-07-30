package ossein

import (
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
// Outside a request served by an Ossein application, WriteError falls back to
// the default envelope. A nil error is reported as an internal error rather than
// producing a blank response.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		err = errors.New("ossein: WriteError called with a nil error")
	}

	ctx := NewContext(w, r)
	if handler := errorHandlerFromContext(r.Context()); handler != nil {
		handler(ctx, err)
		return
	}
	defaultErrorResponse(ctx, err, LoggerFromContext(r.Context()))
}

func (a *App) defaultErrorHandler(ctx *Context, err error) {
	defaultErrorResponse(ctx, err, ctx.Logger())
}

// defaultErrorResponse renders Ossein's standard error document. It is shared by
// the application's default handler and by WriteError outside a request served
// by an application.
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
