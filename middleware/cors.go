package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// CORSOptions configures cross-origin access.
type CORSOptions struct {
	// AllowedOrigins lists origins allowed to read responses, matched exactly. A
	// single "*" allows any origin, which cannot be combined with
	// AllowCredentials.
	AllowedOrigins []string

	// AllowOriginFunc decides origins that AllowedOrigins does not list, for
	// subdomains or an allowlist held elsewhere. It receives the Origin header
	// verbatim.
	AllowOriginFunc func(origin string) bool

	// AllowedMethods lists methods a preflight may approve. Empty means the common
	// set: GET, HEAD, POST, PUT, PATCH, DELETE.
	AllowedMethods []string

	// AllowedHeaders lists request headers a preflight may approve. Empty echoes
	// whatever the preflight asked for, which is the usual convenience: request
	// headers are not themselves a credential.
	AllowedHeaders []string

	// ExposedHeaders lists response headers a browser may read beyond the handful
	// it exposes by default.
	ExposedHeaders []string

	// AllowCredentials permits cookies and HTTP authentication on cross-origin
	// requests. It cannot be combined with a wildcard origin.
	AllowCredentials bool

	// MaxAge is how long a browser may cache a preflight result. Zero omits the
	// header, leaving the browser's own default.
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

// CORS answers cross-origin preflight requests and adds the response headers a
// browser needs.
//
// A preflight is short-circuited with 204, which is necessary rather than merely
// convenient: an OPTIONS request matches no route, so without this it would be
// answered by the router as a 405. For the same reason CORS belongs in App.Use
// rather than on a group, since group middleware does not run for a request that
// matches no route in the group.
//
// A request without an Origin header is not a cross-origin request and passes
// through untouched. An origin that is not allowed is also passed through, without
// the headers that would let a browser read the response — enforcement is the
// browser's job, and refusing to serve the request would break same-origin clients
// that happen to send an Origin.
//
// Vary is set on every origin-dependent response so a shared cache cannot serve one
// origin's response to another.
//
// CORS panics during setup for two configurations that cannot be served safely: a
// wildcard origin together with AllowCredentials, which the specification forbids
// and which would let any site make authenticated requests with the user's cookies,
// and a configuration that can never allow anything.
func CORS(options CORSOptions) ossein.Middleware {
	if len(options.AllowedOrigins) == 0 && options.AllowOriginFunc == nil {
		panic("ossein middleware: CORS needs AllowedOrigins or AllowOriginFunc; " +
			"otherwise no origin can ever be allowed")
	}
	wildcard := containsFold(options.AllowedOrigins, "*")
	if wildcard && options.AllowCredentials {
		panic("ossein middleware: CORS cannot combine a wildcard origin with " +
			"credentials; list the origins that may send credentials instead")
	}

	methods := options.AllowedMethods
	if len(methods) == 0 {
		methods = defaultCORSMethods
	}
	allowedMethods := strings.Join(methods, ", ")
	allowedHeaders := strings.Join(options.AllowedHeaders, ", ")
	exposedHeaders := strings.Join(options.ExposedHeaders, ", ")

	maxAge := ""
	if options.MaxAge > 0 {
		maxAge = strconv.Itoa(int(options.MaxAge.Seconds()))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			header := w.Header()
			allowed := originAllowed(options, origin)

			if isPreflight(r) {
				header.Add("Vary", "Origin")
				header.Add("Vary", "Access-Control-Request-Method")
				header.Add("Vary", "Access-Control-Request-Headers")

				if allowed && containsFold(methods, r.Header.Get("Access-Control-Request-Method")) {
					writeAllowOrigin(header, origin, wildcard, options.AllowCredentials)
					header.Set("Access-Control-Allow-Methods", allowedMethods)
					if requested := requestedHeaders(r, allowedHeaders); requested != "" {
						header.Set("Access-Control-Allow-Headers", requested)
					}
					if maxAge != "" {
						header.Set("Access-Control-Max-Age", maxAge)
					}
				}

				// Answered either way: the browser decides from the headers, and the
				// router has no route for OPTIONS.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			header.Add("Vary", "Origin")
			if allowed {
				writeAllowOrigin(header, origin, wildcard, options.AllowCredentials)
				if exposedHeaders != "" {
					header.Set("Access-Control-Expose-Headers", exposedHeaders)
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isPreflight reports whether this is a CORS preflight rather than a plain OPTIONS
// request, which the router should answer.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		r.Header.Get("Access-Control-Request-Method") != ""
}

// originAllowed reports whether the origin may read responses.
func originAllowed(options CORSOptions, origin string) bool {
	if containsFold(options.AllowedOrigins, "*") {
		return true
	}
	for _, allowed := range options.AllowedOrigins {
		if allowed == origin {
			return true
		}
	}
	if options.AllowOriginFunc != nil {
		return options.AllowOriginFunc(origin)
	}
	return false
}

// writeAllowOrigin sends the origin a browser will accept. A credentialed response
// may never carry a wildcard, and credentials are refused with one at setup, so the
// wildcard is only ever sent without them.
func writeAllowOrigin(header http.Header, origin string, wildcard, credentials bool) {
	if wildcard {
		header.Set("Access-Control-Allow-Origin", "*")
		return
	}
	header.Set("Access-Control-Allow-Origin", origin)
	if credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
}

// requestedHeaders returns the header list a preflight should approve: the configured
// one, or what the request asked for when none is configured.
func requestedHeaders(r *http.Request, configured string) string {
	if configured != "" {
		return configured
	}
	return r.Header.Get("Access-Control-Request-Headers")
}

// containsFold reports whether values contains target, compared case-insensitively,
// which is how HTTP methods and the wildcard are matched. Callers never pass an
// empty target: a preflight is identified by a non-empty requested method.
func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
