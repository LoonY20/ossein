package ossein

import (
	"bytes"
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

	// body caches the request body once it has been read, so raw access and
	// BindJSON compose instead of competing for a single-use stream.
	body     []byte
	bodyRead bool
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

// Body returns the raw request body.
//
// It exists for payloads that must be inspected as received, such as a webhook
// whose HMAC signature covers the exact bytes: re-encoding a decoded struct
// would not reproduce them. The read is limited to the application's
// WithMaxBindBytes setting and reports a 413 beyond it, exactly as BindJSON
// does, so the limit stays in one place.
//
// The body is read once and cached, so Body may be called repeatedly and
// BindJSON still works afterwards. The request body is left readable for
// standard library helpers such as ParseForm.
//
// Body does not check Content-Type, because a raw body may be anything.
func (c *Context) Body() ([]byte, error) {
	if c.bodyRead {
		return c.body, nil
	}
	if c.Request == nil || c.Request.Body == nil {
		c.bodyRead = true
		return nil, nil
	}

	limited := http.MaxBytesReader(c.Response, c.Request.Body, c.bindLimit())
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, bindJSONError(err, "Request body could not be read")
	}

	c.body = raw
	c.bodyRead = true
	// Hand the bytes back to net/http so ParseForm and similar helpers still see
	// a readable body.
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}

// bindLimit returns the configured body limit, falling back to the default.
func (c *Context) bindLimit() int64 {
	if c.maxBindBytes > 0 {
		return c.maxBindBytes
	}
	return defaultMaxBindBytes
}

// BindJSON decodes a JSON request body into target.
// A non-empty Content-Type must be application/json or use a +json suffix.
// The body is limited to the application's WithMaxBindBytes setting (1 MiB by
// default) and unknown JSON fields are rejected. If target implements
// Validatable, validation runs automatically after a successful decode.
//
// BindJSON reads through Body, so it may be called after the raw bytes have
// already been taken.
func (c *Context) BindJSON(target any) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	if err := c.checkJSONContentType(); err != nil {
		return err
	}

	raw, err := c.Body()
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
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
