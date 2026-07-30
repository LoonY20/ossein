package ossein

import (
	"errors"
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

type errorEnvelope struct {
	Error errorResponse `json:"error"`
}

type errorResponse struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Fields  map[string][]string `json:"fields,omitempty"`
}

func (a *App) defaultErrorHandler(ctx *Context, err error) {
	status := http.StatusInternalServerError
	response := errorResponse{
		Code:    "internal_error",
		Message: "Internal Server Error",
	}
	unexpected := true

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		status = http.StatusUnprocessableEntity
		response = errorResponse{
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
		ctx.Logger().Error("unhandled request error", "error", err)
	}

	if writer, ok := ResponseWriterFrom(ctx.Response); ok && writer.Written() {
		ctx.Logger().Error(
			"response already committed; skipping error response",
			"error", err,
			"status", writer.Status(),
		)
		return
	}

	_ = ctx.JSON(status, errorEnvelope{Error: response})
}
