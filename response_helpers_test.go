package ossein

import (
	"errors"
	"io/fs"
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

// TestRedirectRejectsALineBreakInTheLocation covers response splitting, and pins the
// reason the check exists. The first version of this comment claimed Go drops such a
// header; it does not — it replaces the line breaks with spaces, so the redirect points
// somewhere the caller never wrote. The sub-test below verifies that rather than
// asserting it.
func TestRedirectRejectsALineBreakInTheLocation(t *testing.T) {
	for _, location := range []string{
		"/ok\r\nX-Injected: yes",
		"/ok\nX-Injected: yes",
		"/ok\rX-Injected: yes",
	} {
		err := Redirect(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
			http.StatusFound, location)
		if err == nil {
			t.Fatalf("location %q was accepted", location)
		}
		if !strings.Contains(err.Error(), "contains a line break") {
			t.Fatalf("error = %v", err)
		}
	}

	t.Run("go mangles rather than drops", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "/ok\r\nX-Injected: yes")
			w.WriteHeader(http.StatusFound)
		}))
		defer server.Close()

		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer response.Body.Close()

		if response.Header.Get("X-Injected") != "" {
			t.Fatal("the header was actually injected")
		}
		if got := response.Header.Get("Location"); got == "" {
			t.Fatal("Go dropped the header after all; the check's rationale needs updating")
		} else if got == "/ok" {
			t.Fatalf("Location = %q, which would be harmless; the rationale needs updating", got)
		}
	})
}

// TestRedirectRejectsAStatusThatDoesNotRedirect covers the codes a range check would
// wave through. 304 must not carry a body and http.Redirect writes one; 300, 305, and
// 306 use Location for something other than "go here", or not at all.
func TestRedirectRejectsAStatusThatDoesNotRedirect(t *testing.T) {
	for _, status := range []int{
		http.StatusMultipleChoices,
		http.StatusNotModified,
		http.StatusUseProxy,
		306,
	} {
		err := Redirect(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
			status, "/new")
		if err == nil {
			t.Fatalf("status %d was accepted as a redirect", status)
		}
	}

	for _, status := range []int{301, 302, 303, 307, 308} {
		recorder := httptest.NewRecorder()
		if err := Redirect(recorder, httptest.NewRequest(http.MethodGet, "/", nil),
			status, "/new"); err != nil {
			t.Fatalf("status %d was rejected: %v", status, err)
		}
		if recorder.Code != status {
			t.Fatalf("status %d produced %d", status, recorder.Code)
		}
	}
}

// TestRedirectDelegatesToNetHTTP pins the two behaviors inherited from http.Redirect,
// so neither is a surprise and neither can be dropped unnoticed: a GET gets a short
// HTML body naming the target, and a relative location resolves against the request.
func TestRedirectDelegatesToNetHTTP(t *testing.T) {
	app := New()
	app.Get("/a/b/c", func(c *Context) error {
		return c.Redirect(http.StatusFound, "next")
	})

	response := serveOnce(app, http.MethodGet, "/a/b/c")

	if got := response.Result().Header.Get("Location"); got != "/a/b/next" {
		t.Fatalf("Location = %q, want the relative target resolved against the request", got)
	}
	if !strings.Contains(response.Body.String(), `<a href="/a/b/next">`) {
		t.Fatalf("body = %q, want the HTML body net/http writes for a GET",
			response.Body.String())
	}
}

// TestHelpersRefuseToWriteOverACommittedResponse covers the mistake that produces a
// response with one status, one set of headers, and two bodies concatenated — whose
// only symptom is a "superfluous response.WriteHeader call" line in the server log.
func TestHelpersRefuseToWriteOverACommittedResponse(t *testing.T) {
	writes := map[string]func(*Context) error{
		"JSON":       func(c *Context) error { return c.JSON(http.StatusOK, map[string]int{"n": 1}) },
		"Text":       func(c *Context) error { return c.Text(http.StatusOK, "second") },
		"Blob":       func(c *Context) error { return c.Blob(http.StatusOK, "text/csv", []byte("x")) },
		"Stream":     func(c *Context) error { return c.Stream(http.StatusOK, "text/csv", strings.NewReader("x")) },
		"Redirect":   func(c *Context) error { return c.Redirect(http.StatusFound, "/elsewhere") },
		"File":       func(c *Context) error { return c.File(writeTempFile(t, "second.txt", "x")) },
		"Attachment": func(c *Context) error { return c.Attachment(writeTempFile(t, "third.txt", "x"), "n.txt") },
	}

	for name, write := range writes {
		var second error
		app := New()
		app.Get("/twice", func(c *Context) error {
			if err := c.Text(http.StatusCreated, "first"); err != nil {
				return err
			}
			second = write(c)
			return nil
		})

		response := serveOnce(app, http.MethodGet, "/twice")

		if second == nil {
			t.Fatalf("%s wrote over a committed response without complaint", name)
		}
		if !strings.Contains(second.Error(), "already written with status 201") {
			t.Fatalf("%s error = %v", name, second)
		}
		if response.Body.String() != "first" {
			t.Fatalf("%s appended to the first response: %q", name, response.Body.String())
		}
	}
}

// TestFileReportsAMissingPath is why File does not simply delegate. ServeFile answers a
// missing file with a plain-text "404 page not found", which in a JSON API is a second
// error contract, and returns nothing the handler could notice.
func TestFileReportsAMissingPath(t *testing.T) {
	app := New()
	app.Get("/missing", func(c *Context) error {
		return c.File(filepath.Join(t.TempDir(), "absent.txt"))
	})
	app.Get("/directory", func(c *Context) error {
		return c.File(t.TempDir())
	})

	for _, path := range []string{"/missing", "/directory"} {
		response := serveOnce(app, http.MethodGet, path)

		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, response.Code)
		}
		if !strings.Contains(response.Body.String(), "file_not_found") {
			t.Fatalf("%s body = %q, want the application's error contract",
				path, response.Body.String())
		}
	}
}

func TestFileFSReportsAMissingName(t *testing.T) {
	app := New()
	app.Get("/assets/{name}", func(c *Context) error {
		return c.FileFS(fstest.MapFS{"logo.svg": &fstest.MapFile{Data: []byte("<svg/>")}},
			c.Param("name"))
	})

	response := serveOnce(app, http.MethodGet, "/assets/absent.svg")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "file_not_found") {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestAttachmentDoesNotLabelAnErrorResponse covers the ordering. Setting the header
// first and letting the file lookup fail leaves the download name — often user data —
// on a 404, and a browser saves the error page under it.
func TestAttachmentDoesNotLabelAnErrorResponse(t *testing.T) {
	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment(filepath.Join(t.TempDir(), "absent.csv"), "customer-list.csv")
	})

	response := serveOnce(app, http.MethodGet, "/download")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q on an error response", got)
	}
}

// TestBlobAndStreamReportAFailedWrite keeps a disconnected client from looking like a
// successful response, which is what an access log would then record.
func TestBlobAndStreamReportAFailedWrite(t *testing.T) {
	failure := errors.New("connection reset")

	if err := Blob(&refusingWriter{header: http.Header{}, err: failure},
		http.StatusOK, "text/plain", []byte("x")); !errors.Is(err, failure) {
		t.Fatalf("Blob error = %v, want the write failure", err)
	}
	if err := Stream(&refusingWriter{header: http.Header{}, err: failure},
		http.StatusOK, "text/plain", strings.NewReader("x")); !errors.Is(err, failure) {
		t.Fatalf("Stream error = %v, want the write failure", err)
	}
}

func TestStreamUsesTheGivenStatus(t *testing.T) {
	app := New()
	app.Get("/stream", func(c *Context) error {
		return c.Stream(http.StatusAccepted, "text/plain", strings.NewReader("queued"))
	})

	if response := serveOnce(app, http.MethodGet, "/stream"); response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
}

// writeTempFile creates a file and returns its path.
func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// refusingWriter fails every write, the shape of a client that has gone away.
type refusingWriter struct {
	header http.Header
	err    error
}

func (w *refusingWriter) Header() http.Header       { return w.header }
func (w *refusingWriter) WriteHeader(int)           {}
func (w *refusingWriter) Write([]byte) (int, error) { return 0, w.err }

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

// TestFileFSRejectsADirectoryAndReportsOtherFailures covers the two remaining lookup
// outcomes. A directory is not a file, so it is a 404; an unreadable one is the
// server's problem and must not be reported as absent, which would send someone
// hunting for content that is there.
func TestFileFSRejectsADirectoryAndReportsOtherFailures(t *testing.T) {
	assets := fstest.MapFS{
		"images/logo.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}

	app := New()
	app.Get("/dir", func(c *Context) error {
		return c.FileFS(assets, "images")
	})
	app.Get("/unreadable", func(c *Context) error {
		return c.FileFS(refusingFS{}, "locked.svg")
	})

	directory := serveOnce(app, http.MethodGet, "/dir")
	if directory.Code != http.StatusNotFound {
		t.Fatalf("directory status = %d, want 404", directory.Code)
	}

	unreadable := serveOnce(app, http.MethodGet, "/unreadable")
	if unreadable.Code != http.StatusInternalServerError {
		t.Fatalf("unreadable status = %d, want 500 (body %q)",
			unreadable.Code, unreadable.Body.String())
	}
	if strings.Contains(unreadable.Body.String(), "file_not_found") {
		t.Fatalf("an unreadable file was reported as missing: %q", unreadable.Body.String())
	}
}

// refusingFS denies every lookup, the shape of a permission problem or a broken mount.
type refusingFS struct{}

func (refusingFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

func TestAttachmentRejectsADirectory(t *testing.T) {
	app := New()
	app.Get("/download", func(c *Context) error {
		return c.Attachment(t.TempDir(), "data.csv")
	})

	response := serveOnce(app, http.MethodGet, "/download")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Result().Header.Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q on an error response", got)
	}
}

func TestFileFSRefusesToWriteOverACommittedResponse(t *testing.T) {
	assets := fstest.MapFS{"logo.svg": &fstest.MapFile{Data: []byte("<svg/>")}}

	var second error
	app := New()
	app.Get("/twice", func(c *Context) error {
		if err := c.Text(http.StatusCreated, "first"); err != nil {
			return err
		}
		second = c.FileFS(assets, "logo.svg")
		return nil
	})

	response := serveOnce(app, http.MethodGet, "/twice")
	if second == nil {
		t.Fatal("FileFS wrote over a committed response without complaint")
	}
	if response.Body.String() != "first" {
		t.Fatalf("body = %q", response.Body.String())
	}
}
