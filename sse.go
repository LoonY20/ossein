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
	if s.closed {
		return errors.New("ossein: event stream is closed")
	}
	if event.Data == "" && event.ID == "" && event.Name == "" && event.Retry == 0 {
		return errors.New("ossein: event is empty")
	}

	// A newline in any single-line field would start a new field, letting a value
	// forge an event — the stream equivalent of header injection. Data is exempt
	// because its newlines are encoded as separate data lines below.
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
		// A trailing newline in the value would otherwise end the event early.
		for _, line := range strings.Split(strings.TrimSuffix(event.Data, "\n"), "\n") {
			frame.WriteString("data: " + strings.TrimSuffix(line, "\r") + "\n")
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
	if s.closed {
		return errors.New("ossein: event stream is closed")
	}
	if err := checkEventField("event comment", text); err != nil {
		return err
	}
	return s.write(": " + text + "\n\n")
}

// Close marks the stream finished. Later writes report an error rather than appending
// to a response the handler has moved on from. It is safe to call more than once.
func (s *EventStream) Close() {
	s.closed = true
}

func (s *EventStream) write(frame string) error {
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
