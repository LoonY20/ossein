package middleware

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
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
// background until it finishes; its writes and header changes are discarded,
// because the response has already been sent. Rejection is keyed on the deadline
// itself rather than on when this middleware is scheduled, so it does not depend on
// a race between the two, and a cancellation for any other reason — a client that
// disconnected, say — is not reported as a timeout.
//
// The 504 goes through the application's ErrorHandler, so it matches every other
// error the API reports. A response already committed is left alone: a streaming
// handler that overruns keeps what the client received rather than having a timeout
// document appended to it. A connection the handler hijacked is left alone too.
//
// Unlike http.TimeoutHandler, this preserves the Ossein response writer, so
// Written() tracking, the committed-response guard, the access log's status,
// http.ResponseController, and flushing all keep working. That is the reason to
// prefer it. Headers are the exception: the handler mutates a private map that is
// copied to the real response when it commits, because a shared header map cannot be
// written from two goroutines without risking a fatal concurrent map write.
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

			guard := newTimeoutWriter(w, ctx)
			// Buffered so the handler goroutine never blocks, whether or not anyone
			// is still waiting for it.
			done := make(chan handlerOutcome, 1)

			go func() {
				defer func() {
					recovered := recover()
					if recovered != nil && pastDeadline(ctx) {
						// The request has been answered and the request goroutine has
						// moved on, so this cannot be reported to the client and
						// re-panicking would unwind a finished request. Record it
						// rather than losing it, which is what the standard library
						// does with the same case.
						ossein.LoggerFromContext(r.Context()).Error(
							"panic after the request timed out",
							"panic", fmt.Sprint(recovered),
						)
						return
					}
					done <- handlerOutcome{recovered: recovered}
				}()
				next.ServeHTTP(guard, r.WithContext(ctx))
			}()

			finish := func(outcome handlerOutcome) {
				// A handler that only set headers still needs them sent.
				guard.commitHeaders()
				if outcome.recovered != nil {
					panic(outcome.recovered)
				}
			}

			select {
			case outcome := <-done:
				finish(outcome)
			case <-ctx.Done():
				if !pastDeadline(ctx) {
					// Cancelled for another reason, such as the client going away.
					// There is no timeout to report, so wait for the handler and
					// answer normally, as an unwrapped handler would.
					finish(<-done)
					return
				}
				guard.expire(func(writer http.ResponseWriter) {
					ossein.WriteError(writer, r, ossein.NewHTTPError(
						http.StatusGatewayTimeout,
						"timeout",
						"The request took too long to process",
					))
				})
			}
		})
	}
}

// handlerOutcome carries the recovered panic value, or nil for a normal return.
type handlerOutcome struct {
	recovered any
}

// pastDeadline reports whether the deadline elapsed, as opposed to the request being
// cancelled for another reason.
func pastDeadline(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// timeoutWriter serialises the response against the timeout, dropping anything a
// handler produces after the deadline.
//
// The handler mutates a private header map rather than the real one. A response
// header map is not safe for concurrent use, and the timeout response sets headers
// of its own, so sharing it would risk a fatal concurrent map write whenever a
// handler happens to finish near the deadline. The private map is copied across when
// the handler commits.
//
// The writer is not embedded, so nothing reaches the real response without passing
// through the lock. Unwrap still exposes it, which is what keeps
// ResponseWriterFrom, http.ResponseController, flushing, and hijacking working.
type timeoutWriter struct {
	inner  http.ResponseWriter
	ctx    context.Context
	header http.Header

	mu        sync.Mutex
	committed bool
	hijacked  bool
}

func newTimeoutWriter(inner http.ResponseWriter, ctx context.Context) *timeoutWriter {
	return &timeoutWriter{
		inner:  inner,
		ctx:    ctx,
		header: make(http.Header),
	}
}

// Header returns the handler's private header map, which is copied to the response
// when the handler commits.
func (w *timeoutWriter) Header() http.Header {
	return w.header
}

func (w *timeoutWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed || w.rejectLocked() {
		return
	}
	w.commitLocked()
	w.inner.WriteHeader(status)
}

func (w *timeoutWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rejectLocked() {
		// Report success so a handler ignoring cancellation is not derailed by an
		// error it cannot act on, and so io.Copy does not see a short write.
		return len(content), nil
	}
	w.commitLocked()
	return w.inner.Write(content)
}

// Unwrap exposes the real writer to ResponseWriterFrom and
// http.ResponseController. Flushing reaches it directly, which is deliberate: a
// streaming handler must be able to flush. Hijack is intercepted below so the
// timeout knows not to answer on a connection the handler took over.
func (w *timeoutWriter) Unwrap() http.ResponseWriter {
	return w.inner
}

// Hijack hands the connection to the handler and records that it did, so no timeout
// response is written into a connection the handler now owns.
func (w *timeoutWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffered, err := http.NewResponseController(w.inner).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	w.hijacked = true
	w.mu.Unlock()
	return connection, buffered, nil
}

// commitHeaders copies the handler's headers across when it returned without
// writing, so net/http sends them with the implicit 200.
func (w *timeoutWriter) commitHeaders() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed || w.rejectLocked() {
		return
	}
	w.commitLocked()
}

// expire renders the timeout response, unless the handler already committed one or
// took over the connection. render writes to the real writer while the lock is held,
// so no handler write can interleave with it.
func (w *timeoutWriter) expire(render func(http.ResponseWriter)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed || w.hijacked {
		return
	}
	// Mark the response as committed so a late handler write cannot add to it.
	w.committed = true
	render(w.inner)
}

// rejectLocked reports whether the handler's output should be discarded.
func (w *timeoutWriter) rejectLocked() bool {
	return w.hijacked || pastDeadline(w.ctx)
}

// commitLocked transfers the handler's headers to the response exactly once.
func (w *timeoutWriter) commitLocked() {
	if w.committed {
		return
	}
	w.committed = true
	target := w.inner.Header()
	for header, values := range w.header {
		target[header] = values
	}
}
