package apitest_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/apitest"
)

// TestWithTRetargetsFailuresAtASubtest covers the mistake the package invites: a
// client held across a t.Run boundary calls the parent's FailNow, which files the
// failure under the parent, skips the remaining subtests, and under t.Parallel
// takes the process down.
func TestWithTRetargetsFailuresAtASubtest(t *testing.T) {
	base := apitest.New(t, testApp(t))

	t.Run("child", func(t *testing.T) {
		message := mustFail(t, func(tb testing.TB) {
			base.WithT(tb).Get("/ok").AssertStatus(http.StatusTeapot)
		})
		if !strings.Contains(message, "want 418") {
			t.Fatalf("the failure did not reach the derived test: %s", message)
		}
	})

	t.Run("sibling still runs", func(t *testing.T) {
		base.WithT(t).Get("/ok").AssertStatus(http.StatusOK)
	})

	// The derived client keeps the headers and handler it came from.
	authenticated := base.WithHeader("X-API-Key", "secret").WithT(t)
	var identity struct {
		Key string `json:"key"`
	}
	authenticated.Get("/whoami").DecodeJSON(&identity)
	if identity.Key != "secret" {
		t.Fatalf("key = %q, want the derived client to keep its headers", identity.Key)
	}

	if message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).WithT(nil)
	}); !strings.Contains(message, "t cannot be nil") {
		t.Fatalf("message = %s", message)
	}
}

// TestResultBodyIsReadableAlongsideTheAssertions covers a footgun that made both
// halves of "nothing is hidden" false: Result handed out the very reader the
// assertions drain, so mixing them silently read nothing.
func TestResultBodyIsReadableAlongsideTheAssertions(t *testing.T) {
	response := apitest.New(t, testApp(t)).Get("/ok").AssertStatus(http.StatusOK)

	first, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read Result body: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("Result body was empty after an assertion had read it")
	}

	second, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read Result body again: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("Result body changed between reads: %q then %q", first, second)
	}

	// And the other order: reading Result first must not empty the assertions.
	fresh := apitest.New(t, testApp(t)).Get("/ok")
	if _, err := io.ReadAll(fresh.Result().Body); err != nil {
		t.Fatalf("read: %v", err)
	}
	fresh.AssertBodyContains("ok")
}

// TestAMalformedTargetFailsTheTest keeps the most likely typo in a test — a path
// with no leading slash — from panicking the whole binary with no test named.
func TestAMalformedTargetFailsTheTest(t *testing.T) {
	for _, path := range []string{"", "links/abc", "/ q"} {
		message := mustFail(t, func(tb testing.TB) {
			apitest.New(tb, testApp(t)).Get(path)
		})
		if !strings.Contains(message, "not a usable request target") {
			t.Fatalf("path %q produced %q", path, message)
		}
	}
}

// TestDoDoesNotMutateTheCallersRequest covers a leak that would make an
// unauthenticated test pass: defaults written into the caller's request travel
// with it to whatever uses it next.
func TestDoDoesNotMutateTheCallersRequest(t *testing.T) {
	app := testApp(t)
	authenticated := apitest.New(t, app).WithHeader("X-API-Key", "secret")
	anonymous := apitest.New(t, app)

	request := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	authenticated.Do(request)

	if got := request.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("the caller's request carries %q after Do", got)
	}

	var identity struct {
		Key string `json:"key"`
	}
	anonymous.Do(request).DecodeJSON(&identity)
	if identity.Key != "" {
		t.Fatalf("an anonymous client sent %q; the key leaked through the request",
			identity.Key)
	}
}

// TestAnExplicitlyEmptyHeaderIsNotOverridden covers the difference between a
// header that is absent and one deliberately set to nothing. Appending to the
// latter produces a two-valued header no client would send.
func TestAnExplicitlyEmptyHeaderIsNotOverridden(t *testing.T) {
	client := apitest.New(t, testApp(t)).WithHeader("X-API-Key", "secret")

	request := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	request.Header["X-Api-Key"] = []string{""}

	var identity struct {
		Key string `json:"key"`
	}
	client.Do(request).DecodeJSON(&identity)
	if identity.Key != "" {
		t.Fatalf("key = %q, want the caller's empty header respected", identity.Key)
	}
}

// TestAssertErrorMatchesTheCodeExactly is the package's reason to exist: a code
// that merely contains the expected one is a different error.
func TestAssertErrorMatchesTheCodeExactly(t *testing.T) {
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/missing").
			AssertError(http.StatusNotFound, "not_found")
	})
	if !strings.Contains(message, "link_not_found") {
		t.Fatalf("a substring of the code was accepted: %s", message)
	}
}

// TestAssertErrorAcceptsAnEmptyCode covers "some framework error, whatever its
// code", and the envelope with an empty code that used to be reported as not
// being an error document at all.
func TestAssertErrorAcceptsAnEmptyCode(t *testing.T) {
	app := ossein.New()
	app.Get("/nocode", func(c *ossein.Context) error {
		return ossein.NotFound("", "no code")
	})

	mustPass(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/nocode").AssertError(http.StatusNotFound, "")
	})

	// And a body with no error object at all is still rejected.
	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, testApp(t)).Get("/ok").AssertError(http.StatusOK, "")
	})
	if !strings.Contains(message, "not an error document") {
		t.Fatalf("message = %s", message)
	}
}

// TestAssertHeaderIsExactAndSeesEveryValue covers a header sent twice, where
// checking only the first would let the second go unnoticed.
func TestAssertHeaderIsExactAndSeesEveryValue(t *testing.T) {
	app := ossein.New()
	app.Get("/cookies", func(c *ossein.Context) error {
		c.Response.Header().Add("Set-Cookie", "a=1")
		c.Response.Header().Add("Set-Cookie", "b=2")
		return c.NoContent(http.StatusNoContent)
	})

	mustPass(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/cookies").AssertHeader("Set-Cookie", "a=1", "b=2")
	})

	// Only the first value is not enough.
	if message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/cookies").AssertHeader("Set-Cookie", "a=1")
	}); !strings.Contains(message, "b=2") {
		t.Fatalf("the failure hides the second value: %s", message)
	}

	// And a value that merely contains the expected one is a different value.
	if message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/cookies").AssertHeader("Set-Cookie", "a=", "b=2")
	}); !strings.Contains(message, "a=1") {
		t.Fatalf("a substring was accepted: %s", message)
	}
}

// TestAssertFieldErrorRequiresTheValidationStatus keeps it from passing against a
// success whose body happens to carry a validation document.
func TestAssertFieldErrorRequiresTheValidationStatus(t *testing.T) {
	app := ossein.New()
	app.Get("/oops", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"error": map[string]any{
				"code":   "validation_failed",
				"fields": map[string][]string{"target": {"Target is required"}},
			},
		})
	})

	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/oops").AssertFieldError("target", "")
	})
	if !strings.Contains(message, "want 422") {
		t.Fatalf("a 200 passed a field assertion: %s", message)
	}
}

// TestDecodeJSONIgnoresFieldsTheTargetDoesNotDeclare is the ordinary case: a test
// that cares about one field should not have to mirror the whole document, and a
// field added to a response is not a regression.
func TestDecodeJSONIgnoresFieldsTheTargetDoesNotDeclare(t *testing.T) {
	// The response has two fields; the target declares one, which is what a test
	// that cares about one field looks like.
	var partial struct {
		Type string `json:"type"`
	}
	apitest.New(t, testApp(t)).
		PostRaw("/raw", "text/csv", []byte("a,b")).
		DecodeJSON(&partial)
	if partial.Type != "text/csv" {
		t.Fatalf("type = %q", partial.Type)
	}

	// Strictness is available when the shape is the contract.
	message := mustFail(t, func(tb testing.TB) {
		var target struct {
			Missing string `json:"missing"`
		}
		apitest.New(tb, testApp(t)).Get("/ok").DecodeJSONStrict(&target)
	})
	if !strings.Contains(message, "unknown field") {
		t.Fatalf("DecodeJSONStrict accepted an unknown field: %s", message)
	}
}

// TestDecodeJSONRejectsTrailingContent keeps a body that is two documents from
// decoding as the first one and passing.
func TestDecodeJSONRejectsTrailingContent(t *testing.T) {
	app := ossein.New()
	app.Get("/two", func(c *ossein.Context) error {
		return c.Blob(http.StatusOK, "application/json", []byte(`{"a":1}{"a":2}`))
	})

	message := mustFail(t, func(tb testing.TB) {
		var target struct {
			A int `json:"a"`
		}
		apitest.New(tb, app).Get("/two").DecodeJSON(&target)
	})
	if !strings.Contains(message, "more than one JSON value") {
		t.Fatalf("message = %s", message)
	}
}

// TestPostFormSendsRepeatedFields covers what a map could not express at all.
func TestPostFormSendsRepeatedFields(t *testing.T) {
	app := ossein.New()
	app.Post("/tags", func(c *ossein.Context) error {
		if err := c.Request.ParseForm(); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string][]string{"tags": c.Request.PostForm["tag"]})
	})

	var received struct {
		Tags []string `json:"tags"`
	}
	apitest.New(t, app).
		PostForm("/tags", url.Values{"tag": {"a", "b"}}).
		AssertStatus(http.StatusOK).
		DecodeJSON(&received)

	if len(received.Tags) != 2 || received.Tags[0] != "a" || received.Tags[1] != "b" {
		t.Fatalf("tags = %q, want both values", received.Tags)
	}
}

// TestNewRejectsANilTest covers the argument beside the handler, which was guarded
// while this one panicked on the first call it made.
func TestNewRejectsANilTest(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("a nil test was accepted")
		}
		if !strings.Contains(fmt.Sprint(recovered), "t cannot be nil") {
			t.Fatalf("panic = %v", recovered)
		}
	}()

	apitest.New(nil, testApp(t))
}

// TestDoAppliesDefaultsToARequestWithNoHeaders covers the request shape that has a
// nil header map, which a hand-built http.Request can have.
func TestDoAppliesDefaultsToARequestWithNoHeaders(t *testing.T) {
	client := apitest.New(t, testApp(t)).WithHeader("X-API-Key", "secret")

	request := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	request.Header = nil

	var identity struct {
		Key string `json:"key"`
	}
	client.Do(request).DecodeJSON(&identity)
	if identity.Key != "secret" {
		t.Fatalf("key = %q, want the client's default applied", identity.Key)
	}
}

// TestAssertErrorReportsABodyThatDoesNotDecode separates a document with an error
// object of the wrong shape from one with no error object at all, since the two
// need different explanations.
func TestAssertErrorReportsABodyThatDoesNotDecode(t *testing.T) {
	app := ossein.New()
	app.Get("/odd", func(c *ossein.Context) error {
		return c.Blob(http.StatusBadRequest, "application/json", []byte(`{"error":"a string"}`))
	})

	message := mustFail(t, func(tb testing.TB) {
		apitest.New(tb, app).Get("/odd").AssertError(http.StatusBadRequest, "x")
	})
	if !strings.Contains(message, "does not decode") {
		t.Fatalf("message = %s", message)
	}
}
