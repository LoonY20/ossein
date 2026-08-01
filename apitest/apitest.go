// Package apitest drives an Ossein application from a test.
//
// It exists because every application writes the same twenty lines: build a
// request, set the content type, set the auth header, serve it into a recorder,
// check the status, decode the body. That code is not interesting and it is
// where a test's own bugs hide — a status compared against the wrong recorder
// field, a decode whose error is ignored.
//
//	client := apitest.New(t, app).WithHeader("X-API-Key", key)
//
//	var link Link
//	client.PostJSON("/api/links", CreateLinkRequest{Target: target}).
//		AssertStatus(http.StatusCreated).
//		DecodeJSON(&link)
//
//	client.Get("/api/links/missing").AssertError(http.StatusNotFound, "link_not_found")
//
// It knows the framework's error envelope, which is the part a general HTTP
// testing library cannot do: AssertError and AssertFieldError read the document
// the error handler renders rather than matching substrings in a body.
//
// Nothing is hidden. Response.Result returns the *http.Response, Response.Body
// the bytes, and Client.Do takes a request the caller built however it likes.
package apitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	ossein "github.com/LoonY20/ossein"
)

// Client sends requests to a handler and reports failures through a test.
//
// A Client is immutable: WithHeader returns a new one, so a client with an
// authenticated identity can be derived from a base client without either
// affecting the other.
type Client struct {
	t       testing.TB
	handler http.Handler
	headers http.Header
}

// New returns a client that serves requests to handler.
//
// The handler is usually an *ossein.App, which is an http.Handler; anything else
// that serves HTTP works too, so middleware can be tested in isolation.
func New(t testing.TB, handler http.Handler) *Client {
	t.Helper()
	if handler == nil {
		t.Fatal("apitest: handler cannot be nil")
	}
	return &Client{t: t, handler: handler, headers: http.Header{}}
}

// WithHeader returns a client that sends header on every request.
//
// Repeating an API key or a tenant identifier on each call is the boilerplate
// this removes, and forgetting one is a test that passes for the wrong reason.
func (c *Client) WithHeader(name, value string) *Client {
	c.t.Helper()

	headers := http.Header{}
	maps.Copy(headers, c.headers)
	headers.Set(name, value)
	return &Client{t: c.t, handler: c.handler, headers: headers}
}

// Get sends a GET request.
func (c *Client) Get(path string) *Response {
	c.t.Helper()
	return c.send(http.MethodGet, path, nil, "")
}

// Delete sends a DELETE request.
func (c *Client) Delete(path string) *Response {
	c.t.Helper()
	return c.send(http.MethodDelete, path, nil, "")
}

// PostJSON sends a JSON POST request, encoding payload.
func (c *Client) PostJSON(path string, payload any) *Response {
	c.t.Helper()
	return c.sendJSON(http.MethodPost, path, payload)
}

// PutJSON sends a JSON PUT request, encoding payload.
func (c *Client) PutJSON(path string, payload any) *Response {
	c.t.Helper()
	return c.sendJSON(http.MethodPut, path, payload)
}

// PatchJSON sends a JSON PATCH request, encoding payload.
func (c *Client) PatchJSON(path string, payload any) *Response {
	c.t.Helper()
	return c.sendJSON(http.MethodPatch, path, payload)
}

// PostForm sends a form-encoded POST request.
func (c *Client) PostForm(path string, values map[string]string) *Response {
	c.t.Helper()

	// Sorted, so a body an assertion looks at is the same on every run.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var body strings.Builder
	for i, name := range names {
		if i > 0 {
			body.WriteByte('&')
		}
		body.WriteString(urlEncode(name) + "=" + urlEncode(values[name]))
	}

	return c.send(http.MethodPost, path, strings.NewReader(body.String()),
		"application/x-www-form-urlencoded")
}

// PostRaw sends a POST request with a body and content type the caller chooses,
// for a payload the framework's encoders would change — a signed webhook, an
// upload, a deliberately malformed document.
func (c *Client) PostRaw(path, contentType string, body []byte) *Response {
	c.t.Helper()
	return c.send(http.MethodPost, path, bytes.NewReader(body), contentType)
}

// Do sends a request the caller built, applying the client's default headers to
// any it has not set. It is the escape hatch for anything the helpers above do
// not cover.
func (c *Client) Do(request *http.Request) *Response {
	c.t.Helper()
	if request == nil {
		c.t.Fatal("apitest: request cannot be nil")
	}

	for name, values := range c.headers {
		if request.Header.Get(name) == "" {
			for _, value := range values {
				request.Header.Add(name, value)
			}
		}
	}

	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return &Response{t: c.t, request: request, recorded: recorder.Result()}
}

func (c *Client) sendJSON(method, path string, payload any) *Response {
	c.t.Helper()

	encoded, err := json.Marshal(payload)
	if err != nil {
		c.t.Fatalf("apitest: encode %s %s body: %v", method, path, err)
	}
	return c.send(method, path, bytes.NewReader(encoded), "application/json")
}

func (c *Client) send(method, path string, body io.Reader, contentType string) *Response {
	c.t.Helper()

	request := httptest.NewRequest(method, path, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return c.Do(request)
}

// Response is a recorded response, with assertions that fail the test.
//
// Every assertion returns the response, so they chain, and every one reports the
// status and body when it fails — the two things needed to understand why.
type Response struct {
	t        testing.TB
	request  *http.Request
	recorded *http.Response
	body     []byte
	bodyRead bool
}

// Result returns the underlying *http.Response, for anything the assertions here
// do not cover.
func (r *Response) Result() *http.Response {
	return r.recorded
}

// Status returns the response status code.
func (r *Response) Status() int {
	return r.recorded.StatusCode
}

// Body returns the response body, reading it once and caching it so assertions
// and decoding compose.
func (r *Response) Body() []byte {
	r.t.Helper()

	if r.bodyRead {
		return r.body
	}
	r.bodyRead = true

	// The recorder's body is backed by a buffer, so this cannot fail today. It is
	// checked rather than discarded because a Response built from a real response
	// -- which Result already hands out -- would make it reachable.
	body, err := io.ReadAll(r.recorded.Body)
	if err != nil {
		r.t.Fatalf("apitest: read %s body: %v", r.describe(), err)
	}
	r.body = body
	return body
}

// AssertStatus fails unless the response has the given status.
func (r *Response) AssertStatus(status int) *Response {
	r.t.Helper()

	if r.recorded.StatusCode != status {
		r.t.Fatalf("apitest: %s = %d, want %d\nbody: %s",
			r.describe(), r.recorded.StatusCode, status, r.Body())
	}
	return r
}

// AssertHeader fails unless the named response header has the given value.
func (r *Response) AssertHeader(name, value string) *Response {
	r.t.Helper()

	if got := r.recorded.Header.Get(name); got != value {
		r.t.Fatalf("apitest: %s header %s = %q, want %q",
			r.describe(), name, got, value)
	}
	return r
}

// AssertBodyContains fails unless the body contains the given text. It is for
// bodies that are not JSON; a JSON response is better checked with DecodeJSON or
// AssertError, which do not pass because a value happened to appear somewhere.
func (r *Response) AssertBodyContains(text string) *Response {
	r.t.Helper()

	if !bytes.Contains(r.Body(), []byte(text)) {
		r.t.Fatalf("apitest: %s body does not contain %q\nbody: %s",
			r.describe(), text, r.Body())
	}
	return r
}

// DecodeJSON decodes the body into target.
//
// Unknown fields are rejected, so a test decoding into a struct that has drifted
// from the response fails rather than quietly reading zero values.
func (r *Response) DecodeJSON(target any) *Response {
	r.t.Helper()

	if target == nil {
		r.t.Fatalf("apitest: %s decode target cannot be nil", r.describe())
	}

	decoder := json.NewDecoder(bytes.NewReader(r.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		r.t.Fatalf("apitest: %s decode body: %v\nbody: %s", r.describe(), err, r.Body())
	}
	return r
}

// AssertError fails unless the response is the framework's error document with
// the given status and code.
//
// It reads the rendered envelope rather than matching a substring, so a body that
// merely mentions the code somewhere does not pass.
func (r *Response) AssertError(status int, code string) *Response {
	r.t.Helper()

	r.AssertStatus(status)
	envelope := r.envelope()
	if envelope.Error.Code != code {
		r.t.Fatalf("apitest: %s error code = %q, want %q\nbody: %s",
			r.describe(), envelope.Error.Code, code, r.Body())
	}
	return r
}

// AssertFieldError fails unless the response is a validation error naming field
// with a message containing text. An empty text matches any message, which is
// the usual case: the field is what a test cares about.
func (r *Response) AssertFieldError(field, text string) *Response {
	r.t.Helper()

	envelope := r.envelope()
	messages, ok := envelope.Error.Fields[field]
	if !ok {
		fields := make([]string, 0, len(envelope.Error.Fields))
		for name := range envelope.Error.Fields {
			fields = append(fields, name)
		}
		sort.Strings(fields)
		r.t.Fatalf("apitest: %s reported no error for %q; fields: %v\nbody: %s",
			r.describe(), field, fields, r.Body())
	}
	if text == "" {
		return r
	}

	for _, message := range messages {
		if strings.Contains(message, text) {
			return r
		}
	}
	r.t.Fatalf("apitest: %s errors for %q are %q, none containing %q",
		r.describe(), field, messages, text)
	return r
}

// envelope decodes the framework's error document.
func (r *Response) envelope() ossein.ErrorEnvelope {
	r.t.Helper()

	var envelope ossein.ErrorEnvelope
	if err := json.Unmarshal(r.Body(), &envelope); err != nil {
		r.t.Fatalf("apitest: %s body is not an error document: %v\nbody: %s",
			r.describe(), err, r.Body())
	}
	if envelope.Error.Code == "" {
		r.t.Fatalf("apitest: %s body is not an error document\nbody: %s",
			r.describe(), r.Body())
	}
	return envelope
}

// describe names the request, so a failure says which one it was.
func (r *Response) describe() string {
	return fmt.Sprintf("%s %s", r.request.Method, r.request.URL.RequestURI())
}

// urlEncode percent-encodes a form value. net/url would do this, but importing it
// for two lines pulls a parser into a file that only builds a string.
func urlEncode(value string) string {
	var encoded strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			encoded.WriteByte(c)
		case c == ' ':
			encoded.WriteByte('+')
		default:
			fmt.Fprintf(&encoded, "%%%02X", c)
		}
	}
	return encoded.String()
}
