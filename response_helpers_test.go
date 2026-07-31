package ossein

import (
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTextResponse(t *testing.T) {
	app := New()
	app.Get("/text", func(c *Context) error {
		return c.Text(http.StatusOK, "hello")
	})

	response := serveOnce(app, http.MethodGet, "/text")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if response.Body.String() != "hello" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestHTMLResponse(t *testing.T) {
	app := New()
	app.Get("/html", func(c *Context) error {
		return c.HTML(http.StatusOK, "<p>hi</p>")
	})

	response := serveOnce(app, http.MethodGet, "/html")
	if got := response.Result().Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if response.Body.String() != "<p>hi</p>" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestBlobDefaultsToOctetStream keeps net/http from sniffing the body. Sniffing is how
// a text upload comes back as HTML and runs in the browser.
func TestBlobDefaultsToOctetStream(t *testing.T) {
	app := New()
	app.Get("/blob", func(c *Context) error {
		return c.Blob(http.StatusOK, "", []byte("<script>alert(1)</script>"))
	})

	response := serveOnce(app, http.MethodGet, "/blob")
	if got := response.Result().Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want the non-sniffing default", got)
	}
}

func TestBlobUsesTheGivenContentType(t *testing.T) {
	app := New()
	app.Get("/csv", func(c *Context) error {
		return c.Blob(http.StatusCreated, "text/csv", []byte("a,b\n1,2\n"))
	})

	response := serveOnce(app, http.MethodGet, "/csv")
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Content-Type"); got != "text/csv" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// TestResponseHelpersRecordStatusAndSize is the property that makes these helpers worth
// having over writing to the raw writer: everything downstream that reads the status —
// the access log, the committed-response guard — keeps working.
func TestResponseHelpersRecordStatusAndSize(t *testing.T) {
	app := New()

	var tracked *ResponseWriter
	app.Get("/tracked", func(c *Context) error {
		if err := c.Text(http.StatusTeapot, "short and stout"); err != nil {
			return err
		}
		writer, ok := ResponseWriterFrom(c.Response)
		if !ok {
			t.Fatal("the Ossein response writer was not reachable")
		}
		tracked = writer
		return nil
	})

	serveOnce(app, http.MethodGet, "/tracked")

	if tracked == nil {
		t.Fatal("handler did not run")
	}
	if tracked.Status() != http.StatusTeapot {
		t.Fatalf("Status() = %d", tracked.Status())
	}
	if tracked.BytesWritten() != int64(len("short and stout")) {
		t.Fatalf("BytesWritten() = %d", tracked.BytesWritten())
	}
	if !tracked.Written() {
		t.Fatal("Written() = false after a body was written")
	}
}

func TestStreamCopiesWithoutBuffering(t *testing.T) {
	app := New()
	app.Get("/stream", func(c *Context) error {
		return c.Stream(http.StatusOK, "text/csv", strings.NewReader("a,b\n1,2\n"))
	})

	response := serveOnce(app, http.MethodGet, "/stream")
	if got := response.Result().Header.Get("Content-Type"); got != "text/csv" {
		t.Fatalf("Content-Type = %q", got)
	}
	if response.Body.String() != "a,b\n1,2\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestStreamReportsAReadFailureAfterCommitting documents the trade-off: nothing is
// buffered, so a source that fails mid-body truncates the response instead of being
// replaced by an error page. The error still reaches the caller so it can be logged.
func TestStreamReportsAReadFailureAfterCommitting(t *testing.T) {
	failure := errors.New("source failed")
	app := New()
	app.Get("/broken", func(c *Context) error {
		return c.Stream(http.StatusOK, "text/plain",
			&failingReader{prefix: "partial", err: failure})
	})

	response := serveOnce(app, http.MethodGet, "/broken")

	// The status was already sent, so the error handler must not write over it.
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", response.Code)
	}
	if response.Body.String() != "partial" {
		t.Fatalf("body = %q, want the bytes written before the failure", response.Body.String())
	}
}

func TestStreamRejectsANilSource(t *testing.T) {
	if err := Stream(httptest.NewRecorder(), http.StatusOK, "text/plain", nil); err == nil {
		t.Fatal("expected an error for a nil source")
	}
}

func TestRedirectSetsLocation(t *testing.T) {
	app := New()
	app.Get("/old", func(c *Context) error {
		return c.Redirect(http.StatusMovedPermanently, "/new")
	})

	response := serveOnce(app, http.MethodGet, "/old")
	if response.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Location"); got != "/new" {
		t.Fatalf("Location = %q", got)
	}
}

// TestRedirectRejectsANonRedirectStatus catches the mistake at the call site. With a
// 200, the Location header is advisory and the client simply renders the body, so a
// redirect that never redirects is invisible until someone reports the bug.
func TestRedirectRejectsANonRedirectStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNotFound, 299, 309} {
		app := New()
		app.Get("/go", func(c *Context) error {
			return c.Redirect(status, "/elsewhere")
		})

		response := serveOnce(app, http.MethodGet, "/go")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status %d was accepted as a redirect (response %d)", status, response.Code)
		}
		if got := response.Result().Header.Get("Location"); got != "" {
			t.Fatalf("status %d set Location = %q", status, got)
		}
	}
}

// TestRedirectRejectsANewlineInTheLocation covers response splitting. Go drops a header
// whose value has a newline, which turns an injection attempt into a redirect that
// silently does not redirect; reporting it is both safe and debuggable.
func TestRedirectRejectsANewlineInTheLocation(t *testing.T) {
	err := Redirect(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		http.StatusFound, "/ok\r\nX-Injected: yes")
	if err == nil {
		t.Fatal("a location with a newline was accepted")
	}
	if !strings.Contains(err.Error(), "contains a newline") {
		t.Fatalf("error = %v", err)
	}
}

func TestFileServesFromDisk(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app := New()
	app.Get("/report", func(c *Context) error {
		return c.File(path)
	})

	response := serveOnce(app, http.MethodGet, "/report")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.String() != "a,b\n1,2\n" {
		t.Fatalf("body = %q", response.Body.String())
	}

	// Range support is what delegating to net/http buys. It is asserted instead of the
	// content type because that comes from the machine's mime registry on Windows and
	// differs between developers and CI.
	if got := response.Result().Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want net/http's file serving", got)
	}

	ranged := httptest.NewRequest(http.MethodGet, "/report", nil)
	ranged.Header.Set("Range", "bytes=0-2")
	partial := httptest.NewRecorder()
	app.ServeHTTP(partial, ranged)

	if partial.Code != http.StatusPartialContent {
		t.Fatalf("range request status = %d, want 206", partial.Code)
	}
	if partial.Body.String() != "a,b" {
		t.Fatalf("range body = %q, want the requested bytes", partial.Body.String())
	}
}

// TestFileFSCannotEscapeTheFilesystem is why FileFS exists next to File: a name taken
// from the request cannot be allowed to walk out of the directory it is served from.
func TestFileFSCannotEscapeTheFilesystem(t *testing.T) {
	assets := fstest.MapFS{
		"logo.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}

	app := New()
	app.Get("/assets/{name}", func(c *Context) error {
		return c.FileFS(assets, c.Param("name"))
	})

	found := serveOnce(app, http.MethodGet, "/assets/logo.svg")
	if found.Code != http.StatusOK || found.Body.String() != "<svg/>" {
		t.Fatalf("status = %d body = %q", found.Code, found.Body.String())
	}

	escaped := serveOnce(app, http.MethodGet, "/assets/"+"..%2f..%2fsecrets.txt")
	if escaped.Code == http.StatusOK {
		t.Fatalf("a traversal attempt was served: %q", escaped.Body.String())
	}
}

func TestAttachmentSetsContentDisposition(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "data.csv")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment(path, "Q3 report.csv")
	})

	response := serveOnce(app, http.MethodGet, "/download")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}

	disposition := response.Result().Header.Get("Content-Disposition")
	kind, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("Content-Disposition %q does not parse: %v", disposition, err)
	}
	if kind != "attachment" {
		t.Fatalf("disposition kind = %q", kind)
	}
	if params["filename"] != "Q3 report.csv" {
		t.Fatalf("filename = %q, want the name to survive encoding", params["filename"])
	}
}

// TestAttachmentEncodesAHostileFilename covers the case that makes hand-writing this
// header a bug: a download name is often user data, and a quote in it would otherwise
// end the parameter early and let the rest of the name become new header parameters.
func TestAttachmentEncodesAHostileFilename(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "data.csv")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment(path, `evil"; filename="passwd`)
	})

	response := serveOnce(app, http.MethodGet, "/download")
	disposition := response.Result().Header.Get("Content-Disposition")

	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("Content-Disposition %q does not parse: %v", disposition, err)
	}
	if params["filename"] != `evil"; filename="passwd` {
		t.Fatalf("filename = %q, want the whole name as one parameter", params["filename"])
	}
}

func TestAttachmentRejectsAnEmptyFilename(t *testing.T) {
	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment("irrelevant", "")
	})

	response := serveOnce(app, http.MethodGet, "/download")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the error to surface", response.Code)
	}
}

// serveOnce runs one request through an app and returns the recorder.
func serveOnce(app *App, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

// TestAttachmentEncodesANewlineRatherThanDroppingIt covers the other half of the header
// injection case: mime.FormatMediaType percent-encodes into the filename* form, so a
// name that could not otherwise be written survives intact instead of being dropped.
func TestAttachmentEncodesANewlineRatherThanDroppingIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "data.csv")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment(path, "report\r\nX-Injected: yes.csv")
	})

	response := serveOnce(app, http.MethodGet, "/download")
	disposition := response.Result().Header.Get("Content-Disposition")

	if strings.ContainsAny(disposition, "\r\n") {
		t.Fatalf("Content-Disposition contains a raw newline: %q", disposition)
	}
	if response.Result().Header.Get("X-Injected") != "" {
		t.Fatal("the filename injected a header")
	}
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("Content-Disposition %q does not parse: %v", disposition, err)
	}
	if params["filename"] != "report\r\nX-Injected: yes.csv" {
		t.Fatalf("filename = %q, want the whole name preserved", params["filename"])
	}
}

// TestStreamDefaultsToOctetStream matches Blob: an unspecified type is stated rather
// than left for net/http to guess from the first bytes.
func TestStreamDefaultsToOctetStream(t *testing.T) {
	app := New()
	app.Get("/stream", func(c *Context) error {
		return c.Stream(http.StatusOK, "", strings.NewReader("<html>"))
	})

	response := serveOnce(app, http.MethodGet, "/stream")
	if got := response.Result().Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
}
