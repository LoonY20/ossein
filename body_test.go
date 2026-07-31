package ossein

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyReturnsExactBytes is the reason raw access matters: a signature covers
// the bytes as received, so re-encoding a decoded struct will not match.
func TestBodyReturnsExactBytes(t *testing.T) {
	const payload = `{"name":  "Erik",   "email":"erik@example.com"}`

	var got []byte
	app := New()
	app.Post("/users", func(c *Context) error {
		raw, err := c.Body()
		if err != nil {
			return err
		}
		got = raw
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if string(got) != payload {
		t.Fatalf("body = %q, want the bytes exactly as sent", got)
	}
}

// TestSignedWebhookFlow is the end-to-end shape this feature exists for: verify
// an HMAC over the raw bytes and still get strict decoding and validation.
func TestSignedWebhookFlow(t *testing.T) {
	const secret = "shhh"
	sign := func(body string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		return hex.EncodeToString(mac.Sum(nil))
	}

	app := New()
	app.Post("/hooks", func(c *Context) error {
		raw, err := c.Body()
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		provided, decodeErr := hex.DecodeString(c.Request.Header.Get("X-Signature"))
		if decodeErr != nil || !hmac.Equal(provided, mac.Sum(nil)) {
			return Unauthorized("invalid_signature", "Signature does not match")
		}

		// Everything BindJSON provides is still available.
		var request createUserRequest
		if err := c.BindJSON(&request); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, request)
	})

	post := func(body, signature string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Signature", signature)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		return response
	}

	valid := `{"name":"Erik","email":"erik@example.com"}`
	if response := post(valid, sign(valid)); response.Code != http.StatusOK {
		t.Fatalf("valid delivery status = %d (body %q)", response.Code, response.Body.String())
	}

	// A body reformatted in transit must fail verification, which proves the
	// signature was checked against the received bytes.
	reformatted := `{"name":"Erik", "email":"erik@example.com"}`
	if response := post(reformatted, sign(valid)); response.Code != http.StatusUnauthorized {
		t.Fatalf("reformatted delivery status = %d, want 401", response.Code)
	}

	// Validation still runs after a successful signature check.
	invalid := `{"name":"","email":"nope"}`
	if response := post(invalid, sign(invalid)); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid delivery status = %d, want 422", response.Code)
	}

	// Strict decoding still applies.
	unknown := `{"name":"Erik","email":"erik@example.com","admin":true}`
	if response := post(unknown, sign(unknown)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field delivery status = %d, want 400", response.Code)
	}
}

// TestBodyIsReadOnlyOnce keeps repeated access cheap and correct.
func TestBodyIsReadOnlyOnce(t *testing.T) {
	app := New()
	var first, second []byte
	var reads int

	app.Post("/count", func(c *Context) error {
		var err error
		if first, err = c.Body(); err != nil {
			return err
		}
		if second, err = c.Body(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/count", nil)
	request.Body = &countingReader{
		Reader: strings.NewReader(`{"name":"Erik"}`),
		reads:  &reads,
	}
	app.ServeHTTP(httptest.NewRecorder(), request)

	if string(first) != `{"name":"Erik"}` {
		t.Fatalf("first = %q", first)
	}
	if string(second) != string(first) {
		t.Fatalf("second = %q, want the same bytes as the first read", second)
	}
	if reads == 0 {
		t.Fatal("expected the body to be read")
	}
}

type countingReader struct {
	io.Reader
	reads *int
}

func (r *countingReader) Read(p []byte) (int, error) {
	*r.reads++
	return r.Reader.Read(p)
}

func (r *countingReader) Close() error { return nil }

// TestBodyRespectsMaxBindBytes keeps the configured limit in one place instead of
// forcing applications to re-declare it.
func TestBodyRespectsMaxBindBytes(t *testing.T) {
	app := New(WithMaxBindBytes(8))
	app.Post("/users", func(c *Context) error {
		_, err := c.Body()
		return err
	})

	request := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "request_too_large") {
		t.Fatalf("body = %q, want request_too_large", response.Body.String())
	}
}

// TestBodyOnEmptyRequest keeps a bodyless request from being an error.
func TestBodyOnEmptyRequest(t *testing.T) {
	app := New()
	app.Get("/ping", func(c *Context) error {
		raw, err := c.Body()
		if err != nil {
			return err
		}
		if len(raw) != 0 {
			return NewHTTPError(http.StatusInternalServerError, "unexpected", "expected an empty body")
		}
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
}

// TestBodyWithoutARequestBody covers a request constructed without a body at
// all, which net/http permits.
func TestBodyWithoutARequestBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Body = nil

	ctx := NewContext(httptest.NewRecorder(), request)
	raw, err := ctx.Body()
	if err != nil {
		t.Fatalf("Body() error = %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("body = %q, want empty", raw)
	}

	// The cached result is returned on a second call.
	again, err := ctx.Body()
	if err != nil || len(again) != 0 {
		t.Fatalf("second Body() = %q, %v", again, err)
	}
}

// TestBindLimitFallsBackToTheDefault covers a Context whose limit was never set,
// such as one built directly rather than by the application pipeline.
func TestBindLimitFallsBackToTheDefault(t *testing.T) {
	ctx := &Context{
		Response: httptest.NewRecorder(),
		Request:  httptest.NewRequest(http.MethodGet, "/", nil),
	}
	if got := ctx.bindLimit(); got != defaultMaxBindBytes {
		t.Fatalf("bindLimit() = %d, want %d", got, defaultMaxBindBytes)
	}

	ctx.maxBindBytes = 32
	if got := ctx.bindLimit(); got != 32 {
		t.Fatalf("bindLimit() = %d, want 32", got)
	}
}

// TestBindJSONAfterBodyStillChecksContentType keeps media-type enforcement, which
// Body deliberately does not perform since a raw body may be anything.
func TestBindJSONAfterBodyStillChecksContentType(t *testing.T) {
	app := New()
	app.Post("/users", func(c *Context) error {
		if _, err := c.Body(); err != nil {
			return err
		}
		var request createUserRequest
		return c.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", response.Code)
	}
}

// TestBindJSONAfterBodyRejectsTrailingValue keeps the single-value rule.
func TestBindJSONAfterBodyRejectsTrailingValue(t *testing.T) {
	app := New()
	app.Post("/users", func(c *Context) error {
		if _, err := c.Body(); err != nil {
			return err
		}
		var request createUserRequest
		return c.BindJSON(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"Erik","email":"erik@example.com"}{"extra":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", response.Code, response.Body.String())
	}
}

// TestBodyLeavesRequestBodyReadable keeps standard library helpers such as
// ParseForm usable after the raw bytes have been taken.
func TestBodyLeavesRequestBodyReadable(t *testing.T) {
	app := New()
	var formValue string
	app.Post("/form", func(c *Context) error {
		raw, err := c.Body()
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			return NewHTTPError(http.StatusInternalServerError, "empty", "expected a body")
		}
		if err := c.Request.ParseForm(); err != nil {
			return BadRequest("invalid_form", "form could not be parsed").WithCause(err)
		}
		formValue = c.Request.PostFormValue("event")
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/form",
		strings.NewReader("event=invoice.paid&limit=5"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if formValue != "invoice.paid" {
		t.Fatalf("form value = %q, want the body to still be parseable", formValue)
	}
}
