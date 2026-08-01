package ossein

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// JSON writes value as a JSON response with the provided status code.
// The value is encoded before headers are committed, so serialization errors
// can still be handled by the caller.
func JSON(w http.ResponseWriter, status int, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := ensureUncommitted(w, "write JSON response"); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(payload, '\n'))
	return err
}

// Text writes a plain-text response.
func Text(w http.ResponseWriter, status int, text string) error {
	return Blob(w, status, "text/plain; charset=utf-8", []byte(text))
}

// HTML writes an HTML response.
//
// The markup is written as given. Ossein has no template layer, so escaping
// untrusted values is html/template's job, not this function's.
func HTML(w http.ResponseWriter, status int, markup string) error {
	return Blob(w, status, "text/html; charset=utf-8", []byte(markup))
}

// Blob writes bytes with an explicit content type.
//
// An empty contentType becomes application/octet-stream rather than being left for
// net/http to sniff: sniffing turns an uploaded file into whatever it looks like,
// which is how a text/plain upload comes back as HTML.
func Blob(w http.ResponseWriter, status int, contentType string, data []byte) error {
	if err := ensureUncommitted(w, "write response"); err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, err := w.Write(data)
	return err
}

// Stream copies from source with an explicit content type, without buffering it.
//
// Use it for a body that is large or produced as it goes. Because nothing is buffered,
// a read failure happens after the status is committed, and the response is truncated
// rather than replaced with an error — the error is returned so it can be logged.
func Stream(w http.ResponseWriter, status int, contentType string, source io.Reader) error {
	if source == nil {
		return errors.New("ossein: stream source cannot be nil")
	}
	if err := ensureUncommitted(w, "stream response"); err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if _, err := io.Copy(w, source); err != nil {
		return fmt.Errorf("ossein: stream response: %w", err)
	}
	return nil
}

// redirectStatuses are the codes for which Location actually directs the client.
//
// A range check would also admit 300, 304, 305, and 306, where Location means something
// else or nothing at all — 304 in particular must not carry a body, and http.Redirect
// writes one.
var redirectStatuses = map[int]bool{
	http.StatusMovedPermanently:  true,
	http.StatusFound:             true,
	http.StatusSeeOther:          true,
	http.StatusTemporaryRedirect: true,
	http.StatusPermanentRedirect: true,
}

// Redirect answers with a redirect to location.
//
// The status must be one that actually redirects: 301, 302, 303, 307, or 308. Any other
// code leaves Location as something the client is free to ignore, which is a redirect
// that silently does not redirect.
//
// It delegates to http.Redirect, so two of that function's behaviors apply. A GET or
// HEAD receives a short HTML body naming the target, unless the caller has already set
// a Content-Type. And a relative location is resolved against the request path, so
// "next" from /a/b becomes /a/next — worth knowing when the location comes from storage
// rather than from the handler.
func Redirect(w http.ResponseWriter, r *http.Request, status int, location string) error {
	if !redirectStatuses[status] {
		return fmt.Errorf("ossein: status %d does not redirect", status)
	}
	if err := checkHeaderValue("redirect location", location); err != nil {
		return err
	}
	if err := ensureUncommitted(w, "redirect"); err != nil {
		return err
	}

	http.Redirect(w, r, location, status)
	return nil
}

// checkHeaderValue rejects a value that would break out of its header.
//
// Go replaces a carriage return or newline in a header value with a space rather than
// rejecting it, so an injection attempt becomes a header with a quietly mangled value —
// a Location pointing somewhere the caller never wrote. Reporting it is more useful.
func checkHeaderValue(what, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("ossein: %s contains a line break: %q", what, value)
	}
	return nil
}

// ensureUncommitted reports an error when a response has already been written.
//
// Writing a second one appends to the first: the status is whatever was sent, the
// headers are whatever was sent, and the body is both bodies concatenated, with the
// only complaint a "superfluous response.WriteHeader call" on the server's log. The
// check needs the Ossein response writer to be reachable; with a plain net/http writer
// there is nothing to ask, and the write proceeds.
func ensureUncommitted(w http.ResponseWriter, what string) error {
	if tracked, ok := ResponseWriterFrom(w); ok && tracked.Written() {
		return fmt.Errorf(
			"ossein: %s: the response was already written with status %d",
			what, tracked.Status(),
		)
	}
	return nil
}
