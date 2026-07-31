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

// committedPreflight returns the headers a client would actually receive, taken at
// the moment the response was committed rather than from the live map.
func committedPreflight(t *testing.T, app *ossein.App, origin, method string) http.Header {
	t.Helper()
	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", method)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	return response.Result().Header
}

// TestCORSPreflightHeadersReachTheClient asserts on the committed response. Reading
// the live header map cannot tell whether the headers were written before the
// response was committed, so a middleware that set them afterwards — sending a bare
// 204 and breaking every cross-origin request — would pass.
func TestCORSPreflightHeadersReachTheClient(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedMethods: []string{http.MethodGet, http.MethodPost},
		MaxAge:         time.Minute,
	})

	header := committedPreflight(t, app, "https://app.test", http.MethodPost)

	if got := header.Get("Access-Control-Allow-Origin"); got != "https://app.test" {
		t.Fatalf("Allow-Origin = %q on the committed response", got)
	}
	if got := header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Fatalf("Allow-Methods = %q on the committed response", got)
	}
	if got := header.Get("Access-Control-Max-Age"); got != "60" {
		t.Fatalf("Max-Age = %q on the committed response", got)
	}
}

// TestCORSRejectsAnAllowAllOriginFuncWithCredentials closes the asymmetry that made
// the wildcard guard misleading. A wildcard with credentials is inert, because
// browsers refuse the pair; a function reflecting every origin with credentials
// works, and hands any site authenticated read access.
func TestCORSRejectsAnAllowAllOriginFuncWithCredentials(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected a panic for an allow-all origin function with credentials")
		}
		if message, ok := recovered.(string); ok && !strings.Contains(message, "credential") {
			t.Fatalf("panic = %q, want it to name the problem", message)
		}
	}()

	middleware.CORS(middleware.CORSOptions{
		AllowOriginFunc:  func(string) bool { return true },
		AllowCredentials: true,
	})
}

// TestCORSAllowsASelectiveOriginFuncWithCredentials keeps the guard from rejecting a
// function that actually restricts anything.
func TestCORSAllowsASelectiveOriginFuncWithCredentials(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowOriginFunc: func(origin string) bool {
			return origin == "https://tenant.app.test"
		},
		AllowCredentials: true,
	})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://tenant.app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "https://tenant.app.test" {
		t.Fatalf("Allow-Origin = %q", got)
	}
}

// TestCORSRejectsANullOriginWithCredentials refuses to hand credentialed access to
// the opaque origin sandboxed frames and data URLs send, which is not a principal
// that can be authenticated.
func TestCORSRejectsANullOriginWithCredentials(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic for a null origin with credentials")
		}
	}()

	middleware.CORS(middleware.CORSOptions{
		AllowedOrigins:   []string{"null"},
		AllowCredentials: true,
	})
}

// TestCORSOmitsCredentialsWhenNotConfigured pins the negative direction: sending the
// credentials header unasked would grant every allowed origin authenticated read
// access the operator never configured.
func TestCORSOmitsCredentialsWhenNotConfigured(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials = %q on an actual request, want it absent", got)
	}

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	if got := header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials = %q on a preflight, want it absent", got)
	}
}

// TestCORSCredentialedPreflightSendsTheHeader covers the preflight half, which had no
// coverage at all.
func TestCORSCredentialedPreflightSendsTheHeader(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins:   []string{"https://app.test"},
		AllowCredentials: true,
	})

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	if got := header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q on a credentialed preflight", got)
	}
}

// TestCORSNormalisesConfiguredMethods keeps a lower-case configuration from producing
// a preflight that looks approved and is then rejected by the browser, which compares
// methods byte for byte.
func TestCORSNormalisesConfiguredMethods(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedMethods: []string{"get", "patch"},
	})

	header := committedPreflight(t, app, "https://app.test", http.MethodPatch)

	allowed := header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowed, "PATCH") {
		t.Fatalf("Allow-Methods = %q, want the upper-case form a browser will match",
			allowed)
	}
	if strings.Contains(allowed, "patch") {
		t.Fatalf("Allow-Methods = %q, want no lower-case form", allowed)
	}
}

// TestCORSApprovesALowerCaseRequestedMethod keeps a client sending an unusual case
// from being refused, since the configured set is what defines policy.
func TestCORSApprovesALowerCaseRequestedMethod(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		AllowedMethods: []string{http.MethodPatch},
	})

	header := committedPreflight(t, app, "https://app.test", "PATCH")
	if got := header.Get("Access-Control-Allow-Origin"); got == "" {
		t.Fatal("a configured method was not approved")
	}
}

// TestCORSPreflightIsLoggedWhenAccessLogIsOutside pins the ordering the docs must
// recommend: the preflight is answered without reaching anything below, so a log
// registered inside CORS never sees it.
func TestCORSPreflightIsLoggedWhenAccessLogIsOutside(t *testing.T) {
	logger, logs := logCapture()

	app := ossein.New(ossein.WithLogger(logger))
	app.Use(
		middleware.AccessLog(),
		middleware.CORS(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}}),
	)
	app.Get("/items", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	app.ServeHTTP(httptest.NewRecorder(), request)

	if !strings.Contains(logs.String(), "status=204") {
		t.Fatalf("log = %q, want the preflight recorded", logs.String())
	}
}

// TestCORSPreflightPreservesAnExistingVary covers the path where Vary matters most,
// which the actual-request test does not reach.
func TestCORSPreflightPreservesAnExistingVary(t *testing.T) {
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

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	vary := strings.Join(header.Values("Vary"), ", ")
	if !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want the existing value kept", vary)
	}
	if !strings.Contains(vary, "Origin") {
		t.Fatalf("Vary = %q, want Origin added", vary)
	}
}

// TestCORSVariesEvenForARejectedOrigin keeps a shared cache from storing a
// header-less response under a key that ignores the origin.
func TestCORSVariesEvenForARejectedOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if vary := strings.Join(response.Result().Header.Values("Vary"), ", "); !strings.Contains(vary, "Origin") {
		t.Fatalf("Vary = %q on a rejected origin, want Origin", vary)
	}

	rejected := committedPreflight(t, app, "https://evil.test", http.MethodGet)
	if vary := strings.Join(rejected.Values("Vary"), ", "); !strings.Contains(vary, "Origin") {
		t.Fatalf("Vary = %q on a rejected preflight, want Origin", vary)
	}
}

// TestCORSSendsExactlyOneAllowOrigin keeps a duplicate header, which browsers reject
// outright, from being produced when something upstream already set one.
func TestCORSSendsExactlyOneAllowOrigin(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "https://legacy.test")
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

	values := response.Result().Header.Values("Access-Control-Allow-Origin")
	if len(values) != 1 {
		t.Fatalf("Allow-Origin = %v, want exactly one value", values)
	}
	if values[0] != "https://app.test" {
		t.Fatalf("Allow-Origin = %q", values[0])
	}
}

// TestCORSClearsStaleHeadersForARejectedOrigin keeps a permissive value from an outer
// middleware or an edge proxy from surviving on a response this middleware refused.
func TestCORSClearsStaleHeadersForARejectedOrigin(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
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
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	header := response.Result().Header
	if got := header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want the stale value cleared", got)
	}
	if got := header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials = %q, want the stale value cleared", got)
	}
}

// TestCORSClearsStaleHeadersOnARejectedPreflight is the preflight half of the
// stale-header case. The response is short-circuited here, so a permissive value set
// upstream would be the last word.
func TestCORSClearsStaleHeadersOnARejectedPreflight(t *testing.T) {
	app := ossein.New()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Expose-Headers", "X-Secret")
			next.ServeHTTP(w, r)
		})
	})
	app.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
	}))
	app.Get("/items", func(c *ossein.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	header := committedPreflight(t, app, "https://evil.test", http.MethodGet)

	for _, name := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",
	} {
		if got := header.Get(name); got != "" {
			t.Fatalf("%s = %q on a rejected preflight, want it cleared", name, got)
		}
	}
}

// TestCORSMaxAgeRoundsSubSecondValuesUp keeps a short cache lifetime from becoming
// zero, which tells a browser not to cache at all — the opposite of the request.
func TestCORSMaxAgeRoundsSubSecondValuesUp(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		MaxAge:         500 * time.Millisecond,
	})

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	if got := header.Get("Access-Control-Max-Age"); got != "1" {
		t.Fatalf("Max-Age = %q, want a sub-second value rounded up to 1", got)
	}
}

// TestCORSMaxAgeIsAbsentByDefault keeps the browser's own default in place.
func TestCORSMaxAgeIsAbsentByDefault(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	if got := header.Get("Access-Control-Max-Age"); got != "" {
		t.Fatalf("Max-Age = %q, want it absent by default", got)
	}
}

// TestCORSNegativeMaxAgeDisablesCaching gives an explicit way to say "do not cache".
func TestCORSNegativeMaxAgeDisablesCaching(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		MaxAge:         -1,
	})

	header := committedPreflight(t, app, "https://app.test", http.MethodGet)
	if got := header.Get("Access-Control-Max-Age"); got != "0" {
		t.Fatalf("Max-Age = %q, want 0 for a negative configuration", got)
	}
}

// TestCORSEchoDropsAWildcardHeaderRequest keeps the convenience echo from turning
// into a blanket grant a page asked itself for.
func TestCORSEchoDropsAWildcardHeaderRequest(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"https://app.test"}})

	request := httptest.NewRequest(http.MethodOptions, "/items", nil)
	request.Header.Set("Origin", "https://app.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "X-Api-Key, *")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	allowed := response.Result().Header.Get("Access-Control-Allow-Headers")
	if strings.Contains(allowed, "*") {
		t.Fatalf("Allow-Headers = %q, want the wildcard dropped", allowed)
	}
	if !strings.Contains(allowed, "X-Api-Key") {
		t.Fatalf("Allow-Headers = %q, want the named header kept", allowed)
	}
}

// TestCORSDoesNotFollowLaterMutationOfTheCallersSlice keeps a caller reusing its
// slice from silently rewriting the allowlist while the server runs.
func TestCORSDoesNotFollowLaterMutationOfTheCallersSlice(t *testing.T) {
	origins := []string{"https://app.test"}
	app := corsApp(middleware.CORSOptions{AllowedOrigins: origins})
	origins[0] = "https://evil.test"

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want the configuration captured at setup", got)
	}
}

// TestCORSWildcardPreflightSendsAStar covers the wildcard on the preflight path.
func TestCORSWildcardPreflightSendsAStar(t *testing.T) {
	app := corsApp(middleware.CORSOptions{AllowedOrigins: []string{"*"}})

	header := committedPreflight(t, app, "https://anywhere.test", http.MethodGet)
	if got := header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

// TestCORSDoesNotSendExposedHeadersToARejectedOrigin keeps the response surface from
// being described to an origin that may not read it.
func TestCORSDoesNotSendExposedHeadersToARejectedOrigin(t *testing.T) {
	app := corsApp(middleware.CORSOptions{
		AllowedOrigins: []string{"https://app.test"},
		ExposedHeaders: []string{"X-Total-Count"},
	})

	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("Origin", "https://evil.test")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if got := response.Result().Header.Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("Expose-Headers = %q, want nothing for a rejected origin", got)
	}
}
