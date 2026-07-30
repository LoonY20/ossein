package ossein

import "net/http"

// ResponseWriter wraps http.ResponseWriter and records whether the response
// has been committed, its status code, and the number of body bytes written.
//
// Ossein wraps every request's writer before middleware and handlers run, so
// the recorded state is available anywhere through ResponseWriterFrom. Use
// http.ResponseController for flushing, hijacking, and deadlines; it reaches
// the underlying writer through Unwrap.
type ResponseWriter struct {
	writer http.ResponseWriter
	status int
	bytes  int64
}

// NewResponseWriter wraps w. An already wrapped writer is returned unchanged.
func NewResponseWriter(w http.ResponseWriter) *ResponseWriter {
	if wrapped, ok := w.(*ResponseWriter); ok {
		return wrapped
	}
	return &ResponseWriter{writer: w}
}

// ResponseWriterFrom returns the Ossein response writer wrapping w, if any.
//
// Writers that wrap another writer and expose it through
// Unwrap() http.ResponseWriter are followed, the same convention
// http.ResponseController uses, so middleware layered between Ossein and a
// handler does not hide the recorded state.
func ResponseWriterFrom(w http.ResponseWriter) (*ResponseWriter, bool) {
	for w != nil {
		if wrapped, ok := w.(*ResponseWriter); ok {
			return wrapped, true
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, false
		}
		w = unwrapper.Unwrap()
	}
	return nil, false
}

// Header returns the header map of the underlying writer.
func (w *ResponseWriter) Header() http.Header {
	return w.writer.Header()
}

// WriteHeader records the first status code and delegates every call, so the
// underlying writer keeps its own duplicate-call diagnostics.
func (w *ResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.writer.WriteHeader(status)
}

// Write commits the response with an implicit 200 when no status was written.
func (w *ResponseWriter) Write(content []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	written, err := w.writer.Write(content)
	w.bytes += int64(written)
	return written, err
}

// Written reports whether the response has been committed.
func (w *ResponseWriter) Written() bool {
	return w.status != 0
}

// Status returns the committed status code, or zero before the response is
// committed.
func (w *ResponseWriter) Status() int {
	return w.status
}

// BytesWritten returns the number of response body bytes written so far.
func (w *ResponseWriter) BytesWritten() int64 {
	return w.bytes
}

// Unwrap returns the underlying writer for http.ResponseController.
func (w *ResponseWriter) Unwrap() http.ResponseWriter {
	return w.writer
}
