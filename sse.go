package ossein

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Event is one server-sent event.
//
// Every field is optional except Data, which is what the browser delivers to the
// message handler. Name selects the listener (addEventListener("progress", …)); ID is
// echoed back in Last-Event-ID when the browser reconnects; Retry tells it how long to
// wait before doing so.
type Event struct {
	// ID identifies the event for resumption.
	ID string
	// Name is the event type. Empty means the default "message" type.
	Name string
	// Data is the payload. A multi-line value is sent as multiple data lines and
	// arrives at the client rejoined with newlines.
	//
	// CRLF and lone CR are normalized to LF first. The wire format treats all three
	// as line terminators, so a carriage return cannot be carried inside data by any
	// encoding — leaving one in place would end the line at the client and let the
	// rest of the value be read as new fields.
	Data string
	// Retry is the client's reconnection delay. Zero leaves it unchanged. It is sent
	// with millisecond resolution, so a shorter value is rejected rather than
	// silently rounded to zero.
	Retry time.Duration
}

// EventStream writes server-sent events to a response.
//
// It is not safe for concurrent use: one goroutine should own the stream, as it owns
// the response it is writing to.
type EventStream struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	closed     bool
}

// EventStream begins a server-sent event response.
//
// The headers are written and flushed immediately, so the client's connection opens
// before the first event rather than when the buffer happens to fill. It reports an
// error if the response cannot be flushed, since a stream that cannot flush delivers
// nothing until the handler returns — which for a stream is never.
//
// The Ossein response writer is preserved, so status and size tracking, the access log,
// and the committed-response guard all keep working.
func (c *Context) EventStream() (*EventStream, error) {
	controller := http.NewResponseController(c.Response)

	header := c.Response.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	// Nginx buffers proxied responses by default, which holds events until the buffer
	// fills. This is the documented way to turn that off, and it is ignored elsewhere.
	header.Set("X-Accel-Buffering", "no")

	c.Response.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return nil, fmt.Errorf("ossein: response cannot be streamed: %w", err)
	}

	return &EventStream{writer: c.Response, controller: controller}, nil
}

// Send writes one event and flushes it.
func (s *EventStream) Send(event Event) error {
	if s == nil {
		return errors.New("ossein: event stream is nil")
	}
	if s.closed {
		return errors.New("ossein: event stream is closed")
	}
	if event.Data == "" && event.ID == "" && event.Name == "" && event.Retry == 0 {
		return errors.New("ossein: event is empty")
	}

	// A client dispatches an event only when it has data, so an id or a name without
	// any would be written, accepted, and then silently ignored by every browser. A
	// retry on its own is different: it is a directive, not an event, and is not meant
	// to be dispatched.
	if event.Data == "" && (event.ID != "" || event.Name != "") {
		return errors.New(
			"ossein: event has no data, so no client will dispatch it; " +
				"send a retry on its own if that is what was meant",
		)
	}

	// A line terminator in any single-line field would end it and let the rest be read
	// as new fields — the stream equivalent of header injection. Data is exempt because
	// its terminators are encoded as separate data lines below.
	if err := checkEventField("event id", event.ID); err != nil {
		return err
	}
	if err := checkEventField("event name", event.Name); err != nil {
		return err
	}
	if event.Retry < 0 {
		return fmt.Errorf("ossein: event retry %v is negative", event.Retry)
	}
	if event.Retry > 0 && event.Retry < time.Millisecond {
		return fmt.Errorf(
			"ossein: event retry %v is below the millisecond resolution of the field",
			event.Retry,
		)
	}

	var frame strings.Builder
	if event.ID != "" {
		frame.WriteString("id: " + event.ID + "\n")
	}
	if event.Name != "" {
		frame.WriteString("event: " + event.Name + "\n")
	}
	if event.Retry > 0 {
		frame.WriteString("retry: " + strconv.FormatInt(event.Retry.Milliseconds(), 10) + "\n")
	}
	if event.Data != "" {
		// Normalized before splitting, so every line boundary in the value is one this
		// encoder chose. Splitting on LF alone and trimming a trailing CR is not enough:
		// a client ends a line on CR too, so "a\rid: 999" would arrive as a data line
		// followed by a forged id field.
		data := strings.ReplaceAll(event.Data, "\r\n", "\n")
		data = strings.ReplaceAll(data, "\r", "\n")

		// One trailing terminator is dropped: it is how a value is usually written, and
		// keeping it would emit a blank line, which ends the event early.
		for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
			frame.WriteString("data: " + line + "\n")
		}
	}
	frame.WriteString("\n")

	return s.write(frame.String())
}

// Comment writes a comment line, which clients ignore.
//
// It is how a stream is kept alive through an idle proxy: a comment costs nothing and
// resets the idle timer that would otherwise drop the connection.
func (s *EventStream) Comment(text string) error {
	if s == nil {
		return errors.New("ossein: event stream is nil")
	}
	if s.closed {
		return errors.New("ossein: event stream is closed")
	}
	if err := checkEventField("event comment", text); err != nil {
		return err
	}
	return s.write(": " + text + "\n\n")
}

// Close marks the stream finished. Later writes report an error rather than appending
// to a response the handler has moved on from. It is safe to call more than once, and
// on a nil stream.
func (s *EventStream) Close() {
	if s == nil {
		return
	}
	s.closed = true
}

// write emits a frame and pushes it to the client.
//
// The recover is for a stream that outlived its handler. net/http clears the response's
// buffered writer once a handler returns, so a write after that is a nil dereference —
// and a producer goroutine holding the stream is a natural enough shape that it should
// come back as an error rather than take the process down from a goroutine no recovery
// middleware is watching.
func (s *EventStream) write(frame string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.closed = true
			err = fmt.Errorf(
				"ossein: event stream is no longer writable, "+
					"which happens when it outlives its handler: %v", recovered,
			)
		}
	}()

	if _, err := s.writer.Write([]byte(frame)); err != nil {
		return fmt.Errorf("ossein: write event: %w", err)
	}
	if err := s.controller.Flush(); err != nil {
		return fmt.Errorf("ossein: flush event: %w", err)
	}
	return nil
}

func checkEventField(what, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("ossein: %s contains a newline: %q", what, value)
	}
	return nil
}
