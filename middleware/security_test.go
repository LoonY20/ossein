package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

func TestSecurityHeadersSetsDefaults(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders())
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	expected := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, want := range expected {
		if got := response.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestSecurityHeadersApplyToErrorResponses keeps the headers on the responses that
// matter most, which are produced after the handler returns.
func TestSecurityHeadersApplyToErrorResponses(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders())
	app.Get("/known", func(*ossein.Context) error {
		return ossein.NotFound("missing", "missing")
	})

	for _, target := range []string{"/known", "/unmatched"} {
		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))

		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", target, response.Code)
		}
		if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", target, got)
		}
	}
}

// TestSecurityHeadersDoNotOverrideAnEarlierValue pins the only case that can
// distinguish the behaviour. A handler setting the header wins regardless, because
// it runs later; a value set by an *outer* middleware is overwritten unless the
// header is only filled in when absent.
func TestSecurityHeadersDoNotOverrideAnEarlierValue(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			next.ServeHTTP(w, r)
		})
	})
	app.Use(middleware.SecurityHeaders())
	app.Get("/embed", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/embed", nil))

	if got := response.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want the value set before this middleware", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want the untouched default", got)
	}
}

// TestSecurityHeadersLeaveAHandlerInControl covers the ordinary case of an endpoint
// that needs a different policy.
func TestSecurityHeadersLeaveAHandlerInControl(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders())
	app.Get("/embed", func(c *ossein.Context) error {
		c.Response.Header().Set("X-Frame-Options", "SAMEORIGIN")
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/embed", nil))

	if got := response.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want the handler's value", got)
	}
}

// TestSecurityHeadersAcceptOverrides covers configuring the policy once for the
// application.
func TestSecurityHeadersAcceptOverrides(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeaderValues(map[string]string{
		"X-Frame-Options":           "SAMEORIGIN",
		"Strict-Transport-Security": "max-age=63072000",
	})))
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "max-age=63072000" {
		t.Fatalf("Strict-Transport-Security = %q", got)
	}
	// Defaults not overridden stay in place.
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

// TestSecurityHeadersCanRemoveADefault covers turning one off with an empty value.
func TestSecurityHeadersCanRemoveADefault(t *testing.T) {
	app := ossein.New()
	app.Use(middleware.SecurityHeaders(middleware.SecurityHeaderValues(map[string]string{
		"Referrer-Policy": "",
	})))
	app.Get("/", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header().Get("Referrer-Policy"); got != "" {
		t.Fatalf("Referrer-Policy = %q, want it removed", got)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want the remaining default", got)
	}
}
