package ossein

import (
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// floatRequest mirrors a realistic money field with a range rule.
type floatRequest struct {
	Amount float64
}

func (r *floatRequest) BindForm(form *Form) error {
	r.Amount = form.Float64("amount")
	return nil
}

func (r *floatRequest) Validate() error {
	errs := NewValidationError()
	if r.Amount <= 0 || r.Amount > 10000 {
		errs.Add("amount", "must be between 0 and 10000")
	}
	return errs.OrNil()
}

// TestFormFloat64RejectsNonFiniteValues closes a validation bypass. NaN makes
// every comparison in an application's rules false, so a range check silently
// passes, and the value then fails JSON encoding and surfaces as a 500.
func TestFormFloat64RejectsNonFiniteValues(t *testing.T) {
	for _, raw := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "-infinity"} {
		t.Run(raw, func(t *testing.T) {
			app := New()
			app.Post("/pay", func(c *Context) error {
				var request floatRequest
				if err := c.BindForm(&request); err != nil {
					return err
				}
				if math.IsNaN(request.Amount) || math.IsInf(request.Amount, 0) {
					t.Errorf("bound a non-finite amount: %v", request.Amount)
				}
				return c.JSON(http.StatusOK, request)
			})

			request := httptest.NewRequest(http.MethodPost, "/pay",
				strings.NewReader("amount="+raw))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"amount"`) {
				t.Fatalf("body = %q, want an error on the amount field", response.Body.String())
			}
		})
	}
}

// TestBindFormReadsTheBodyForEveryMethod covers methods the standard library's
// ParseForm ignores bodies for. A correctly labelled form body must bind whatever
// the method is, otherwise the request reports its fields as missing.
func TestBindFormReadsTheBodyForEveryMethod(t *testing.T) {
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			var captured *replayRequest
			app := New()
			app.Handle(method, "/replay", func(c *Context) error {
				var request replayRequest
				if err := c.BindForm(&request); err != nil {
					return err
				}
				captured = &request
				return c.NoContent(http.StatusNoContent)
			})

			request := httptest.NewRequest(method, "/replay",
				strings.NewReader("event=invoice.paid&limit=3"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
			}
			if captured.Event != "invoice.paid" || captured.Limit != 3 {
				t.Fatalf("bound %+v, want the body's values", captured)
			}
		})
	}
}

// TestBindFormExcludesQueryParameters keeps a body-only bind body-only. Reading
// Request.Form instead of PostForm would let a query string satisfy fields the
// client never put in the body.
func TestBindFormExcludesQueryParameters(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	request := httptest.NewRequest(http.MethodPost, "/replay?event=from.query&limit=99",
		strings.NewReader("event=from.body&limit=4"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Event != "from.body" {
		t.Fatalf("event = %q, want the body value", captured.Event)
	}
	if captured.Limit != 4 {
		t.Fatalf("limit = %d, want the body value", captured.Limit)
	}
}

// TestBindFormIgnoresQueryOnlyFields is the other half: a field present only in
// the query string must not satisfy a required body field.
func TestBindFormIgnoresQueryOnlyFields(t *testing.T) {
	var captured *replayRequest
	app := newReplayApp(&captured)

	request := httptest.NewRequest(http.MethodPost, "/replay?event=from.query",
		strings.NewReader("limit=4"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"event"`) {
		t.Fatalf("body = %q, want event reported missing", response.Body.String())
	}
}

// TestBindFormMultipartRespectsMaxBindBytes pins the limit the no-temp-file
// guarantee rests on: the in-memory limit passed to the multipart parser must be
// the application's body limit, not a hardcoded value.
func TestBindFormMultipartRespectsMaxBindBytes(t *testing.T) {
	app := New(WithMaxBindBytes(64))
	app.Post("/upload", func(c *Context) error {
		var request uploadRequest
		return c.BindForm(&request)
	})

	body, contentType := multipartBody(t,
		map[string]string{"event": "bulk.replay"}, "batch.ndjson",
		strings.Repeat("x", 4096))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", response.Code, response.Body.String())
	}
}

// TestBindFormLimitsFieldCount bounds the parsed form, which the body limit alone
// does not: a body of empty keys expands into a far larger map.
func TestBindFormLimitsFieldCount(t *testing.T) {
	var builder strings.Builder
	for i := 0; i <= maxFormFields; i++ {
		if i > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString("k")
		builder.WriteString(strings.Repeat("0", 3))
		builder.WriteString(strconvItoa(i))
		builder.WriteByte('=')
	}

	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/replay", func(c *Context) error {
		var request replayRequest
		return c.BindForm(&request)
	})

	request := httptest.NewRequest(http.MethodPost, "/replay",
		strings.NewReader(builder.String()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for too many fields (body %q)",
			response.Code, response.Body.String())
	}
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// hardFailingBinder records field errors and then fails for an unrelated reason.
type hardFailingBinder struct{}

func (hardFailingBinder) BindForm(form *Form) error {
	form.Required("event")
	form.Int("limit")
	return NewHTTPError(http.StatusInternalServerError, "upload_failed", "could not open the upload")
}

// TestBindFormKeepsFieldErrorsWhenTheBindMethodFails keeps a 422 field report
// from disappearing because the bind method also returned an error.
func TestBindFormKeepsFieldErrorsWhenTheBindMethodFails(t *testing.T) {
	app := New()
	app.Post("/replay", func(c *Context) error {
		return c.BindForm(hardFailingBinder{})
	})

	request := httptest.NewRequest(http.MethodPost, "/replay",
		strings.NewReader("limit=abc"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"event"`) || !strings.Contains(body, `"limit"`) {
		t.Fatalf("body = %q, want both field errors", body)
	}
}

// TestFormHasSeesFileFields keeps Has honest about fields submitted as files.
func TestFormHasSeesFileFields(t *testing.T) {
	var sawFile, sawValue bool
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/upload", func(c *Context) error {
		return c.BindForm(&hasProbe{sawFile: &sawFile, sawValue: &sawValue})
	})

	body, contentType := multipartBody(t,
		map[string]string{"event": "bulk.replay"}, "batch.ndjson", "line\n")

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	app.ServeHTTP(httptest.NewRecorder(), request)

	if !sawValue {
		t.Fatal("Has did not see a value field")
	}
	if !sawFile {
		t.Fatal("Has did not see a field submitted as a file")
	}
}

type hasProbe struct {
	sawFile  *bool
	sawValue *bool
}

func (p *hasProbe) BindForm(form *Form) error {
	*p.sawValue = form.Has("event")
	*p.sawFile = form.Has("deliveries")
	return nil
}

// TestFormBoolAcceptsCheckboxConventions keeps the checkbox pair symmetrical: if
// "on" means true, "off" must mean false rather than being a field error.
func TestFormBoolAcceptsCheckboxConventions(t *testing.T) {
	cases := map[string]bool{
		"on":    true,
		"ON":    true,
		"off":   false,
		"OFF":   false,
		"true":  true,
		"false": false,
		"1":     true,
		"0":     false,
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			probe := bindProbe(t, "flag="+raw)
			if probe.err != nil {
				t.Fatalf("Err() = %v, want %q to be accepted", probe.err, raw)
			}
			if probe.flag != want {
				t.Fatalf("Bool(%q) = %v, want %v", raw, probe.flag, want)
			}
		})
	}
}

// TestFormRequiredAndTypedAccessorsTrim documents the whitespace rule: typed
// accessors and Required trim, String returns the value as submitted.
func TestFormRequiredAndTypedAccessorsTrim(t *testing.T) {
	probe := bindProbe(t, "text=%20%20spaced%20%20&number=%20%2042%20")

	if probe.text != "  spaced  " {
		t.Fatalf("String = %q, want the raw value", probe.text)
	}
	if probe.number != 42 {
		t.Fatalf("Int64 = %d, want the trimmed value parsed", probe.number)
	}

	var captured *replayRequest
	app := newReplayApp(&captured)
	response := postForm(t, app, "event=%20%20%20&limit=1",
		"application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a blank required field", response.Code)
	}
}

// TestFormFileReturnsTheFirstUploadAndFilesReturnsAll pins the documented
// behaviour for repeated file fields.
func TestFormFileReturnsTheFirstUploadAndFilesReturnsAll(t *testing.T) {
	var first string
	var all []string
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/upload", func(c *Context) error {
		return c.BindForm(&filesProbe{first: &first, all: &all})
	})

	body, contentType := multipartWithTwoFiles(t)
	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	app.ServeHTTP(httptest.NewRecorder(), request)

	if first != "one.txt" {
		t.Fatalf("File = %q, want the first upload", first)
	}
	if len(all) != 2 || all[1] != "two.txt" {
		t.Fatalf("Files = %v, want both uploads in order", all)
	}
}

type filesProbe struct {
	first *string
	all   *[]string
}

func (p *filesProbe) BindForm(form *Form) error {
	if header := form.File("uploads"); header != nil {
		*p.first = header.Filename
	}
	for _, header := range form.Files("uploads") {
		*p.all = append(*p.all, header.Filename)
	}
	return nil
}

func multipartWithTwoFiles(t *testing.T) (string, string) {
	t.Helper()
	var buffer strings.Builder
	writer := multipart.NewWriter(&buffer)
	for _, name := range []string{"one.txt", "two.txt"} {
		part, err := writer.CreateFormFile("uploads", name)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(name)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buffer.String(), writer.FormDataContentType()
}

// TestBindFormHonoursALargeBodyLimit keeps a raised WithMaxBindBytes effective.
// Routing through the standard library's form parser silently capped it at 10 MB
// and reported the overflow as a malformed body.
func TestBindFormHonoursALargeBodyLimit(t *testing.T) {
	const padding = 11 << 20

	app := New(WithMaxBindBytes(16 << 20))
	var boundEvent string
	app.Post("/replay", func(c *Context) error {
		var request replayRequest
		if err := c.BindForm(&request); err != nil {
			return err
		}
		boundEvent = request.Event
		return c.NoContent(http.StatusNoContent)
	})

	body := "event=invoice.paid&limit=1&pad=" + strings.Repeat("x", padding)
	request := httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", response.Code, response.Body.String())
	}
	if boundEvent != "invoice.paid" {
		t.Fatalf("event = %q", boundEvent)
	}
}
