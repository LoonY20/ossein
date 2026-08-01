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

// TestEventStreamNormalizesCarriageReturnsInData is the injection this encoder existed
// to prevent and did not. A client ends a line on CR, LF, or CRLF, so a bare CR left in
// data ends the data line there and everything after it is read as new fields —
// attacker-chosen event type, attacker-chosen id, attacker-appended payload.
func TestEventStreamNormalizesCarriageReturnsInData(t *testing.T) {
	forged := "hi\rid: 999\revent: adminMessage\rdata: you are fired"

	stream, recorder := newTestStream(t)
	if err := stream.Send(Event{ID: "1", Name: "comment", Data: forged}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	frame := recorder.Body.String()
	if strings.Contains(frame, "\r") {
		t.Fatalf("frame carries a raw carriage return: %q", frame)
	}
	// Every forged field is now the content of a data line, including the last one,
	// which arrives at the client as the literal text "data: you are fired".
	want := "id: 1\nevent: comment\n" +
		"data: hi\ndata: id: 999\ndata: event: adminMessage\ndata: data: you are fired\n\n"
	if frame != want {
		t.Fatalf("frame = %q, want %q", frame, want)
	}
}

// TestEventStreamHandlesEveryLineEnding covers the three terminators a client
// recognises, including the doubled forms that would otherwise emit a blank line and
// dispatch the event early.
func TestEventStreamHandlesEveryLineEnding(t *testing.T) {
	for _, testCase := range []struct {
		data string
		want string
	}{
		{"a\nb", "data: a\ndata: b\n\n"},
		{"a\r\nb", "data: a\ndata: b\n\n"},
		{"a\rb", "data: a\ndata: b\n\n"},
		// A doubled terminator is a blank line in the payload, which has to be sent as
		// an empty data line rather than ending the event.
		{"a\r\rb", "data: a\ndata: \ndata: b\n\n"},
		{"a\n\nb", "data: a\ndata: \ndata: b\n\n"},
		// One trailing terminator is how a value is usually written; it is dropped
		// rather than becoming a trailing blank line.
		{"a\n", "data: a\n\n"},
		{"a\r\n", "data: a\n\n"},
		{"a\r", "data: a\n\n"},
		// Exactly one, though: a payload that genuinely ends in a blank line keeps it.
		{"a\n\n", "data: a\ndata: \n\n"},
		{"a\r\n\r\n", "data: a\ndata: \n\n"},
	} {
		stream, recorder := newTestStream(t)
		if err := stream.Send(Event{Data: testCase.data}); err != nil {
			t.Fatalf("Send(%q): %v", testCase.data, err)
		}
		if got := recorder.Body.String(); got != testCase.want {
			t.Fatalf("data %q produced %q, want %q", testCase.data, got, testCase.want)
		}
		// Exactly one event: a second terminator anywhere means the payload split the
		// event in two.
		if count := strings.Count(recorder.Body.String(), "\n\n"); count != 1 {
			t.Fatalf("data %q produced %d events", testCase.data, count)
		}
	}
}

// TestEventStreamRejectsACarriageReturnInAField is the single-line half of the same
// problem: a CR ends the field just as an LF does.
func TestEventStreamRejectsACarriageReturnInAField(t *testing.T) {
	stream, _ := newTestStream(t)

	if err := stream.Send(Event{ID: "1\rdata: forged", Data: "real"}); err == nil {
		t.Fatal("a carriage return in an id was accepted")
	}
	if err := stream.Send(Event{Name: "x\rdata: forged", Data: "real"}); err == nil {
		t.Fatal("a carriage return in a name was accepted")
	}
	if err := stream.Comment("x\rdata: forged"); err == nil {
		t.Fatal("a carriage return in a comment was accepted")
	}
}

// TestEventStreamRejectsAnEventWithNoData covers a silent no-op: a client dispatches an
// event only when it has data, so an id or a name on its own is written, accepted, and
// then ignored by every browser.
func TestEventStreamRejectsAnEventWithNoData(t *testing.T) {
	stream, recorder := newTestStream(t)

	if err := stream.Send(Event{Name: "ping"}); err == nil {
		t.Fatal("an event with a name and no data was accepted")
	}
	if err := stream.Send(Event{ID: "7"}); err == nil {
		t.Fatal("an event with an id and no data was accepted")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing written", recorder.Body.String())
	}

	// A retry on its own is a directive rather than an event, so it is allowed.
	if err := stream.Send(Event{Retry: 2 * time.Second}); err != nil {
		t.Fatalf("a retry-only event was rejected: %v", err)
	}
	if recorder.Body.String() != "retry: 2000\n\n" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

// TestEventStreamWritesHeadersWhenItOpens pins the behavior the docs lead with. Without
// the explicit WriteHeader, the status is only committed by the first event, so a
// stream that sends nothing for a minute has not answered the request at all.
func TestEventStreamWritesHeadersWhenItOpens(t *testing.T) {
	recorder := httptest.NewRecorder()
	c := &Context{
		Response: NewResponseWriter(recorder),
		Request:  httptest.NewRequest(http.MethodGet, "/events", nil),
	}

	stream, err := c.EventStream()
	if err != nil {
		t.Fatalf("EventStream: %v", err)
	}
	defer stream.Close()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d before the first event", recorder.Code)
	}
	tracked, ok := ResponseWriterFrom(c.Response)
	if !ok || !tracked.Written() {
		t.Fatal("the response was not committed when the stream opened")
	}
	if !recorder.Flushed {
		t.Fatal("the headers were not flushed when the stream opened")
	}
}

// TestEventStreamOnANilStream keeps a zero value from taking the process down: an
// EventStream is returned alongside an error, and a caller that forgets to check it
// should get another error rather than a panic.
func TestEventStreamOnANilStream(t *testing.T) {
	var stream *EventStream

	stream.Close() // must not panic
	if err := stream.Send(Event{Data: "x"}); err == nil {
		t.Fatal("Send on a nil stream was accepted")
	}
	if err := stream.Comment("x"); err == nil {
		t.Fatal("Comment on a nil stream was accepted")
	}
}

// TestEventStreamSurvivingItsHandlerReportsAnError covers the natural mistake of handing
// the stream to a producer goroutine. net/http clears the response writer once the
// handler returns, so a later write is a nil dereference — on a goroutine no recovery
// middleware is watching, which takes the process down.
func TestEventStreamSurvivingItsHandlerReportsAnError(t *testing.T) {
	streams := make(chan *EventStream, 1)

	server := httptest.NewServer(New().Handler())
	defer server.Close()

	app := New()
	app.Get("/events", func(c *Context) error {
		stream, err := c.EventStream()
		if err != nil {
			return err
		}
		streams <- stream
		return stream.Send(Event{Data: "one"})
	})

	live := httptest.NewServer(app)
	defer live.Close()

	response, err := http.Get(live.URL + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	response.Body.Close()

	stream := <-streams
	// The handler has returned; this is the write that used to panic.
	if err := stream.Send(Event{Data: "late"}); err == nil {
		t.Fatal("a write after the handler returned was reported as success")
	}
	if err := stream.Send(Event{Data: "later"}); err == nil {
		t.Fatal("the stream did not close itself after failing")
	}
}
