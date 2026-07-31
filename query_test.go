package ossein

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// listQuery is the pagination block every list endpoint used to hand-roll.
type listQuery struct {
	Page    int
	PerPage int
	Search  string
	Tags    []string
}

func (q *listQuery) BindQuery(values *Values) error {
	q.Page = values.Int("page")
	if !values.Has("page") {
		q.Page = 1
	}
	q.PerPage = values.Int("per_page")
	if !values.Has("per_page") {
		q.PerPage = 20
	}
	q.Search = values.String("q")
	q.Tags = values.Strings("tags")
	return nil
}

func (q *listQuery) Validate() error {
	errs := NewValidationError()
	if q.Page < 1 {
		errs.Add("page", "must be a positive integer")
	}
	if q.PerPage < 1 || q.PerPage > 100 {
		errs.Add("per_page", "must be between 1 and 100")
	}
	return errs.OrNil()
}

func newListApp(captured **listQuery) *App {
	app := New()
	app.Get("/links", func(c *Context) error {
		var query listQuery
		if err := c.BindQuery(&query); err != nil {
			return err
		}
		*captured = &query
		return c.NoContent(http.StatusNoContent)
	})
	return app
}

func getQuery(t *testing.T, app *App, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func TestBindQueryDecodesValues(t *testing.T) {
	var captured *listQuery
	app := newListApp(&captured)

	response := getQuery(t, app, "/links?page=3&per_page=50&q=example&tags=a&tags=b")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Page != 3 || captured.PerPage != 50 {
		t.Fatalf("page = %d, per_page = %d", captured.Page, captured.PerPage)
	}
	if captured.Search != "example" {
		t.Fatalf("q = %q", captured.Search)
	}
	if len(captured.Tags) != 2 || captured.Tags[1] != "b" {
		t.Fatalf("tags = %v", captured.Tags)
	}
}

func TestBindQueryAppliesDefaults(t *testing.T) {
	var captured *listQuery
	app := newListApp(&captured)

	response := getQuery(t, app, "/links")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Page != 1 || captured.PerPage != 20 {
		t.Fatalf("page = %d, per_page = %d, want the handler's defaults",
			captured.Page, captured.PerPage)
	}
}

// TestBindQueryReportsTypeErrorsBeforeValidating mirrors the form ordering: a
// malformed value is reported as such, not as a broken application rule.
func TestBindQueryReportsTypeErrorsBeforeValidating(t *testing.T) {
	var captured *listQuery
	app := newListApp(&captured)

	response := getQuery(t, app, "/links?page=abc")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"page"`) {
		t.Fatalf("body = %q, want an error on page", body)
	}
	if strings.Contains(body, "must be a positive integer") {
		t.Fatalf("body = %q, want the type error only", body)
	}
}

func TestBindQueryRunsValidation(t *testing.T) {
	var captured *listQuery
	app := newListApp(&captured)

	response := getQuery(t, app, "/links?per_page=500")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if !strings.Contains(response.Body.String(), "between 1 and 100") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestBindQueryReportsAMalformedQueryString keeps a broken query from binding as
// silently missing fields.
func TestBindQueryReportsAMalformedQueryString(t *testing.T) {
	var captured *listQuery
	app := newListApp(&captured)

	request := httptest.NewRequest(http.MethodGet, "/links", nil)
	request.URL.RawQuery = "page=%zz"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "invalid_query") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestQueryParseFailureIsSticky keeps a second call reporting the same failure
// rather than silently returning an empty set.
func TestQueryParseFailureIsSticky(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/links", nil)
	request.URL.RawQuery = "page=%zz"
	ctx := NewContext(httptest.NewRecorder(), request)

	first, firstErr := ctx.Query()
	if firstErr == nil {
		t.Fatal("expected the first call to report the malformed query")
	}
	if first != nil {
		t.Fatal("expected no values alongside the error")
	}

	second, secondErr := ctx.Query()
	if secondErr == nil {
		t.Fatal("expected the failure to be reported again")
	}
	if secondErr.Error() != firstErr.Error() {
		t.Fatalf("second error = %v, want the first error %v", secondErr, firstErr)
	}
	if second != nil {
		t.Fatal("expected no values on the second call either")
	}
}

// TestBindQueryIgnoresTheRequestBody keeps the two sources separate: a query bind
// must not be satisfiable from a form body.
func TestBindQueryIgnoresTheRequestBody(t *testing.T) {
	var captured *listQuery
	app := New()
	app.Post("/links", func(c *Context) error {
		var query listQuery
		if err := c.BindQuery(&query); err != nil {
			return err
		}
		captured = &query
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/links?page=2",
		strings.NewReader("page=9&per_page=99"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if captured.Page != 2 {
		t.Fatalf("page = %d, want the query value", captured.Page)
	}
	if captured.PerPage != 20 {
		t.Fatalf("per_page = %d, want the default rather than the body value",
			captured.PerPage)
	}
}

func TestBindQueryRejectsANilTarget(t *testing.T) {
	app := New()
	app.Get("/links", func(c *Context) error {
		return c.BindQuery(nil)
	})

	if response := getQuery(t, app, "/links"); response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// TestBindQueryKeepsFieldErrorsWhenTheBindMethodFails mirrors the form precedence.
func TestBindQueryKeepsFieldErrorsWhenTheBindMethodFails(t *testing.T) {
	app := New()
	app.Get("/links", func(c *Context) error {
		return c.BindQuery(hardFailingQueryBinder{})
	})

	response := getQuery(t, app, "/links?page=abc")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"page"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

type hardFailingQueryBinder struct{}

func (hardFailingQueryBinder) BindQuery(values *Values) error {
	values.Int("page")
	return NewHTTPError(http.StatusInternalServerError, "failed", "unrelated failure")
}

// TestQueryReadsValuesWithoutABindTarget covers the ad-hoc path, for a handler
// with one or two parameters that does not want a request type.
func TestQueryReadsValuesWithoutABindTarget(t *testing.T) {
	var page int
	var search string
	var present bool

	app := New()
	app.Get("/links", func(c *Context) error {
		query, err := c.Query()
		if err != nil {
			return err
		}
		page = query.Int("page")
		search = query.String("q")
		present = query.Has("q")
		return query.Err()
	})

	response := getQuery(t, app, "/links?page=4&q=needle")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", response.Code, response.Body.String())
	}
	if page != 4 || search != "needle" || !present {
		t.Fatalf("page = %d, q = %q, present = %v", page, search, present)
	}
}

// TestQueryIsParsedOnce keeps repeated access cheap.
func TestQueryIsParsedOnce(t *testing.T) {
	app := New()
	var same bool
	app.Get("/links", func(c *Context) error {
		first, err := c.Query()
		if err != nil {
			return err
		}
		second, err := c.Query()
		if err != nil {
			return err
		}
		same = first == second
		return c.NoContent(http.StatusNoContent)
	})

	if response := getQuery(t, app, "/links?page=1"); response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if !same {
		t.Fatal("Query returned a different instance on the second call")
	}
}

// TestQueryErrorsAreReportedThroughTheHandler shows the ad-hoc path's contract:
// accessor errors surface when the handler returns Err.
func TestQueryErrorsAreReportedThroughTheHandler(t *testing.T) {
	app := New()
	app.Get("/links", func(c *Context) error {
		query, err := c.Query()
		if err != nil {
			return err
		}
		_ = query.Int("page")
		return query.Err()
	})

	response := getQuery(t, app, "/links?page=abc")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %q)", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"page"`) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestFormAndQueryShareAccessors confirms the two entry points expose one
// accessor set, so learning it once is enough.
func TestFormAndQueryShareAccessors(t *testing.T) {
	var fromQuery, fromForm int64

	app := New()
	app.Get("/q", func(c *Context) error {
		query, err := c.Query()
		if err != nil {
			return err
		}
		fromQuery = query.Int64("n")
		return c.NoContent(http.StatusNoContent)
	})
	app.Post("/f", func(c *Context) error {
		probe := &numberProbe{value: &fromForm}
		return c.BindForm(probe)
	})

	getQuery(t, app, "/q?n=7")

	request := httptest.NewRequest(http.MethodPost, "/f", strings.NewReader("n=7"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(httptest.NewRecorder(), request)

	if fromQuery != 7 || fromForm != 7 {
		t.Fatalf("query = %d, form = %d, want both 7", fromQuery, fromForm)
	}
}

type numberProbe struct {
	value *int64
}

func (p *numberProbe) BindForm(form *Form) error {
	*p.value = form.Int64("n")
	return nil
}
