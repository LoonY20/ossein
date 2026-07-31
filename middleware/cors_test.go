package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/middleware"
)

func corsApp(options middleware.CORSOptions) *ossein.App {
	app := ossein.New()
	app.Use(middleware.CORS(options))
	app.Get("/items", func(c *ossein.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "yes"})
	})
	app.Post("/items", func(c *ossein.Context) error {
		return c.NoContent(http.StatusCreated)
	})
	return app
}

func preflight(t *testing.T, app *ossein.App, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", method)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	return response
}

// TestCORSAnswersPreflight is the concrete gap from the field notes: an OPTIONS
// preflight matches no route, so without this it answered 405 in plain text.
func TestCORSAnswersPreflight(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedMethods: []string{http.MethodGet, http.MethodPost},
	})

	response := preflight(t, app, "https://app.test", http.MethodPost)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.test" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Fatalf("Allow-Methods = %q", got)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", response.Body.String())
	}
}

// TestCORSPreflightDoesNotReachTheRouter keeps the short-circuit honest: the router
// has no OPTIONS route, so anything reaching it would answer 405.
func TestCORSPreflightDoesNotReachTheRouter(t *testing.T) {
	var reached bool
	app := ossein.New()
	app.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
	}))
	app.Get("/items", func(c *ossein.Context) error {
		reached = true
		return c.NoContent(http.StatusOK)
	})

	response := preflight(t, app, "https://app.test", http.MethodGet)

	if reached {
		t.Fatal("the preflight reached a route handler")
	}
	if response.Code == http.StatusMethodNotAllowed {
		t.Fatal("the preflight fell through to the router's 405")
	}
}

func TestCORSAddsHeadersToAnActualRequest(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		ExposedHeaders: []string{"X-Total-Count"},
	})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "https://app.test" {
		t.Fatalf("Allow-Origin = %q on the committed response", got)
	}
	if got := response.Result().Header.Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Total-Count") {
		t.Fatalf("Expose-Headers = %q", got)
	}
	if !strings.Contains(response.Body.String(), `"ok":"yes"`) {
		t.Fatalf("body = %q, want the handler's response", response.Body.String())
	}
}

// TestCORSVariesOnOrigin keeps a shared cache from serving one origin's response to
// another.
func TestCORSVariesOnOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if vary := response.Result().Header.Get("Vary"); !strings.Contains(vary, "Origin") {
		t.Fatalf("Vary = %q, want Origin", vary)
	}
}

// TestCORSRejectsAnUnknownOrigin keeps the browser from being told a foreign origin
// is allowed. The request still reaches the handler; only the browser enforces CORS.
func TestCORSRejectsAnUnknownOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want nothing for a foreign origin", got)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the handler still to run", response.Code)
	}
}

// TestCORSPreflightForAnUnknownOriginSendsNoHeaders keeps a foreign preflight from
// being approved while still not leaking whether the route exists.
func TestCORSPreflightForAnUnknownOriginSendsNoHeaders(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	response := preflight(t, app, "https://evil.test", http.MethodGet)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want nothing", got)
	}
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 regardless", response.Code)
	}
}

// TestCORSPreflightForADisallowedMethodSendsNoHeaders keeps a method outside the
// configured set from being approved.
func TestCORSPreflightForADisallowedMethodSendsNoHeaders(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedMethods: []string{http.MethodGet},
	})

	response := preflight(t, app, "https://app.test", http.MethodDelete)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want nothing for a disallowed method", got)
	}
}

func TestCORSIgnoresRequestsWithoutAnOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"*"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want nothing for a same-origin request", got)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestCORSWildcardOriginSendsAStar(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"*"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://anywhere.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

// TestCORSRejectsWildcardWithCredentials refuses the single most damaging CORS
// misconfiguration: any site making authenticated requests with the user's cookies.
// The specification forbids the pair, and silently echoing the origin instead would
// turn "allow all" into "allow all, with credentials".
func TestCORSRejectsWildcardWithCredentials(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected a panic for a wildcard origin with credentials")
		}
		if message, ok := recovered.(string); ok && !strings.Contains(message, "credential") {
			t.Fatalf("panic = %q, want it to name the problem", message)
		}
	}()

	middleware.CORS(middleware.CORSOptions{
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
	})
}

// TestCORSWithCredentialsEchoesTheOrigin keeps the response usable: a credentialed
// response may never carry a wildcard.
func TestCORSWithCredentialsEchoesTheOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins:   []string{"https://app.test"},
		AllowCredentials: true,
	})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	header := response.Result().Header
	if got := header.Get("Access-Control-Allow-Origin"); got != "https://app.test" {
		t.Fatalf("Allow-Origin = %q, want the concrete origin", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q", got)
	}
}

// TestCORSAllowOriginFuncCoversDynamicAllowlists covers subdomains and allowlists
// held elsewhere, without inventing a pattern syntax.
func TestCORSAllowOriginFuncCoversDynamicAllowlists(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowOriginFunc: func(origin string) bool {
			return strings.HasSuffix(origin, ".app.test")
		},
	})

	for origin, want := range map[string]string{
		"https://tenant.app.test": "https://tenant.app.test",
		"https://evil.test":       "",
	} {
		request := httptest.NewRequest(http.MethodGet, "/items", nil)
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)

		if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Fatalf("origin %q: Allow-Origin = %q, want %q", origin, got, want)
		}
	}
}

func TestCORSPreflightEchoesRequestedHeadersWhenUnconfigured(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "X-Api-Key, Content-Type")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Api-Key") {
		t.Fatalf("Allow-Headers = %q, want the requested headers echoed", got)
	}
}

func TestCORSPreflightUsesConfiguredHeaders(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedHeaders: []string{"X-Api-Key"},
	})

	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "X-Secret")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	allowed := response.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allowed, "X-Api-Key") {
		t.Fatalf("Allow-Headers = %q, want the configured list", allowed)
	}
	if strings.Contains(allowed, "X-Secret") {
		t.Fatalf("Allow-Headers = %q, want the requested header not echoed", allowed)
	}
}

func TestCORSPreflightSendsMaxAge(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		MaxAge:         10 * time.Minute,
	})

	response := preflight(t, app, "https://app.test", http.MethodGet)

	if got := response.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("Max-Age = %q, want 600 seconds", got)
	}
}

// TestCORSPreflightVariesOnItsInputs keeps a cached preflight from being reused for
// a different method or header set.
func TestCORSPreflightVariesOnItsInputs(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	response := preflight(t, app, "https://app.test", http.MethodGet)

	// Vary may arrive as several header lines, which is equivalent to one joined
	// list; adding rather than setting also preserves a Vary an outer middleware
	// already sent.
	vary := strings.Join(response.Result().Header.Values("Vary"), ", ")
	for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		if !strings.Contains(vary, want) {
			t.Fatalf("Vary = %q, want %q", vary, want)
		}
	}
}

// TestCORSPreservesAnExistingVary keeps a Vary set elsewhere from being lost.
func TestCORSPreservesAnExistingVary(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Accept-Encoding")
			next.ServeHTTP(w, r)
		})
	})
	app.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
	}))
	app.Get("/items", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	vary := strings.Join(response.Result().Header.Values("Vary"), ", ")
	if !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want the existing value kept", vary)
	}
	if !strings.Contains(vary, "Origin") {
		t.Fatalf("Vary = %q, want Origin added", vary)
	}
}

// TestCORSOptionsRequestWithoutPreflightHeadersIsRouted keeps a plain OPTIONS
// request, which is not a preflight, out of the short-circuit.
func TestCORSOptionsRequestWithoutPreflightHeadersIsRouted(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	// No route serves OPTIONS, so the framework's own fallback answers.
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want the router's 405 for a non-preflight OPTIONS",
			response.Code)
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "json") {
		t.Fatalf("Content-Type = %q, want the error contract", response.Header().Get("Content-Type"))
	}
}

// TestCORSDefaultsToTheCommonMethods keeps an unconfigured middleware useful.
func TestCORSDefaultsToTheCommonMethods(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete,
	} {
		response := preflight(t, app, "https://app.test", method)
		if got := response.Header().Get("Access-Control-Allow-Origin"); got == "" {
			t.Fatalf("method %s was not allowed by default", method)
		}
	}
}

// TestCORSRejectsAnEmptyConfiguration keeps a middleware that can never allow
// anything from being registered silently.
func TestCORSRejectsAnEmptyConfiguration(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic when no origin can ever be allowed")
		}
	}()
	middleware.CORS(middleware.CORSOptions{})
}
