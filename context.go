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
	// BindJSON compose instead of competing for a single-use stream. bodyErr is
	// cached with it: a failed read leaves the stream partially drained, so
	// retrying would return a fragment rather than the body.
	body     []byte
	bodyErr  error
	bodyRead bool

	// query caches the parsed query string, or the failure to parse it.
	query    *Values
	queryErr error
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
// BindJSON still works afterwards. A failed read is cached too: the stream is
// partially drained by then, so a retry would return a fragment rather than the
// body. That also keeps the limit bounding the request rather than each call.
//
// The request body is left readable for standard library helpers, so Body may be
// followed by ParseForm. The reverse order does not work: a helper or middleware
// that consumes the body without restoring it leaves Body nothing to read.
//
// Body does not check Content-Type, because a raw body may be anything. The
// returned slice backs both the cache and the reinstated request body, so it
// must not be modified.
func (c *Context) Body() ([]byte, error) {
	if c.bodyRead {
		return c.body, c.bodyErr
	}
	if c.Request == nil || c.Request.Body == nil {
		c.bodyRead = true
		return nil, nil
	}

	// MaxBytesReader takes the writer to signal that the connection should be
	// closed on overflow, which it does through an unexported method only
	// *http.response implements. Ossein always wraps the writer, so that signal
	// is inert here; the standard library still closes an early-closed body.
	limited := http.MaxBytesReader(c.Response, c.Request.Body, c.bindLimit())
	raw, err := io.ReadAll(limited)
	if err != nil {
		c.bodyRead = true
		c.bodyErr = readBodyError(err)
		return nil, c.bodyErr
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

// jsonDecoder reads from the cached body when it has already been taken, and
// streams otherwise so an invalid body is abandoned at its first syntax error.
func (c *Context) jsonDecoder() (*json.Decoder, error) {
	if c.bodyRead {
		if c.bodyErr != nil {
			return nil, c.bodyErr
		}
		return json.NewDecoder(bytes.NewReader(c.body)), nil
	}
	if c.Request == nil || c.Request.Body == nil {
		return json.NewDecoder(bytes.NewReader(nil)), nil
	}
	return json.NewDecoder(
		http.MaxBytesReader(c.Response, c.Request.Body, c.bindLimit()),
	), nil
}

// BindJSON decodes a JSON request body into target.
// A non-empty Content-Type must be application/json or use a +json suffix.
// The body is limited to the application's WithMaxBindBytes setting (1 MiB by
// default) and unknown JSON fields are rejected. If target implements
// Validatable, validation runs automatically after a successful decode.
//
// BindJSON decodes the cached body when Body has already been called, so the two
// compose. Otherwise it streams, which stops at the first syntax error instead of
// reading a whole invalid body up to the limit.
func (c *Context) BindJSON(target any) error {
	if target == nil {
		return BadRequest("invalid_request", "Request target cannot be nil")
	}

	if err := c.checkJSONContentType(); err != nil {
		return err
	}

	decoder, err := c.jsonDecoder()
	if err != nil {
		return err
	}
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

// readBodyError describes a failed body read without blaming JSON, since Body
// serves any payload shape.
func readBodyError(err error) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return NewHTTPError(
			http.StatusRequestEntityTooLarge,
			"request_too_large",
			"Request body is too large",
		).WithCause(err)
	}
	return BadRequest("invalid_request_body", "Request body could not be read").
		WithCause(err)
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
