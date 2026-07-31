package middleware

import (
	"net/http"

	ossein "github.com/LoonY20/ossein"
)

// SecurityHeaderOption configures the security headers.
type SecurityHeaderOption func(map[string]string)

// SecurityHeaderValues overrides or adds headers. An empty value removes a default,
// which is how an application opts out of one.
func SecurityHeaderValues(headers map[string]string) SecurityHeaderOption {
	return func(current map[string]string) {
		for header, value := range headers {
			if value == "" {
				delete(current, header)
				continue
			}
			current[header] = value
		}
	}
}

// defaultSecurityHeaders returns headers that are safe for an API to send
// unconditionally.
//
// Content-Security-Policy is deliberately absent: a useful policy depends on what
// the application serves, and a wrong one breaks pages silently.
// Strict-Transport-Security is absent too, because sending it over plain HTTP or
// from a host that is not fully HTTPS causes lasting problems; add it explicitly
// once that is true.
func defaultSecurityHeaders() map[string]string {
	return map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
}

// SecurityHeaders sets conservative response headers before the handler runs, so
// they apply to error responses and to the not-found fallback as well.
//
// A handler that sets one of these headers itself wins, since the values are only
// applied where the header is not already present.
func SecurityHeaders(options ...SecurityHeaderOption) ossein.Middleware {
	headers := defaultSecurityHeaders()
	for _, option := range options {
		if option != nil {
			option(headers)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := w.Header()
			for header, value := range headers {
				if target.Get(header) == "" {
					target.Set(header, value)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
