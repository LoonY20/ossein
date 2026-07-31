package middleware

import (
	"fmt"
	"net/http"
	"runtime"

	ossein "github.com/LoonY20/ossein"
)

// stackBufferBytes bounds the stack trace captured for a panic, matching the size
// net/http uses for the same purpose.
const stackBufferBytes = 64 << 10

// Recover turns a panic in a later handler into a structured 500.
//
// The response goes through the application's ErrorHandler, so it matches every
// other error the API reports, and the panic value is never included: it is logged
// with a stack trace through the request-scoped logger instead.
//
// A response that was already committed is left alone, since its bytes are on the
// wire and appending an error document would corrupt it. http.ErrAbortHandler is
// re-panicked, because the standard library uses it to abandon a response
// deliberately.
//
// Register it outermost so it covers the middleware below it as well.
func Recover() ossein.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				stack := make([]byte, stackBufferBytes)
				stack = stack[:runtime.Stack(stack, false)]
				ossein.LoggerFromContext(r.Context()).Error(
					"recovered from panic",
					"panic", fmt.Sprint(recovered),
					"stack", string(stack),
				)

				// Nothing can be added to a response the client already has.
				if writer, ok := ossein.ResponseWriterFrom(w); ok && writer.Written() {
					return
				}
				respondInternalError(w, r)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// respondInternalError renders a 500 through the application's error handler, with
// a plain fallback if that handler is itself what failed.
//
// The panic being recovered may have come from the error handler, in which case
// rendering through it panics again — inside the deferred function, where nothing
// else can catch it, taking the connection down instead of answering.
func respondInternalError(w http.ResponseWriter, r *http.Request) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if recovered == http.ErrAbortHandler {
			panic(recovered)
		}
		ossein.LoggerFromContext(r.Context()).Error(
			"the error handler panicked while reporting a panic",
			"panic", fmt.Sprint(recovered),
		)
		if writer, ok := ossein.ResponseWriterFrom(w); ok && writer.Written() {
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}()

	ossein.WriteError(w, r, ossein.NewHTTPError(
		http.StatusInternalServerError,
		"internal_error",
		"Internal Server Error",
	))
}
