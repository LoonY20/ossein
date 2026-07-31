package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// nullOrigin is the opaque origin sandboxed frames, data URLs, and cross-origin
// redirects send. It identifies no principal, so it cannot be authenticated.
const nullOrigin = "null"

// credentialsProbeOrigin is used once at setup to detect an AllowOriginFunc that
// approves everything. The .invalid domain is reserved and can never be a real
// origin, so a function that accepts it accepts anything.
const credentialsProbeOrigin = "https://ossein-credentials-probe.invalid"

// CORSOptions configures cross-origin access.
type CORSOptions struct {
	// AllowedOrigins lists origins allowed to read responses, matched exactly:
	// browsers send an ASCII-lowercase origin with no default port and no trailing
	// slash, and an internationalised host arrives in punycode. A single "*" allows
	// any origin, which cannot be combined with AllowCredentials.
	AllowedOrigins []string

	// AllowOriginFunc decides origins that AllowedOrigins does not list, for
	// subdomains or an allowlist held elsewhere. It receives the Origin header
	// verbatim, including the literal "null".
	//
	// A function that returns true for every origin, combined with
	// AllowCredentials, is the vulnerability the wildcard guard exists to prevent —
	// and unlike a wildcard, browsers honour it, so any site could read
	// authenticated responses. Match on the whole origin, not a suffix:
	// "https://evil-app.test" ends with "app.test".
	AllowOriginFunc func(origin string) bool

	// AllowedMethods lists methods a preflight may approve. Empty means the common
	// set: GET, HEAD, POST, PUT, PATCH, DELETE. Values are upper-cased, because a
	// browser compares the approved list byte for byte.
	AllowedMethods []string

	// AllowedHeaders lists request headers a preflight may approve. Empty echoes
	// whatever the preflight asked for, which places no restriction on request
	// headers; they are not themselves a credential.
	AllowedHeaders []string

	// ExposedHeaders lists response headers a browser may read beyond the handful
	// it exposes by default.
	ExposedHeaders []string

	// AllowCredentials permits cookies and HTTP authentication on cross-origin
	// requests. It cannot be combined with a wildcard origin, the null origin, or an
	// AllowOriginFunc that approves everything.
	AllowCredentials bool

	// MaxAge is how long a browser may cache a preflight result. Zero omits the
	// header, leaving the browser's own default; a negative value sends zero, which
	// asks the browser not to cache at all. Sub-second values round up.
	MaxAge time.Duration
}

// defaultCORSMethods is the set a preflight approves when none is configured.
var defaultCORSMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// corsPolicy is the configuration resolved once at setup, so nothing is recomputed
// per request and a caller mutating its slices afterwards cannot change the policy.
type corsPolicy struct {
	origins     []string
	allowOrigin func(string) bool
	wildcard    bool
	methods     []string
	methodList  string
	headerList  string
	exposedList string
	credentials bool
	maxAge      string
}

// CORS answers cross-origin preflight requests and adds the response headers a
// browser needs.
//
// A preflight is short-circuited with 204, which is necessary rather than merely
// convenient: an OPTIONS request matches no route, so without this it would be
// answered by the router as a 405. For the same reason CORS belongs in App.Use
// rather than on a group, since group middleware does not run for a request that
// matches no route in the group; registered on a group, simple requests get their
// headers while preflights still fail.
//
// Register it inside AccessLog. The preflight is answered without reaching anything
// below this middleware, so a log registered further in never sees it.
//
// A request without an Origin header is not a cross-origin request and passes
// through untouched. An origin that is not allowed is also passed through, without
// the headers that would let a browser read the response — enforcement is the
// browser's job, and refusing to serve the request would break same-origin clients
// that happen to send an Origin. That also means CORS is not CSRF protection: a
// simple cross-origin request needs no preflight, so it still reaches the handler
// whatever the browser does with the response.
//
// Vary is added, never replaced, on every origin-dependent response, so a value set
// elsewhere survives and a shared cache cannot serve one origin's response to
// another.
//
// CORS panics during setup for configurations that cannot be served safely: a
// configuration that can never allow anything, and credentials combined with a
// wildcard origin, the null origin, or an origin function that approves everything.
func CORS(options CORSOptions) ossein.Middleware {
	policy := resolveCORSPolicy(options)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			header := w.Header()
			allowed := policy.allows(origin)

			if isPreflight(r) {
				header.Add("Vary", "Origin")
				header.Add("Vary", "Access-Control-Request-Method")
				header.Add("Vary", "Access-Control-Request-Headers")

				if allowed && policy.allowsMethod(r.Header.Get("Access-Control-Request-Method")) {
					policy.writeAllowOrigin(header, origin)
					header.Set("Access-Control-Allow-Methods", policy.methodList)
					if headers := policy.headersFor(r); headers != "" {
						header.Set("Access-Control-Allow-Headers", headers)
					}
					if policy.maxAge != "" {
						header.Set("Access-Control-Max-Age", policy.maxAge)
					}
				} else {
					clearCORSHeaders(header)
				}

				// Answered either way: the browser decides from the headers, and the
				// router has no route for OPTIONS. The response is identical whether
				// the origin, the method, or the route was the problem.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			header.Add("Vary", "Origin")
			if allowed {
				policy.writeAllowOrigin(header, origin)
				if policy.exposedList != "" {
					header.Set("Access-Control-Expose-Headers", policy.exposedList)
				}
			} else {
				clearCORSHeaders(header)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// resolveCORSPolicy validates the configuration and precomputes everything the
// request path needs.
func resolveCORSPolicy(options CORSOptions) *corsPolicy {
	if len(options.AllowedOrigins) == 0 && options.AllowOriginFunc == nil {
		panic("ossein middleware: CORS needs AllowedOrigins or AllowOriginFunc; " +
			"otherwise no origin can ever be allowed")
	}

	origins := slices.Clone(options.AllowedOrigins)
	wildcard := slices.Contains(origins, "*")

	if options.AllowCredentials {
		switch {
		case wildcard:
			panic("ossein middleware: CORS cannot combine a wildcard origin with " +
				"credentials; list the origins that may send credentials instead")
		case slices.Contains(origins, nullOrigin):
			panic("ossein middleware: CORS cannot combine the null origin with " +
				"credentials; it identifies no principal that can be authenticated")
		case options.AllowOriginFunc != nil && options.AllowOriginFunc(credentialsProbeOrigin):
			panic("ossein middleware: CORS cannot combine credentials with an " +
				"AllowOriginFunc that approves every origin; that lets any site read " +
				"authenticated responses")
		}
	}

	methods := slices.Clone(options.AllowedMethods)
	if len(methods) == 0 {
		methods = slices.Clone(defaultCORSMethods)
	}
	// A browser compares the approved list byte for byte, so a lower-case
	// configuration would produce a preflight that looks approved and is then
	// rejected.
	for i := range methods {
		methods[i] = strings.ToUpper(methods[i])
	}

	return &corsPolicy{
		origins:     origins,
		allowOrigin: options.AllowOriginFunc,
		wildcard:    wildcard,
		methods:     methods,
		methodList:  strings.Join(methods, ", "),
		headerList:  strings.Join(options.AllowedHeaders, ", "),
		exposedList: strings.Join(options.ExposedHeaders, ", "),
		credentials: options.AllowCredentials,
		maxAge:      resolveMaxAge(options.MaxAge),
	}
}

// resolveMaxAge renders the cache lifetime. A sub-second value rounds up rather than
// truncating to zero, which would tell the browser not to cache at all.
func resolveMaxAge(maxAge time.Duration) string {
	switch {
	case maxAge == 0:
		return ""
	case maxAge < 0:
		return "0"
	case maxAge < time.Second:
		return "1"
	default:
		return strconv.FormatInt(int64(maxAge/time.Second), 10)
	}
}

// allows reports whether the origin may read responses.
func (p *corsPolicy) allows(origin string) bool {
	if p.wildcard {
		return true
	}
	if slices.Contains(p.origins, origin) {
		return true
	}
	if p.allowOrigin != nil {
		return p.allowOrigin(origin)
	}
	return false
}

// allowsMethod reports whether a preflight may approve the requested method.
func (p *corsPolicy) allowsMethod(requested string) bool {
	return slices.Contains(p.methods, strings.ToUpper(requested))
}

// writeAllowOrigin sends the origin a browser will accept. A credentialed response
// may never carry a wildcard, and credentials are refused with one at setup, so the
// wildcard is only ever sent without them.
func (p *corsPolicy) writeAllowOrigin(header http.Header, origin string) {
	if p.wildcard {
		header.Set("Access-Control-Allow-Origin", "*")
		return
	}
	header.Set("Access-Control-Allow-Origin", origin)
	if p.credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
}

// headersFor returns the header list a preflight should approve: the configured one,
// or what the request asked for when none is configured.
//
// A requested wildcard is dropped from the echo. It is a valid token, so a page can
// ask for it, and echoing it back would turn a mirror into a blanket grant.
func (p *corsPolicy) headersFor(r *http.Request) string {
	if p.headerList != "" {
		return p.headerList
	}

	requested := r.Header.Get("Access-Control-Request-Headers")
	if !strings.Contains(requested, "*") {
		return requested
	}

	kept := make([]string, 0, 4)
	for _, name := range strings.Split(requested, ",") {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		kept = append(kept, name)
	}
	return strings.Join(kept, ", ")
}

// clearCORSHeaders removes headers a rejected origin must not receive, in case an
// outer middleware or an edge proxy set a permissive value.
func clearCORSHeaders(header http.Header) {
	header.Del("Access-Control-Allow-Origin")
	header.Del("Access-Control-Allow-Credentials")
	header.Del("Access-Control-Expose-Headers")
}

// isPreflight reports whether this is a CORS preflight rather than a plain OPTIONS
// request, which the router should answer.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		r.Header.Get("Access-Control-Request-Method") != ""
}
