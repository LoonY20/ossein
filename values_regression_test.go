package ossein

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestValuesCanBeConstructedDirectly is what makes a bind method testable as
// ordinary Go, which is the claim the explicit-contract design rests on.
func TestValuesCanBeConstructedDirectly(t *testing.T) {
	values := NewValues(url.Values{"page": {"3"}, "q": {"needle"}})

	query := &listQuery{}
	if err := query.BindQuery(values); err != nil {
		t.Fatalf("BindQuery: %v", err)
	}
	if err := values.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	if query.Page != 3 || query.Search != "needle" {
		t.Fatalf("bound %+v", query)
	}
}

func TestNewValuesToleratesNil(t *testing.T) {
	values := NewValues(nil)
	if values.Has("anything") {
		t.Fatal("expected no fields")
	}
	if got := values.String("anything"); got != "" {
		t.Fatalf("String = %q", got)
	}
	if err := values.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
}

// TestHasReportsAnEmptyFieldAsPresent pins the presence semantics Has documents.
// Nothing constrained this, so redefining Has as "non-empty" passed the suite.
func TestHasReportsAnEmptyFieldAsPresent(t *testing.T) {
	values := NewValues(url.Values{"page": {""}, "flag": {""}})

	if !values.Has("page") {
		t.Fatal(`Has("page") = false for "?page="; an empty field is still present`)
	}
	if values.Has("missing") {
		t.Fatal(`Has("missing") = true`)
	}
}

// TestOrAccessorsFallBackForAbsentAndEmptyFields covers the defaulting case that
// Has plus a typed accessor gets wrong: an HTML form submits untouched inputs as
// present-but-empty, so "?page=" must still take the default.
func TestOrAccessorsFallBackForAbsentAndEmptyFields(t *testing.T) {
	for _, raw := range []url.Values{
		{},
		{"page": {""}, "size": {""}, "ratio": {""}, "flag": {""}, "name": {""}},
		{"page": {"  "}, "size": {" "}, "ratio": {" "}, "flag": {" "}, "name": {" "}},
	} {
		values := NewValues(raw)

		if got := values.IntOr("page", 1); got != 1 {
			t.Fatalf("IntOr = %d, want the fallback", got)
		}
		if got := values.Int64Or("size", 20); got != 20 {
			t.Fatalf("Int64Or = %d, want the fallback", got)
		}
		if got := values.Float64Or("ratio", 1.5); got != 1.5 {
			t.Fatalf("Float64Or = %v, want the fallback", got)
		}
		if got := values.BoolOr("flag", true); !got {
			t.Fatal("BoolOr = false, want the fallback")
		}
		if got := values.StringOr("name", "anonymous"); got != "anonymous" {
			t.Fatalf("StringOr = %q, want the fallback", got)
		}
		if err := values.Err(); err != nil {
			t.Fatalf("Err() = %v, want no error for absent or empty fields", err)
		}
	}
}

func TestOrAccessorsUseSubmittedValues(t *testing.T) {
	values := NewValues(url.Values{
		"page":  {"7"},
		"size":  {"50"},
		"ratio": {"2.5"},
		"flag":  {"false"},
		"name":  {"erik"},
	})

	if got := values.IntOr("page", 1); got != 7 {
		t.Fatalf("IntOr = %d", got)
	}
	if got := values.Int64Or("size", 20); got != 50 {
		t.Fatalf("Int64Or = %d", got)
	}
	if got := values.Float64Or("ratio", 1.5); got != 2.5 {
		t.Fatalf("Float64Or = %v", got)
	}
	if got := values.BoolOr("flag", true); got {
		t.Fatal("BoolOr = true, want the submitted false")
	}
	if got := values.StringOr("name", "anonymous"); got != "erik" {
		t.Fatalf("StringOr = %q", got)
	}
}

// TestOrAccessorsStillReportMalformedValues keeps the fallback from swallowing a
// genuine type error.
func TestOrAccessorsStillReportMalformedValues(t *testing.T) {
	values := NewValues(url.Values{"page": {"abc"}})

	if got := values.IntOr("page", 1); got != 1 {
		t.Fatalf("IntOr = %d, want the fallback after a type error", got)
	}
	if err := values.Err(); err == nil {
		t.Fatal("expected a field error for a malformed value")
	}
}

// TestInt64AcceptsValuesBeyondInt32 pins the bit size, which an overflow test
// using a value too large for either width cannot.
func TestInt64AcceptsValuesBeyondInt32(t *testing.T) {
	values := NewValues(url.Values{"n": {"3000000000"}})

	if got := values.Int64("n"); got != 3000000000 {
		t.Fatalf("Int64 = %d, want 3000000000", got)
	}
	if err := values.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
}

// TestQueryFieldCountIsCapped closes the gap between the two sources: the same
// payload was a 413 as a body and a 26 MB map as a query string.
func TestQueryFieldCountIsCapped(t *testing.T) {
	var builder strings.Builder
	for i := 0; i <= maxFormFields; i++ {
		if i > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString("k")
		builder.WriteString(strconvItoa(i))
		builder.WriteByte('=')
	}

	app := New()
	app.Get("/links", func(c *Context) error {
		var query listQuery
		return c.BindQuery(&query)
	})

	request := httptest.NewRequest(http.MethodGet, "/links", nil)
	request.URL.RawQuery = builder.String()
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "too_many_fields") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestFieldCountBoundaryIsInclusive pins the boundary: exactly the limit is fine.
func TestFieldCountBoundaryIsInclusive(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < maxFormFields; i++ {
		if i > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString("k")
		builder.WriteString(strconvItoa(i))
		builder.WriteByte('=')
	}

	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/replay", func(c *Context) error {
		return c.BindForm(&modeCapture{mode: new(string)})
	})

	response := postForm(t, app, builder.String(), "application/x-www-form-urlencoded")
	if response.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("exactly %d fields was rejected; the limit must be inclusive",
			maxFormFields)
	}
}

// TestMultipartBindIgnoresAMalformedQueryString extends the invariant the
// urlencoded path already had: a bind that only reads the body must not fail
// because of the URL. ParseMultipartForm parses the query internally and returned
// that failure as a malformed body.
func TestMultipartBindIgnoresAMalformedQueryString(t *testing.T) {
	var captured *uploadRequest
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/upload", func(c *Context) error {
		var request uploadRequest
		if err := c.BindForm(&request); err != nil {
			return err
		}
		captured = &request
		return c.NoContent(http.StatusNoContent)
	})

	body, contentType := multipartBody(t,
		map[string]string{"event": "bulk.replay"}, "batch.ndjson", "line\n")

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.URL.RawQuery = "%zz"

	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", response.Code, response.Body.String())
	}
	if captured.Filename != "batch.ndjson" {
		t.Fatalf("filename = %q", captured.Filename)
	}
}

// TestFormRequiredSeesFileFields resolves a contradiction: Has reported the field
// as submitted and File returned its header while Required called it missing.
func TestFormRequiredSeesFileFields(t *testing.T) {
	var recorded error
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/upload", func(c *Context) error {
		return c.BindForm(&requiredFileProbe{recorded: &recorded})
	})

	body, contentType := multipartBody(t, map[string]string{}, "avatar.png", "bytes")
	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	app.ServeHTTP(httptest.NewRecorder(), request)

	if recorded != nil {
		t.Fatalf("Required on a field submitted as a file recorded %v", recorded)
	}
}

type requiredFileProbe struct {
	recorded *error
}

func (p *requiredFileProbe) BindForm(form *Form) error {
	form.Required("deliveries")
	*p.recorded = form.Err()
	return nil
}

// TestBindFormPopulatesPostForm pins the documented side effect, so PostFormValue
// and a later ParseForm agree with what was bound.
func TestBindFormPopulatesPostForm(t *testing.T) {
	var viaStandardLibrary string
	app := New()
	app.Post("/replay", func(c *Context) error {
		if err := c.BindForm(&modeCapture{mode: new(string)}); err != nil {
			return err
		}
		viaStandardLibrary = c.Request.PostFormValue("mode")
		return c.NoContent(http.StatusNoContent)
	})

	response := postForm(t, app, "mode=fast", "application/x-www-form-urlencoded")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if viaStandardLibrary != "fast" {
		t.Fatalf("PostFormValue = %q, want the bound value", viaStandardLibrary)
	}
}

// TestQueryParseFailureIsCached asserts identity, not just a recurring message, so
// removing the cache is detectable.
func TestQueryParseFailureIsCached(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.URL.RawQuery = "page=%zz"
	ctx := NewContext(httptest.NewRecorder(), request)

	_, first := ctx.Query()
	_, second := ctx.Query()
	if first == nil || second == nil {
		t.Fatal("expected both calls to fail")
	}
	if first != second {
		t.Fatal("expected the same error instance; the failure is not cached")
	}
}

// TestBindQueryPropagatesAPlainBindError covers a bind method that fails without
// recording any field error.
func TestBindQueryPropagatesAPlainBindError(t *testing.T) {
	app := New()
	app.Get("/links", func(c *Context) error {
		return c.BindQuery(plainFailingQueryBinder{})
	})

	response := getQuery(t, app, "/links?page=1")
	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (body %q)", response.Code, response.Body.String())
	}
}

type plainFailingQueryBinder struct{}

func (plainFailingQueryBinder) BindQuery(*Values) error {
	return NewHTTPError(http.StatusTeapot, "bind_failed", "the bind method failed")
}

// TestJSONAndFormAgreeOnMalformedContentTypeParameters keeps the two binders
// consistent: a broken parameter must not decide the media type for one and not
// the other.
func TestJSONAndFormAgreeOnMalformedContentTypeParameters(t *testing.T) {
	app := New()
	app.Post("/json", func(c *Context) error {
		var request createUserRequest
		if err := c.BindJSON(&request); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	app.Post("/form", func(c *Context) error {
		if err := c.BindForm(&modeCapture{mode: new(string)}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	jsonRequest := httptest.NewRequest(http.MethodPost, "/json",
		strings.NewReader(`{"name":"Erik","email":"erik@example.com"}`))
	jsonRequest.Header.Set("Content-Type", "application/json; charset=")
	jsonResponse := httptest.NewRecorder()
	app.ServeHTTP(jsonResponse, jsonRequest)

	formRequest := httptest.NewRequest(http.MethodPost, "/form",
		strings.NewReader("mode=fast"))
	formRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=")
	formResponse := httptest.NewRecorder()
	app.ServeHTTP(formResponse, formRequest)

	if jsonResponse.Code != formResponse.Code {
		t.Fatalf("JSON = %d, form = %d; a malformed parameter must be treated alike",
			jsonResponse.Code, formResponse.Code)
	}
	if jsonResponse.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (JSON body %q)",
			jsonResponse.Code, jsonResponse.Body.String())
	}
}
