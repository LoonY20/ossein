package middleware

import (
	"log/slog"
	"net/http"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// AccessLogOption configures the access log.
type AccessLogOption func(*accessLogOptions)

type accessLogOptions struct {
	skip map[string]struct{}
}

// SkipPaths omits requests for the given exact paths, which is what keeps health
// and readiness probes out of the log.
func SkipPaths(paths ...string) AccessLogOption {
	return func(options *accessLogOptions) {
		if options.skip == nil {
			options.skip = make(map[string]struct{}, len(paths))
		}
		for _, path := range paths {
			options.skip[path] = struct{}{}
		}
	}
}

// AccessLog writes one structured line per request through the request-scoped
// logger, so every line already carries the request ID, method, and path.
//
// The status and response size come from the tracking Ossein installs on every
// request, so they are the values actually sent, including those written by the
// error handler or the not-found fallback after the handler returned.
//
// Register it outside Recover. A middleware only observes a status written below
// it, so the other order reports a panicking request with the status it had before
// recovery. The line is emitted either way, because logging is deferred.
//
// A connection the handler hijacked, such as a websocket upgrade, never reaches
// the tracked writer, so it is reported as an uncommitted response.
//
// The level reflects the outcome: server errors are logged at error level, client
// errors at warn, everything else at info, so a level filter is a useful triage
// tool.
func AccessLog(options ...AccessLogOption) ossein.Middleware {
	var settings accessLogOptions
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, skipped := settings.skip[r.URL.Path]; skipped {
				next.ServeHTTP(w, r)
				return
			}

			started := time.Now()
			// Deferred so a panic unwinding towards Recover is still logged. On the
			// normal path this runs at the same point a plain call would.
			defer func() {
				status, size := recordedResponse(w)
				ossein.LoggerFromContext(r.Context()).Log(
					r.Context(),
					levelForStatus(status),
					"request completed",
					"status", status,
					"bytes", size,
					"duration_ms", time.Since(started).Milliseconds(),
				)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// recordedResponse reports the status and size Ossein recorded. A handler that
// wrote nothing leaves the status unset, and net/http then sends 200, so that is
// what gets logged.
//
// The tracking is installed per request by the application, so a zero status means
// this middleware is running outside one and the real status is unavailable.
func recordedResponse(w http.ResponseWriter) (int, int64) {
	writer, ok := ossein.ResponseWriterFrom(w)
	if !ok {
		return 0, 0
	}
	status := writer.Status()
	if status == 0 {
		status = http.StatusOK
	}
	return status, writer.BytesWritten()
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
