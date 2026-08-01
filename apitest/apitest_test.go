package apitest_test

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/apitest"
)

// recorder stands in for *testing.T so a test can check that an assertion fails.
//
// testing.TB cannot be implemented outside the testing package, but it can be
// embedded: the unexported method is promoted, and the methods that matter are
// overridden. Fatalf ends the goroutine as the real one does, which is why every
// use runs in its own.
type recorder struct {
	testing.TB
	failed  bool
	message string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatal(args ...any) {
	r.failed = true
	r.message = fmt.Sprint(args...)
	runtime.Goexit()
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// mustFail runs body and returns the failure message, failing the real test when
// body did not fail at all.
func mustFail(t *testing.T, body func(tb testing.TB)) string {
	t.Helper()

	fake := &recorder{TB: t}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		body(fake)
	}()
	group.Wait()

	if !fake.failed {
		t.Fatal("the assertion passed; it should have failed")
	}
	return fake.message
}

// mustPass runs body and fails the real test if it reported anything.
func mustPass(t *testing.T, body func(tb testing.TB)) {
	t.Helper()

	fake := &recorder{TB: t}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		body(fake)
	}()
	group.Wait()

	if fake.failed {
		t.Fatalf("the assertion failed: %s", fake.message)
	}
}

// testApp is the application the assertions are exercised against.
func testApp(t *testing.T) *ossein.App {
	t.Helper()

	app := ossein.New()

	app.Get("/ok", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	app.Get("/text", func(c *ossein.Context) error {
		return c.Text(http.StatusOK, "plain")
	})
	app.Get("/missing", func(c *ossein.Context) error {
		return ossein.NotFound("link_not_found", "Link does not exist")
	})
	app.Get("/whoami", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"key": c.Request.Header.Get("X-API-Key"),
		})
	})
	app.Post("/links", func(c *ossein.Context) error {
		var request createRequest
		if err := c.BindJSON(&request); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, request)
	})
	app.Post("/form", func(c *ossein.Context) error {
		var request formRequest
		if err := c.BindForm(&request); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, request)
	})
	app.Post("/raw", func(c *ossein.Context) error {
		body, err := c.Body()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{
			"type": c.Request.Header.Get("Content-Type"),
			"body": string(body),
		})
	})
	app.Delete("/links/{code}", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	app.Put("/links/{code}", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"code": c.Param("code")})
	})
	app.Patch("/links/{code}", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"code": c.Param("code")})
	})

	return app
}

type createRequest struct {
	Target string `json:"target"`
}

// Validate implements ossein.Validatable.
func (r *createRequest) Validate() error {
	errs := ossein.NewValidationError()
	if r.Target == "" {
		errs.Add("target", "Target is required")
	}
	return errs.OrNil()
}

type formRequest struct {
	Name string `json:"name"`
}

// BindForm implements ossein.FormBindable.
func (r *formRequest) BindForm(form *ossein.Form) error {
	r.Name = form.Required("name")
	return nil
}

func TestClientSendsEveryMethod(t *testing.T) {
	client := apitest.New(t, testApp(t))

	client.Get("/ok").AssertStatus(http.StatusOK)
	client.Delete("/links/abc").AssertStatus(http.StatusNoContent)
	client.PutJSON("/links/abc", map[string]string{}).AssertStatus(http.StatusOK)
	client.PatchJSON("/links/abc", map[string]string{}).AssertStatus(http.StatusOK)

	var created createRequest
	client.PostJSON("/links", createRequest{Target: "https://example.test"}).
		AssertStatus(http.StatusCreated).
		AssertHeader("Content-Type", "application/json; charset=utf-8").
		DecodeJSON(&created)
	if created.Target != "https://example.test" {
		t.Fatalf("target = %q", created.Target)
	}
}

// TestWithHeaderIsInheritedAndIsolated covers the case the helper exists for: an
// API key set once. A client that mutated itself would leak the header into the
// base client and make an unauthenticated test pass.
func TestWithHeaderIsInheritedAndIsolated(t *testing.T) {
	base := apitest.New(t, testApp(t))
	authenticated := base.WithHeader("X-API-Key", "secret")

	var identity struct {
		Key string `json:"key"`
	}
	authenticated.Get("/whoami").DecodeJSON(&identity)
	if identity.Key != "secret" {
		t.Fatalf("key = %q, want the client's header", identity.Key)
	}

	identity.Key = "unset"
	base.Get("/whoami").DecodeJSON(&identity)
	if identity.Key != "" {
		t.Fatalf("the base client sent %q; WithHeader mutated it", identity.Key)
	}

	// And a per-request header wins over the client's.
	request, err := http.NewRequest(http.MethodGet, "/whoami", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("X-API-Key", "override")
	authenticated.Do(request).DecodeJSON(&identity)
	if identity.Key != "override" {
		t.Fatalf("key = %q, want the request's own header", identity.Key)
	}
}

func TestPostFormAndPostRaw(t *testing.T) {
	client := apitest.New(t, testApp(t))

	var form formRequest
	// Two fields, so the separator between them is exercised, and a value needing
	// escaping so the encoding is.
	client.PostForm("/form", url.Values{"name": {"a b&c"}, "other": {"x=y"}}).
		AssertStatus(http.StatusOK).
		DecodeJSON(&form)
	if form.Name != "a b&c" {
		t.Fatalf("name = %q, want the value encoded and decoded intact", form.Name)
	}

	var raw struct {
		Type string `json:"type"`
		Body string `json:"body"`
	}
	client.PostRaw("/raw", "application/octet-stream", []byte("{\"a\":1}")).
		AssertStatus(http.StatusOK).
		DecodeJSON(&raw)
	if raw.Type != "application/octet-stream" {
		t.Fatalf("content type = %q", raw.Type)
	}
	if raw.Body != `{"a":1}` {
		t.Fatalf("body = %q, want the bytes as given", raw.Body)
	}
}

// TestAssertErrorReadsTheEnvelope is what a general HTTP testing library cannot
// do: the code comes from the rendered error document, so a body that merely
// mentions it somewhere does not pass.
func TestAssertErrorReadsTheEnvelope(t *testing.T) {
	client := apitest.New(t, testApp(t))

	client.Get("/missing").AssertError(http.StatusNotFound, "link_not_found")

	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/missing").
			AssertError(http.StatusNotFound, "something_else")
	})
	if !strings.Contains(message, "link_not_found") {
		t.Fatalf("the failure does not report the actual code: %s", message)
	}

	// A wrong status fails before the code is even read.
	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/missing").
			AssertError(http.StatusOK, "link_not_found")
	})
	if !strings.Contains(message, "want 200") {
		t.Fatalf("the failure does not report the status: %s", message)
	}

	// A body that is not an error document fails rather than reporting an empty
	// code, which would let AssertError("") pass against any response.
	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/ok").AssertError(http.StatusOK, "")
	})
	if !strings.Contains(message, "not an error document") {
		t.Fatalf("a success body was accepted as an error: %s", message)
	}
	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/text").AssertError(http.StatusOK, "nope")
	})
	if !strings.Contains(message, "not an error document") {
		t.Fatalf("a text body was accepted as an error: %s", message)
	}
}

func TestAssertFieldError(t *testing.T) {
	client := apitest.New(t, testApp(t))

	client.PostJSON("/links", createRequest{}).
		AssertError(http.StatusUnprocessableEntity, "validation_failed").
		AssertFieldError("target", "required").
		AssertFieldError("target", "")

	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).PostJSON("/links", createRequest{}).
			AssertFieldError("nickname", "")
	})
	if !strings.Contains(message, "[target]") {
		t.Fatalf("the failure does not list the fields that did fail: %s", message)
	}

	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).PostJSON("/links", createRequest{}).
			AssertFieldError("target", "must be a URL")
	})
	if !strings.Contains(message, "Target is required") {
		t.Fatalf("the failure does not report the actual messages: %s", message)
	}
}

// TestAssertionsFailWithTheRequestAndBody covers what makes a failure usable. A
// bare "got 500, want 200" sends someone to add a print statement.
func TestAssertionsFailWithTheRequestAndBody(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/missing").AssertStatus(http.StatusOK)
	})
	for _, expected := range []string{"GET /missing", "= 404", "want 200", "link_not_found"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("the failure does not contain %q: %s", expected, message)
		}
	}

	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/ok").AssertHeader("Content-Type", "text/plain")
	})
	if !strings.Contains(message, "GET /ok") || !strings.Contains(message, "application/json") {
		t.Fatalf("the header failure is not actionable: %s", message)
	}

	message = mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/text").AssertBodyContains("absent")
	})
	if !strings.Contains(message, "plain") {
		t.Fatalf("the body failure does not show the body: %s", message)
	}
}

// TestDecodeJSONReportsABodyItCannotRead covers the failure that matters for the
// lenient decoder: a body that is not JSON at all.
func TestDecodeJSONReportsABodyItCannotRead(t *testing.T) {
	type target struct {
		Missing string `json:"missing"`
	}

	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/text").DecodeJSON(&target{})
	})
	if !strings.Contains(message, "decode body") {
		t.Fatalf("a non-JSON body decoded without complaint: %s", message)
	}
}

// TestBodyIsReadableMoreThanOnce is why the body is cached: an assertion that
// consumed it would leave the next one, or a later decode, with nothing.
func TestBodyIsReadableMoreThanOnce(t *testing.T) {
	response := apitest.New(t, testApp(t)).Get("/ok")

	first := string(response.Body())
	response.AssertBodyContains("ok")

	var decoded map[string]string
	response.DecodeJSON(&decoded)

	if second := string(response.Body()); second != first {
		t.Fatalf("the body changed between reads: %q then %q", first, second)
	}
	if decoded["status"] != "ok" {
		t.Fatalf("decoded = %v", decoded)
	}
}

func TestResponseExposesTheStandardTypes(t *testing.T) {
	response := apitest.New(t, testApp(t)).Get("/ok")

	if response.Status() != http.StatusOK {
		t.Fatalf("Status() = %d", response.Status())
	}
	result := response.Result()
	if result == nil || result.StatusCode != http.StatusOK {
		t.Fatalf("Result() = %+v", result)
	}
	if result.Header.Get("Content-Type") == "" {
		t.Fatal("Result() carries no headers")
	}
}

func TestNewRejectsANilHandler(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, nil)
	})
	if !strings.Contains(message, "handler cannot be nil") {
		t.Fatalf("message = %s", message)
	}
}

func TestDoRejectsANilRequest(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Do(nil)
	})
	if !strings.Contains(message, "request cannot be nil") {
		t.Fatalf("message = %s", message)
	}
}

func TestDecodeJSONRejectsANilTarget(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/ok").DecodeJSON(nil)
	})
	if !strings.Contains(message, "target cannot be nil") {
		t.Fatalf("message = %s", message)
	}
}

func TestPostJSONReportsAnUnencodablePayload(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).PostJSON("/links", make(chan int))
	})
	if !strings.Contains(message, "encode POST /links body") {
		t.Fatalf("message = %s", message)
	}
}

// TestPassingAssertionsReportNothing is the other half: an assertion that failed
// on a correct response would be worse than none.
func TestPassingAssertionsReportNothing(t *testing.T) {
	mustPass(t, func(tb testing.TB) {
		client := apitest.New(tb, testApp(t))
		client.Get("/ok").
			AssertStatus(http.StatusOK).
			AssertHeader("Content-Type", "application/json; charset=utf-8").
			AssertBodyContains("ok")
		client.Get("/missing").AssertError(http.StatusNotFound, "link_not_found")
		client.PostJSON("/links", createRequest{}).AssertFieldError("target", "required")
	})
}
