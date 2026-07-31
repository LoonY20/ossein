package ossein

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventStreamSetsStreamingHeaders(t *testing.T) {
	app := New()
	app.Get("/events", func(c *Context) error {
		stream, err := c.EventStream()
		if err != nil {
			return err
		}
		defer stream.Close()
		return stream.Send(Event{Data: "hello"})
	})

	response := serveOnce(app, http.MethodGet, "/events")
	header := response.Result().Header

	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	// Without this, nginx holds every event until its proxy buffer fills.
	if got := header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if response.Body.String() != "data: hello\n\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestEventStreamWritesEveryField(t *testing.T) {
	app := New()
	app.Get("/events", func(c *Context) error {
		stream, err := c.EventStream()
		if err != nil {
			return err
		}
		defer stream.Close()
		return stream.Send(Event{
			ID:    "42",
			Name:  "progress",
			Data:  "half way",
			Retry: 3 * time.Second,
		})
	})

	body := serveOnce(app, http.MethodGet, "/events").Body.String()
	want := "id: 42\nevent: progress\nretry: 3000\ndata: half way\n\n"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// TestEventStreamSplitsMultiLineData covers the encoding rule that makes the difference
// between a payload arriving intact and the stream desynchronising: a newline inside
// data has to become another data line, because a bare one ends the event.
func TestEventStreamSplitsMultiLineData(t *testing.T) {
	app := New()
	app.Get("/events", func(c *Context) error {
		stream, err := c.EventStream()
		if err != nil {
			return err
		}
		defer stream.Close()
		// Trailing newline included: a naive encoder emits a blank data line and ends
		// the event one line early.
		return stream.Send(Event{Data: "{\n  \"a\": 1\n}\n"})
	})

	body := serveOnce(app, http.MethodGet, "/events").Body.String()
	want := "data: {\ndata:   \"a\": 1\ndata: }\n\n"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}

	// One event, so exactly one blank-line terminator.
	if count := strings.Count(body, "\n\n"); count != 1 {
		t.Fatalf("body contains %d event terminators, want 1: %q", count, body)
	}
}

// TestEventStreamRejectsInjectionInFields is the stream's version of header injection:
// a newline in an ID or a name would end the field and let an attacker-supplied value
// forge events of its own.
func TestEventStreamRejectsInjectionInFields(t *testing.T) {
	forged := "1\ndata: forged\n\nevent: admin"

	for name, event := range map[string]Event{
		"id":      {ID: forged, Data: "real"},
		"name":    {Name: forged, Data: "real"},
		"comment": {},
	} {
		app := New()
		app.Get("/events", func(c *Context) error {
			stream, err := c.EventStream()
			if err != nil {
				return err
			}
			defer stream.Close()
			if name == "comment" {
				return stream.Comment(forged)
			}
			return stream.Send(event)
		})

		body := serveOnce(app, http.MethodGet, "/events").Body.String()
		if strings.Contains(body, "data: forged") {
			t.Fatalf("%s field allowed a forged event: %q", name, body)
		}
	}
}

func TestEventStreamRejectsAnEmptyEvent(t *testing.T) {
	stream, _ := newTestStream(t)
	if err := stream.Send(Event{}); err == nil {
		t.Fatal("an event with nothing in it was accepted")
	}
}

// TestEventStreamRejectsARetryBelowItsResolution keeps a sub-millisecond value from
// being silently rounded to "retry: 0", which tells the client to reconnect
// immediately and forever.
func TestEventStreamRejectsARetryBelowItsResolution(t *testing.T) {
	stream, _ := newTestStream(t)

	if err := stream.Send(Event{Data: "x", Retry: 500 * time.Microsecond}); err == nil {
		t.Fatal("a sub-millisecond retry was accepted")
	}
	if err := stream.Send(Event{Data: "x", Retry: -time.Second}); err == nil {
		t.Fatal("a negative retry was accepted")
	}
}

func TestEventStreamComment(t *testing.T) {
	stream, recorder := newTestStream(t)

	if err := stream.Comment("keep-alive"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if recorder.Body.String() != ": keep-alive\n\n" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestEventStreamRefusesWritesAfterClose(t *testing.T) {
	stream, recorder := newTestStream(t)
	stream.Close()
	stream.Close() // idempotent

	if err := stream.Send(Event{Data: "late"}); err == nil {
		t.Fatal("Send after Close was accepted")
	}
	if err := stream.Comment("late"); err == nil {
		t.Fatal("Comment after Close was accepted")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing written after Close", recorder.Body.String())
	}
}

// TestEventStreamReportsAWriterThatCannotFlush fails at the point the stream is opened
// rather than at the first event. A stream that cannot flush delivers nothing until the
// handler returns, which for a stream is never — so the handler would simply hang.
func TestEventStreamReportsAWriterThatCannotFlush(t *testing.T) {
	writer := unflushableWriter{ResponseWriter: httptest.NewRecorder()}
	c := &Context{
		Response: writer,
		Request:  httptest.NewRequest(http.MethodGet, "/events", nil),
	}

	if _, err := c.EventStream(); err == nil {
		t.Fatal("a writer that cannot flush was accepted")
	} else if !strings.Contains(err.Error(), "cannot be streamed") {
		t.Fatalf("error = %v", err)
	}
}

// TestEventStreamKeepsResponseTracking is the reason this is not just a documentation
// snippet: the Ossein writer survives, so the access log records the stream and the
// error handler will not write over it.
func TestEventStreamKeepsResponseTracking(t *testing.T) {
	app := New()

	var tracked *ResponseWriter
	app.Get("/events", func(c *Context) error {
		stream, err := c.EventStream()
		if err != nil {
			return err
		}
		defer stream.Close()
		if err := stream.Send(Event{Data: "one"}); err != nil {
			return err
		}
		writer, ok := ResponseWriterFrom(c.Response)
		if !ok {
			t.Fatal("the Ossein response writer was not reachable from a stream")
		}
		tracked = writer
		// A handler that fails after streaming must not produce a second response.
		return errors.New("something went wrong afterwards")
	})

	response := serveOnce(app, http.MethodGet, "/events")

	if tracked == nil {
		t.Fatal("handler did not run")
	}
	if tracked.Status() != http.StatusOK {
		t.Fatalf("Status() = %d", tracked.Status())
	}
	if tracked.BytesWritten() != int64(len("data: one\n\n")) {
		t.Fatalf("BytesWritten() = %d", tracked.BytesWritten())
	}
	if response.Body.String() != "data: one\n\n" {
		t.Fatalf("body = %q, want the error handler to leave the stream alone",
			response.Body.String())
	}
}

// newTestStream opens a stream over a recorder, for the cases that do not need an app.
func newTestStream(t *testing.T) (*EventStream, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c := &Context{
		Response: NewResponseWriter(recorder),
		Request:  httptest.NewRequest(http.MethodGet, "/events", nil),
	}
	stream, err := c.EventStream()
	if err != nil {
		t.Fatalf("EventStream: %v", err)
	}
	recorder.Body.Reset()
	return stream, recorder
}

// unflushableWriter hides the recorder's Flush method, the shape of a middleware that
// wraps the writer without forwarding it.
type unflushableWriter struct {
	http.ResponseWriter
}

// TestEventStreamReportsAFailedWrite covers a client that disconnects mid-stream: the
// write fails, and the handler has to hear about it or it will loop forever producing
// events for a connection that is gone.
func TestEventStreamReportsAFailedWrite(t *testing.T) {
	broken := &brokenWriter{ResponseRecorder: httptest.NewRecorder()}
	c := &Context{
		Response: NewResponseWriter(broken),
		Request:  httptest.NewRequest(http.MethodGet, "/events", nil),
	}

	stream, err := c.EventStream()
	if err != nil {
		t.Fatalf("EventStream: %v", err)
	}

	broken.fail = errors.New("connection reset")
	if err := stream.Send(Event{Data: "one"}); err == nil {
		t.Fatal("a failed write was reported as success")
	} else if !strings.Contains(err.Error(), "write event") {
		t.Fatalf("error = %v", err)
	}
	if err := stream.Comment("keep-alive"); err == nil {
		t.Fatal("a failed comment write was reported as success")
	}
}

// TestEventStreamReportsAFailedFlush covers the writer that accepts bytes and then
// cannot push them, which leaves events buffered indefinitely.
func TestEventStreamReportsAFailedFlush(t *testing.T) {
	broken := &brokenWriter{ResponseRecorder: httptest.NewRecorder()}
	c := &Context{
		Response: NewResponseWriter(broken),
		Request:  httptest.NewRequest(http.MethodGet, "/events", nil),
	}

	stream, err := c.EventStream()
	if err != nil {
		t.Fatalf("EventStream: %v", err)
	}

	broken.flushFail = errors.New("pipe closed")
	if err := stream.Send(Event{Data: "one"}); err == nil {
		t.Fatal("a failed flush was reported as success")
	} else if !strings.Contains(err.Error(), "flush event") {
		t.Fatalf("error = %v", err)
	}
}

// brokenWriter fails on demand, after the stream has already been opened.
type brokenWriter struct {
	*httptest.ResponseRecorder
	fail      error
	flushFail error
}

func (w *brokenWriter) Write(p []byte) (int, error) {
	if w.fail != nil {
		return 0, w.fail
	}
	return w.ResponseRecorder.Write(p)
}

func (w *brokenWriter) FlushError() error {
	return w.flushFail
}
