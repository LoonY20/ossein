package middleware

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// Timeout bounds how long a request may take, answering 504 when the deadline
// passes.
//
// The request context is cancelled at the deadline, so a handler that honours
// cancellation returns on its own. One that does not keeps running in the
// background until it finishes; its writes are discarded, because the deadline has
// passed and the response has already been sent. Rejection is keyed on the deadline
// itself rather than on when this middleware is scheduled, so it does not depend on
// a race between the two.
//
// The 504 goes through the application's ErrorHandler, so it matches every other
// error the API reports. A response already committed is left alone: a streaming
// handler that overruns keeps what the client received rather than having a timeout
// document appended to it.
//
// Unlike http.TimeoutHandler, this preserves the Ossein response writer, so
// Written() tracking, the committed-response guard, the access log's status, and
// http.ResponseController all keep working, and a streaming handler can still
// flush. That is the reason to prefer it.
//
// The handler runs on another goroutine, so a panic there is forwarded to the
// request goroutine, where Recover can see it. Register Timeout inside Recover.
// Do not apply it to long-lived streaming routes; scope it to a group instead.
func Timeout(duration time.Duration) ossein.Middleware {
	if duration <= 0 {
		panic("ossein middleware: timeout must be positive")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), duration)
			defer cancel()

			guard := &timeoutWriter{ResponseWriter: w, ctx: ctx}
			// Buffered and always written exactly once, so the handler goroutine
			// never blocks and nothing is left to leak.
			done := make(chan handlerOutcome, 1)

			go func() {
				defer func() {
					done <- handlerOutcome{recovered: recover()}
				}()
				next.ServeHTTP(guard, r.WithContext(ctx))
			}()

			select {
			case outcome := <-done:
				if outcome.recovered != nil {
					panic(outcome.recovered)
				}
			case <-ctx.Done():
				guard.expire(func(writer http.ResponseWriter) {
					ossein.WriteError(writer, r, ossein.NewHTTPError(
						http.StatusGatewayTimeout,
						"timeout",
						"The request took too long to process",
					))
				})
				// A panic arriving after the deadline cannot be reported to the
				// client, and re-panicking would unwind a request already answered.
				// Record it rather than losing it, as the standard library does.
				go reportLateOutcome(r, done)
			}
		})
	}
}

// handlerOutcome carries the recovered panic value, or nil for a normal return.
type handlerOutcome struct {
	recovered any
}

// reportLateOutcome logs a panic that arrived after the response was sent. It
// always receives exactly one value, so it cannot leak.
func reportLateOutcome(r *http.Request, done <-chan handlerOutcome) {
	outcome := <-done
	if outcome.recovered == nil {
		return
	}
	ossein.LoggerFromContext(r.Context()).Error(
		"panic after the request timed out",
		"panic", fmt.Sprint(outcome.recovered),
	)
}

// timeoutWriter serialises writes against the timeout response and drops anything a
// handler produces after the deadline, since the response has already been sent.
//
// Unwrap exposes the wrapped writer, so ResponseWriterFrom and
// http.ResponseController reach through it. That is what keeps response tracking and
// streaming working, which http.TimeoutHandler breaks by substituting a buffer.
type timeoutWriter struct {
	http.ResponseWriter

	ctx       context.Context
	mu        sync.Mutex
	expired   bool
	committed bool
}

func (w *timeoutWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pastDeadlineLocked() {
		return
	}
	w.committed = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *timeoutWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pastDeadlineLocked() {
		// Report success so a handler ignoring cancellation is not derailed by an
		// error it cannot act on.
		return len(content), nil
	}
	w.committed = true
	return w.ResponseWriter.Write(content)
}

// pastDeadlineLocked reports whether writes should be dropped. Consulting the
// context rather than only the expire flag makes rejection deterministic at the
// deadline, instead of depending on when the middleware happens to be scheduled.
func (w *timeoutWriter) pastDeadlineLocked() bool {
	return w.expired || w.ctx.Err() != nil
}

// Unwrap exposes the wrapped writer to ResponseWriterFrom and
// http.ResponseController.
func (w *timeoutWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// expire renders the timeout response, unless the handler already committed one.
// render writes to the wrapped writer while the lock is held, so no handler write
// can interleave with it.
func (w *timeoutWriter) expire(render func(http.ResponseWriter)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expired = true
	if w.committed {
		return
	}
	render(w.ResponseWriter)
}
