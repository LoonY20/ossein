package ossein

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// replayRequest is the shape an application writes: binding is an explicit
// method, so no reflection is involved on the request path.
type replayRequest struct {
	Event  string
	Limit  int
	DryRun bool
	Tags   []string
}

func (r *replayRequest) BindForm(form *Form) error {
	r.Event = form.Required("event")
	r.Limit = form.Int("limit")
	r.DryRun = form.Bool("dry_run")
	r.Tags = form.Strings("tags")
	if !form.Has("limit") {
		r.Limit = 10
	}
	return nil
}

func (r *replayRequest) Validate() error {
	errs := NewValidationError()
	if r.Limit < 0 || r.Limit > 100 {
		errs.Add("limit", "must be between 0 and 100")
	}
	return errs.OrNil()
}

func postForm(t *testing.T, app *App, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	return response
}

func newReplayApp(captured **replayRequest) *App {
	app := New()
	app.Post("/replay", func(c *Context) error {
		var request replayRequest
		if err := c.BindForm(&request); err != nil {
			return err
		}
		*captured = &request
		return c.NoContent(http.StatusNoContent)
	})
	return app
}

func TestBindFormDecodesURLEncodedValues(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app,
		"event=invoice.paid&limit=5&dry_run=true&tags=a&tags=b",
		"application/x-www-form-urlencoded")

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Event != "invoice.paid" {
		t.Fatalf("event = %q", captured.Event)
	}
	if captured.Limit != 5 {
		t.Fatalf("limit = %d", captured.Limit)
	}
	if !captured.DryRun {
		t.Fatal("dry_run = false, want true")
	}
	if len(captured.Tags) != 2 || captured.Tags[0] != "a" || captured.Tags[1] != "b" {
		t.Fatalf("tags = %v", captured.Tags)
	}
}

func TestBindFormAppliesDefaultsForAbsentFields(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, "event=invoice.paid", "application/x-www-form-urlencoded")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Limit != 10 {
		t.Fatalf("limit = %d, want the handler's default of 10", captured.Limit)
	}
	if captured.DryRun {
		t.Fatal("dry_run should default to false")
	}
}

// TestBindFormRejectsJSONContentType is the concrete defect from the field notes:
// posting JSON to a form endpoint used to parse as an empty form and produce a
// misleading validation error instead of 415.
func TestBindFormRejectsJSONContentType(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, `{"event":"invoice.paid"}`, "application/json")

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "unsupported_media_type") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestBindFormRequiresAContentType records a deliberate asymmetry with BindJSON,
// which tolerates an absent Content-Type. Decoding JSON validates the format on
// the way through, so guessing is safe there. Parsing a query string almost never
// fails, so guessing would turn an unlabelled body of any shape into silently
// empty fields — exactly the misleading validation error this feature exists to
// replace.
func TestBindFormRequiresAContentType(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, "event=invoice.paid", "")
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %q)", response.Code, response.Body.String())
	}
	if captured != nil {
		t.Fatal("the request must not have been bound")
	}
}

// TestBindFormReportsMissingRequiredFields checks the field-level errors a form
// produces, using the same ValidationError shape as JSON binding.
func TestBindFormReportsMissingRequiredFields(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, "limit=5", "application/x-www-form-urlencoded")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"event"`) {
		t.Fatalf("body = %q, want an error on the event field", response.Body.String())
	}
}

// TestBindFormReportsTypeErrorsBeforeValidating keeps a malformed value from also
// being reported as a semantic failure.
func TestBindFormReportsTypeErrorsBeforeValidating(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, "event=invoice.paid&limit=abc",
		"application/x-www-form-urlencoded")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"limit"`) {
		t.Fatalf("body = %q, want an error on the limit field", body)
	}
	if strings.Contains(body, "between 0 and 100") {
		t.Fatalf("body = %q, want the type error only, not the validation rule", body)
	}
}

// TestBindFormRunsValidationAfterBinding keeps the Validatable contract working.
func TestBindFormRunsValidationAfterBinding(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	response := postForm(t, app, "event=invoice.paid&limit=500",
		"application/x-www-form-urlencoded")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if !strings.Contains(response.Body.String(), "between 0 and 100") {
		t.Fatalf("body = %q, want the validation rule", response.Body.String())
	}
}

func TestBindFormRespectsMaxBindBytes(t *testing.T) {
	app := New(WithMaxBindBytes(8))
	app.Post("/replay", func(c *Context) error {
		var request replayRequest
		return c.BindForm(&request)
	})

	response := postForm(t, app,
		"event=invoice.paid&limit=5&dry_run=true",
		"application/x-www-form-urlencoded")

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", response.Code, response.Body.String())
	}
}

// uploadRequest exercises multipart binding, including a file part.
type uploadRequest struct {
	Event    string
	Filename string
	Contents string
}

func (r *uploadRequest) BindForm(form *Form) error {
	r.Event = form.Required("event")

	header := form.RequiredFile("deliveries")
	if header == nil {
		return nil
	}
	r.Filename = header.Filename

	file, err := header.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	contents, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	r.Contents = string(contents)
	return nil
}

func multipartBody(t *testing.T, values map[string]string, filename, contents string) (string, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for field, value := range values {
		if err := writer.WriteField(field, value); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("deliveries", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(contents)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buffer.String(), writer.FormDataContentType()
}

func TestBindFormHandlesMultipartWithFiles(t *testing.T) {
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
		map[string]string{"event": "bulk.replay"}, "batch.ndjson", "{\"a\":1}\n{\"b\":2}\n")

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Event != "bulk.replay" {
		t.Fatalf("event = %q", captured.Event)
	}
	if captured.Filename != "batch.ndjson" {
		t.Fatalf("filename = %q", captured.Filename)
	}
	if !strings.Contains(captured.Contents, `{"b":2}`) {
		t.Fatalf("contents = %q", captured.Contents)
	}
}

func TestBindFormReportsAMissingRequiredFile(t *testing.T) {
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/upload", func(c *Context) error {
		var request uploadRequest
		return c.BindForm(&request)
	})

	body, contentType := multipartBody(t, map[string]string{"event": "bulk.replay"}, "", "")

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "deliveries") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestBindFormRejectsANilTarget guards against a programming mistake.
func TestBindFormRejectsANilTarget(t *testing.T) {
	app := New()
	app.Post("/replay", func(c *Context) error {
		return c.BindForm(nil)
	})

	response := postForm(t, app, "event=x", "application/x-www-form-urlencoded")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// TestBindFormAfterBodyUsesTheSameBytes keeps form binding composable with raw
// access, the same way BindJSON is.
func TestBindFormAfterBodyUsesTheSameBytes(t *testing.T) {
	var captured *replayRequest
	var raw []byte
	app := New()
	app.Post("/replay", func(c *Context) error {
		var err error
		if raw, err = c.Body(); err != nil {
			return err
		}
		var request replayRequest
		if err := c.BindForm(&request); err != nil {
			return err
		}
		captured = &request
		return c.NoContent(http.StatusNoContent)
	})

	response := postForm(t, app, "event=invoice.paid&limit=7",
		"application/x-www-form-urlencoded")

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if string(raw) != "event=invoice.paid&limit=7" {
		t.Fatalf("raw = %q", raw)
	}
	if captured.Limit != 7 {
		t.Fatalf("limit = %d", captured.Limit)
	}
}

// TestFormErrorsFromTheBindMethodPropagate keeps a bind method's own error from
// being swallowed.
func TestFormErrorsFromTheBindMethodPropagate(t *testing.T) {
	app := New()
	app.Post("/replay", func(c *Context) error {
		return c.BindForm(&failingBinder{})
	})

	response := postForm(t, app, "event=x", "application/x-www-form-urlencoded")
	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", response.Code)
	}
}

type failingBinder struct{}

func (failingBinder) BindForm(*Form) error {
	return NewHTTPError(http.StatusTeapot, "bind_failed", "the bind method failed")
}
