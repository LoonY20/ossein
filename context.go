package ossein

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
)

// defaultMaxBindBytes limits BindJSON request bodies when no application
// limit is configured.
const defaultMaxBindBytes int64 = 1 << 20

// Context exposes Ossein conveniences while keeping the underlying net/http types available.
type Context struct {
	Response http.ResponseWriter
	Request  *http.Request

	maxBindBytes int64
}

// NewContext creates an Ossein request context around standard library HTTP types.
func NewContext(w http.ResponseWriter, r *http.Request) *Context {
	return &Context{Response: w, Request: r, maxBindBytes: defaultMaxBindBytes}
}

// Context returns the request's standard context.Context.
func (c *Context) Context() context.Context {
	return c.Request.Context()
}

// RequestID returns the request ID assigned by Ossein.
func (c *Context) RequestID() string {
	return RequestIDFromContext(c.Request.Context())
}

// Logger returns the request-scoped slog logger.
// The logger includes request_id, method, and path attributes.
func (c *Context) Logger() *slog.Logger {
	return LoggerFromContext(c.Request.Context())
}

// Param returns a path parameter captured by the standard library ServeMux.
func (c *Context) Param(name string) string {
	return c.Request.PathValue(name)
}

// JSON writes a JSON response.
func (c *Context) JSON(status int, value any) error {
	return JSON(c.Response, status, value)
}

// NoContent writes a status code without a response body.
func (c *Context) NoContent(status int) error {
	c.Response.WriteHeader(status)
	return nil
}

// BindJSON decodes a JSON request body into target.
// A non-empty Content-Type must be application/json or use a +json suffix.
// The body is limited to the application's WithMaxBindBytes setting (1 MiB by
// default) and unknown JSON fields are rejected. If target implements
// Validatable, validation runs automatically after a successful decode.
func (c *Context) BindJSON(target any) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	if err := c.checkJSONContentType(); err != nil {
		return err
	}

	limit := c.maxBindBytes
	if limit <= 0 {
		limit = defaultMaxBindBytes
	}
	body := http.MaxBytesReader(c.Response, c.Request.Body, limit)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return bindJSONError(err, "Request body contains invalid JSON")
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return bindJSONError(err, "Request body must contain a single JSON value")
	}

	if validatable, ok := target.(Validatable); ok {
		if err := validatable.Validate(); err != nil {
			return err
		}
	}

	return nil
}

func (c *Context) checkJSONContentType() error {
	contentType := c.Request.Header.Get("Content-Type")
	if contentType == "" {
		return nil
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return NewHTTPError(
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"Request Content-Type must be application/json",
		).WithCause(err)
	}
	return nil
}

func bindJSONError(err error, invalidJSONMessage string) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return NewHTTPError(
			http.StatusRequestEntityTooLarge,
			"request_too_large",
			"Request body is too large",
		).WithCause(err)
	}
	return BadRequest("invalid_json", invalidJSONMessage).WithCause(err)
}
