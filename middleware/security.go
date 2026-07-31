package middleware

import (
	"net/http"
	"net/textproto"

	ossein "github.com/LoonY20/ossein"
)

// SecurityHeaderOption configures the security headers.
type SecurityHeaderOption func(*securityHeaderOptions)

type securityHeaderOptions struct {
	headers map[string]string
}

// SecurityHeaderValues overrides or adds headers. An empty value removes a default,
// which is how an application opts out of one.
func SecurityHeaderValues(headers map[string]string) SecurityHeaderOption {
	return func(options *securityHeaderOptions) {
		for header, value := range headers {
			canonical := textproto.CanonicalMIMEHeaderKey(header)
			if value == "" {
				delete(options.headers, canonical)
				continue
			}
			options.headers[canonical] = value
		}
	}
}

// defaultSecurityHeaders returns headers that are safe for an API to send
// unconditionally. The keys are already in canonical form.
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
// A header already present is never replaced, so a handler or an outer middleware
// stays in control — including one that deliberately set an empty value, which is
// distinct from the header being absent.
func SecurityHeaders(options ...SecurityHeaderOption) ossein.Middleware {
	settings := securityHeaderOptions{headers: defaultSecurityHeaders()}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	headers := settings.headers

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := w.Header()
			for header, value := range headers {
				// Get cannot tell an absent header from one present but empty, and
				// only the former should be filled in.
				if _, present := target[header]; present {
					continue
				}
				target[header] = []string{value}
			}
			next.ServeHTTP(w, r)
		})
	}
}
