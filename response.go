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

// Redirect answers with a redirect to location.
//
// The status must be a 3xx, because every other code leaves the Location header as
// something the client is free to ignore — a redirect that silently does not redirect.
func Redirect(w http.ResponseWriter, r *http.Request, status int, location string) error {
	if status < http.StatusMultipleChoices || status > http.StatusPermanentRedirect {
		return fmt.Errorf("ossein: redirect status %d is not a 3xx", status)
	}
	if err := checkHeaderValue("redirect location", location); err != nil {
		return err
	}

	http.Redirect(w, r, location, status)
	return nil
}

// checkHeaderValue rejects a value that would break out of its header.
//
// Go's own header writer drops a header whose value contains a newline, which turns an
// injection attempt into a response that is merely missing a header — correct, and
// impossible to debug. Reporting it is more useful.
func checkHeaderValue(what, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("ossein: %s contains a newline: %q", what, value)
	}
	return nil
}
