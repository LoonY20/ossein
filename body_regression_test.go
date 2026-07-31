package ossein

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyErrorIsSticky is the corruption case. A failed read leaves the stream
// partially drained, so a second call must report the same failure instead of
// reading further and returning a fragment as if it were the whole body.
func TestBodyErrorIsSticky(t *testing.T) {
	// The body is sized so that what remains after the first failed read fits
	// inside the limit. A second read then "succeeds" with a fragment, which is
	// the dangerous case: a signature would be checked against partial bytes.
	request := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"name":"Erik"}`))
	ctx := NewContext(httptest.NewRecorder(), request)
	ctx.maxBindBytes = 8

	firstBody, firstErr := ctx.Body()
	if firstErr == nil {
		t.Fatal("expected the first call to report the limit")
	}
	if len(firstBody) != 0 {
		t.Fatalf("first body = %q, want none", firstBody)
	}

	secondBody, secondErr := ctx.Body()
	if secondErr == nil {
		t.Fatalf("second call returned body %q with no error; a partial read must not "+
			"be reported as success", secondBody)
	}
	if secondErr.Error() != firstErr.Error() {
		t.Fatalf("second error = %v, want the first error %v", secondErr, firstErr)
	}
	if len(secondBody) != 0 {
		t.Fatalf("second body = %q, want none", secondBody)
	}
}

// failingReader fails partway through, the shape of a client disconnect or a read
// deadline firing mid-body.
type failingReader struct {
	prefix string
	err    error
	offset int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.offset < len(r.prefix) {
		n := copy(p, r.prefix[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, r.err
}

func (r *failingReader) Close() error { return nil }

// TestBodyReportsTransportFailuresNeutrally keeps a failed read from being
// blamed on JSON, since Body is used for any payload shape.
func TestBodyReportsTransportFailuresNeutrally(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = &failingReader{prefix: "partial", err: errors.New("connection reset")}

	ctx := NewContext(httptest.NewRecorder(), request)
	_, err := ctx.Body()
	if err == nil {
		t.Fatal("expected a read error")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want an *HTTPError", err)
	}
	if httpErr.Code == "invalid_json" {
		t.Fatalf("code = %q; a raw body is not necessarily JSON", httpErr.Code)
	}

	// The failure is sticky, like every other Body error.
	if _, again := ctx.Body(); again == nil {
		t.Fatal("expected the read failure to be reported again")
	}
}

// countingBody records how many bytes were pulled off the request.
type countingBody struct {
	reader io.Reader
	read   int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *countingBody) Close() error { return nil }

// TestBindJSONStopsReadingAtTheFirstSyntaxError keeps malformed input cheap.
// Buffering the whole body before decoding would let a client force the full
// configured limit to be read for input that is invalid at byte one.
func TestBindJSONStopsReadingAtTheFirstSyntaxError(t *testing.T) {
	const size = 1 << 20
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", size))}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = body
	request.Header.Set("Content-Type", "application/json")

	ctx := NewContext(httptest.NewRecorder(), request)
	ctx.maxBindBytes = 4 << 20

	var target createUserRequest
	err := ctx.BindJSON(&target)
	if err == nil {
		t.Fatal("expected a decode error")
	}

	if body.read >= size {
		t.Fatalf("read %d bytes of a %d byte invalid body; decoding must stop at the "+
			"first syntax error", body.read, size)
	}
	t.Logf("read %d bytes before failing", body.read)
}

// TestOversizedMalformedBodyKeepsReportingInvalidJSON pins the status codes
// against the pre-existing contract: a syntax error inside the limit is a 400,
// and only exceeding the limit is a 413.
func TestOversizedMalformedBodyKeepsReportingInvalidJSON(t *testing.T) {
	app := New(WithMaxBindBytes(8))
	app.Post("/users", func(c *Context) error {
		var request createUserRequest
		return c.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader("this is definitely not json and it is long"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "invalid_json") {
		t.Fatalf("body = %q, want invalid_json", response.Body.String())
	}
}

// TestBindJSONStillReportsOversizedValidBodies keeps the 413 path intact.
func TestBindJSONStillReportsOversizedValidBodies(t *testing.T) {
	app := New(WithMaxBindBytes(16))
	app.Post("/users", func(c *Context) error {
		var request createUserRequest
		return c.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

// TestBindJSONAfterAFailedBodyReportsTheSameError keeps BindJSON from starting a
// fresh read over a stream the failed Body call already drained.
func TestBindJSONAfterAFailedBodyReportsTheSameError(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"name":"Erik"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx := NewContext(httptest.NewRecorder(), request)
	ctx.maxBindBytes = 8

	_, bodyErr := ctx.Body()
	if bodyErr == nil {
		t.Fatal("expected the limit to be reported")
	}

	var target createUserRequest
	bindErr := ctx.BindJSON(&target)
	if bindErr == nil {
		t.Fatal("expected BindJSON to report the earlier read failure")
	}
	if bindErr.Error() != bodyErr.Error() {
		t.Fatalf("BindJSON error = %v, want the Body error %v", bindErr, bodyErr)
	}
	if target.Name != "" {
		t.Fatalf("target was populated from a partial read: %+v", target)
	}
}

// TestBindJSONWithoutARequestBody covers a request carrying no body at all.
func TestBindJSONWithoutARequestBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = nil
	request.Header.Set("Content-Type", "application/json")

	ctx := NewContext(httptest.NewRecorder(), request)
	var target createUserRequest
	err := ctx.BindJSON(&target)
	if err == nil {
		t.Fatal("expected an error for an absent body")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("error = %v, want a 400", err)
	}
}

// TestBodyLimitIsPerRequestNotPerCall keeps the configured limit from being
// multiplied by the number of calls.
func TestBodyLimitIsPerRequestNotPerCall(t *testing.T) {
	const size = 4096
	body := &countingBody{reader: strings.NewReader(strings.Repeat("y", size))}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Body = body

	ctx := NewContext(httptest.NewRecorder(), request)
	ctx.maxBindBytes = 64

	for i := 0; i < 5; i++ {
		if _, err := ctx.Body(); err == nil {
			t.Fatalf("call %d: expected the limit to be reported", i+1)
		}
	}

	// One limit plus the single byte that detects the overflow.
	if body.read > 128 {
		t.Fatalf("read %d bytes across five calls; the limit must bound the request, "+
			"not each call", body.read)
	}
}
