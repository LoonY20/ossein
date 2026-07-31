package ossein

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// accessorProbe records what every accessor produced, so one request can pin the
// whole surface.
type accessorProbe struct {
	values    url.Values
	text      string
	texts     []string
	number    int64
	fraction  float64
	flag      bool
	checkbox  bool
	fileNames []string
	err       error
}

func (p *accessorProbe) BindForm(form *Form) error {
	p.values = form.Values()
	p.text = form.String("text")
	p.texts = form.Strings("texts")
	p.number = form.Int64("number")
	p.fraction = form.Float64("fraction")
	p.flag = form.Bool("flag")
	p.checkbox = form.Bool("checkbox")
	for _, header := range form.Files("uploads") {
		p.fileNames = append(p.fileNames, header.Filename)
	}
	p.err = form.Err()
	return nil
}

func bindProbe(t *testing.T, body string) *accessorProbe {
	t.Helper()
	probe := &accessorProbe{}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ctx := NewContext(httptest.NewRecorder(), request)
	if err := ctx.BindForm(probe); err != nil {
		t.Fatalf("BindForm: %v", err)
	}
	return probe
}

func TestFormAccessorsReadSubmittedValues(t *testing.T) {
	probe := bindProbe(t,
		"text=hello&texts=a&texts=b&number=-42&fraction=1.5&flag=true&checkbox=on")

	if probe.text != "hello" {
		t.Fatalf("String = %q", probe.text)
	}
	if len(probe.texts) != 2 || probe.texts[1] != "b" {
		t.Fatalf("Strings = %v", probe.texts)
	}
	if probe.number != -42 {
		t.Fatalf("Int64 = %d", probe.number)
	}
	if probe.fraction != 1.5 {
		t.Fatalf("Float64 = %v", probe.fraction)
	}
	if !probe.flag {
		t.Fatal("Bool(flag) = false, want true")
	}
	if !probe.checkbox {
		t.Fatal(`Bool(checkbox) with "on" = false, want true`)
	}
	if probe.values.Get("text") != "hello" {
		t.Fatalf("Values() = %v", probe.values)
	}
	if probe.err != nil {
		t.Fatalf("Err() = %v, want nil", probe.err)
	}
	if probe.fileNames != nil {
		t.Fatalf("Files = %v, want none for a urlencoded body", probe.fileNames)
	}
}

func TestFormAccessorsReturnZeroForAbsentFields(t *testing.T) {
	probe := bindProbe(t, "other=1")

	if probe.text != "" || probe.texts != nil {
		t.Fatalf("text = %q, texts = %v", probe.text, probe.texts)
	}
	if probe.number != 0 || probe.fraction != 0 {
		t.Fatalf("number = %d, fraction = %v", probe.number, probe.fraction)
	}
	if probe.flag || probe.checkbox {
		t.Fatalf("flag = %v, checkbox = %v", probe.flag, probe.checkbox)
	}
	if probe.err != nil {
		t.Fatalf("Err() = %v, want nil for absent fields", probe.err)
	}
}

// TestFormAccessorsReportMalformedValues checks that each typed accessor records
// a field error rather than failing silently.
func TestFormAccessorsReportMalformedValues(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{"int64", "number=abc", "number"},
		{"float64", "fraction=abc", "fraction"},
		{"bool", "flag=maybe", "flag"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			probe := &accessorProbe{}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			ctx := NewContext(httptest.NewRecorder(), request)
			err := ctx.BindForm(probe)
			if err == nil {
				t.Fatal("expected a validation error")
			}

			var validationErr *ValidationError
			if !asValidationError(err, &validationErr) {
				t.Fatalf("error = %v, want a *ValidationError", err)
			}
			if len(validationErr.Fields[testCase.field]) == 0 {
				t.Fatalf("fields = %v, want an error on %q",
					validationErr.Fields, testCase.field)
			}
		})
	}
}

func asValidationError(err error, target **ValidationError) bool {
	candidate, ok := err.(*ValidationError)
	if ok {
		*target = candidate
	}
	return ok
}

// TestFormIntRejectsOverflow keeps a value that does not fit an int from binding
// as zero without comment.
func TestFormIntRejectsOverflow(t *testing.T) {
	probe := &accessorProbe{}
	request := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader("number=99999999999999999999999"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ctx := NewContext(httptest.NewRecorder(), request)
	if err := ctx.BindForm(probe); err == nil {
		t.Fatal("expected an error for an out-of-range number")
	}
}

// TestFormAddErrorRecordsApplicationRules covers a bind method adding its own
// checks alongside the accessors.
func TestFormAddErrorRecordsApplicationRules(t *testing.T) {
	app := New()
	app.Post("/", func(c *Context) error {
		return c.BindForm(&ruleBinder{})
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("mode=bogus"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if !strings.Contains(response.Body.String(), "must be fast or slow") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

type ruleBinder struct{}

func (ruleBinder) BindForm(form *Form) error {
	mode := form.String("mode")
	if mode != "fast" && mode != "slow" {
		form.AddError("mode", "must be fast or slow")
	}
	return nil
}

// TestBindFormRejectsAMalformedContentType covers a header the media-type parser
// cannot read at all.
func TestBindFormRejectsAMalformedContentType(t *testing.T) {
	app := New()
	app.Post("/", func(c *Context) error {
		return c.BindForm(&ruleBinder{})
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("mode=fast"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; boundary=")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %q)", response.Code, response.Body.String())
	}
}

// TestBindFormIgnoresAnUnparseableQueryString keeps a bad query string from
// failing a bind that only reads the body. Routing through Request.ParseForm made
// the two share a failure, and the reported error blamed the body.
func TestBindFormIgnoresAnUnparseableQueryString(t *testing.T) {
	var mode string
	app := New()
	app.Post("/", func(c *Context) error {
		probe := &modeCapture{mode: &mode}
		if err := c.BindForm(probe); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("mode=fast"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.URL.RawQuery = "%zz"

	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", response.Code, response.Body.String())
	}
	if mode != "fast" {
		t.Fatalf("mode = %q, want the body value", mode)
	}
}

type modeCapture struct {
	mode *string
}

func (m *modeCapture) BindForm(form *Form) error {
	*m.mode = form.String("mode")
	return nil
}

// TestBindFormReportsAnUnparseableBody covers a body the query parser rejects.
func TestBindFormReportsAnUnparseableBody(t *testing.T) {
	app := New()
	app.Post("/", func(c *Context) error {
		return c.BindForm(&ruleBinder{})
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("mode=%zz"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "invalid_form") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestBindFormRejectsMultipartWithoutABoundary covers a multipart request the
// standard library cannot parse at all, which is a client error rather than a
// media-type mismatch.
func TestBindFormRejectsMultipartWithoutABoundary(t *testing.T) {
	app := New(WithMaxBindBytes(1 << 20))
	app.Post("/", func(c *Context) error {
		return c.BindForm(&ruleBinder{})
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("mode=fast"))
	request.Header.Set("Content-Type", "multipart/form-data")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "invalid_form") {
		t.Fatalf("body = %q", response.Body.String())
	}
}
