package ossein

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestFallbackHandlerWithoutResponseStillCommits keeps the guarantee that an
// unmatched route always produces a status. A handler that returns nil without
// writing must not leave an implicit empty 200 behind, which is what happens
// once the mux's own response has been suppressed.
func TestFallbackHandlerWithoutResponseStillCommits(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		app := newRoutedApp()
		app.SetNotFoundHandler(func(*Context) error { return nil })

		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %q)", response.Code, response.Body.String())
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		app := newRoutedApp()
		app.SetMethodNotAllowedHandler(func(*Context) error { return nil })

		response := httptest.NewRecorder()
		app.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/items", nil))

		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", response.Code)
		}
	})
}

// TestMountedServeMuxKeepsItsOwnResponses covers a route that delegates to
// another http.ServeMux with the same request. The nested mux mutates
// Request.Pattern as a side effect, so a fallback keyed on that field would both
// hijack the nested 404 and, worse, start discarding the outer handler's writes.
func TestMountedServeMuxKeepsItsOwnResponses(t *testing.T) {
	nested := http.NewServeMux()
	nested.HandleFunc("/debug/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	app := New()
	app.SetNotFoundHandler(func(*Context) error {
		return NewHTTPError(http.StatusTeapot, "app_fallback", "app fallback ran")
	})
	app.HandleHTTPFunc(http.MethodGet, "/debug/{rest...}",
		func(w http.ResponseWriter, r *http.Request) {
			nested.ServeHTTP(w, r)
		})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/unknown", nil))

	if response.Code == http.StatusTeapot {
		t.Fatal("the application fallback hijacked a mounted mux's own 404")
	}
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the nested mux's 404", response.Code)
	}
}

// TestWritesAfterMountedMuxMissAreNotDiscarded is the data-loss case: after a
// nested mux misses, the outer handler keeps writing and every byte must reach
// the client.
func TestWritesAfterMountedMuxMissAreNotDiscarded(t *testing.T) {
	nested := http.NewServeMux()
	nested.HandleFunc("/api/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	const payload = "handler output that must reach the client"

	app := New()
	var written int
	var writeErr error
	app.HandleHTTPFunc(http.MethodGet, "/api/{rest...}",
		func(w http.ResponseWriter, r *http.Request) {
			// Probe the nested mux, ignore its miss, then answer ourselves.
			nested.ServeHTTP(&discardResponse{header: make(http.Header)}, r)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			written, writeErr = w.Write([]byte(payload))
		})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))

	if writeErr != nil {
		t.Fatalf("Write error = %v", writeErr)
	}
	if written != len(payload) {
		t.Fatalf("Write returned %d, want %d", written, len(payload))
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if body := response.Body.String(); body != payload {
		t.Fatalf("body = %q, want %q", body, payload)
	}
}

// discardResponse lets a handler probe another mux without touching the real
// response.
type discardResponse struct {
	header http.Header
	status int
}

func (w *discardResponse) Header() http.Header         { return w.header }
func (w *discardResponse) WriteHeader(status int)      { w.status = status }
func (w *discardResponse) Write(b []byte) (int, error) { return len(b), nil }

// TestMiddlewareHeadersSurviveOnMissPath keeps security headers consistent
// between matched and unmatched requests.
func TestMiddlewareHeadersSurviveOnMissPath(t *testing.T) {
	app := newRoutedApp()
	app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			next.ServeHTTP(w, r)
		})
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff to survive the miss path", got)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "json") {
		t.Fatalf("Content-Type = %q, want JSON", contentType)
	}
}

// TestFallbackSettersPanicAfterServing matches the freeze invariant that Use and
// route registration already enforce, and closes an unsynchronized write to a
// field the request path reads.
func TestFallbackSettersPanicAfterServing(t *testing.T) {
	cases := map[string]func(*App){
		"not found": func(app *App) { app.SetNotFoundHandler(func(*Context) error { return nil }) },
		"method not allowed": func(app *App) {
			app.SetMethodNotAllowedHandler(func(*Context) error { return nil })
		},
	}

	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			app := newRoutedApp()
			app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))

			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("expected a panic when setting a fallback after serving")
				}
			}()
			set(app)
		})
	}
}

// selfUnwrapper returns itself from Unwrap, the shape that would spin a naive
// unwrapping loop forever.
type selfUnwrapper struct {
	http.ResponseWriter
}

func (w *selfUnwrapper) Unwrap() http.ResponseWriter { return w }

func TestResponseWriterFromTerminatesOnSelfUnwrap(t *testing.T) {
	done := make(chan struct{})
	var found bool

	go func() {
		defer close(done)
		_, found = ResponseWriterFrom(&selfUnwrapper{ResponseWriter: httptest.NewRecorder()})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ResponseWriterFrom did not terminate on a self-unwrapping writer")
	}
	if found {
		t.Fatal("expected no Ossein writer to be reported")
	}
}

// nilUnwrapper hands back a typed nil, which satisfies a type assertion while
// being unusable.
type nilUnwrapper struct {
	http.ResponseWriter
}

func (w *nilUnwrapper) Unwrap() http.ResponseWriter {
	var missing *ResponseWriter
	return missing
}

func TestResponseWriterFromRejectsTypedNil(t *testing.T) {
	writer, found := ResponseWriterFrom(&nilUnwrapper{ResponseWriter: httptest.NewRecorder()})
	if found && writer == nil {
		t.Fatal("reported a nil *ResponseWriter as found; callers would panic on Written()")
	}
}

// TestResponseWriterFromFollowsWrapperChain is the direct unit test for the
// unwrapping behavior.
func TestResponseWriterFromFollowsWrapperChain(t *testing.T) {
	base := NewResponseWriter(httptest.NewRecorder())
	outer := &selfDescribingWrapper{ResponseWriter: &selfDescribingWrapper{ResponseWriter: base}}

	found, ok := ResponseWriterFrom(outer)
	if !ok {
		t.Fatal("expected the wrapped Ossein writer to be found")
	}
	if found != base {
		t.Fatal("found a different writer than the one wrapped")
	}

	if _, ok := ResponseWriterFrom(httptest.NewRecorder()); ok {
		t.Fatal("a plain recorder must not report an Ossein writer")
	}
}

type selfDescribingWrapper struct {
	http.ResponseWriter
}

func (w *selfDescribingWrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }
