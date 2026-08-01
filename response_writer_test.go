package ossein

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestResponseWriterRecordsStatusAndSize(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewResponseWriter(recorder)

	if writer.Written() {
		t.Fatal("new response writer reports a committed response")
	}

	writer.WriteHeader(http.StatusTeapot)
	written, err := writer.Write([]byte("short and stout"))
	if err != nil {
		t.Fatal(err)
	}

	if !writer.Written() {
		t.Fatal("response writer does not report a committed response")
	}
	if writer.Status() != http.StatusTeapot {
		t.Fatalf("expected status %d, got %d", http.StatusTeapot, writer.Status())
	}
	if writer.BytesWritten() != int64(written) {
		t.Fatalf("expected %d bytes written, got %d", written, writer.BytesWritten())
	}
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("underlying writer received status %d", recorder.Code)
	}
	if recorder.Body.String() != "short and stout" {
		t.Fatalf("underlying writer received body %q", recorder.Body.String())
	}
}

func TestResponseWriterDefaultsToOKOnWrite(t *testing.T) {
	writer := NewResponseWriter(httptest.NewRecorder())
	if _, err := writer.Write([]byte("implicit")); err != nil {
		t.Fatal(err)
	}
	if writer.Status() != http.StatusOK {
		t.Fatalf("expected implicit status %d, got %d", http.StatusOK, writer.Status())
	}
	if !writer.Written() {
		t.Fatal("response writer does not report a committed response")
	}
}

func TestResponseWriterKeepsFirstStatus(t *testing.T) {
	writer := NewResponseWriter(httptest.NewRecorder())
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusConflict)
	if writer.Status() != http.StatusAccepted {
		t.Fatalf("expected first status %d, got %d", http.StatusAccepted, writer.Status())
	}
}

func TestResponseWriterDoesNotDoubleWrap(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewResponseWriter(recorder)
	if NewResponseWriter(writer) != writer {
		t.Fatal("wrapping a wrapped writer created a second wrapper")
	}
	if writer.Unwrap() != http.ResponseWriter(recorder) {
		t.Fatal("Unwrap does not return the underlying writer")
	}
}

func TestHandlersReceiveResponseWriter(t *testing.T) {
	app := New()
	var osseinWrapped, nativeWrapped bool
	app.Get("/ossein", func(ctx *Context) error {
		_, osseinWrapped = ResponseWriterFrom(ctx.Response)
		return ctx.NoContent(http.StatusNoContent)
	})
	app.HandleHTTPFunc(http.MethodGet, "/native", func(w http.ResponseWriter, r *http.Request) {
		_, nativeWrapped = ResponseWriterFrom(w)
		w.WriteHeader(http.StatusNoContent)
	})

	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ossein", nil))
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/native", nil))

	if !osseinWrapped {
		t.Fatal("Ossein handler did not receive a wrapped response writer")
	}
	if !nativeWrapped {
		t.Fatal("native handler did not receive a wrapped response writer")
	}
}

func TestErrorHandlerSkipsCommittedResponse(t *testing.T) {
	app := New()
	app.Get("/partial", func(ctx *Context) error {
		ctx.Response.WriteHeader(http.StatusOK)
		if _, err := ctx.Response.Write([]byte("partial")); err != nil {
			return err
		}
		return errors.New("boom")
	})

	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/partial", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if body := response.Body.String(); body != "partial" {
		t.Fatalf("error handler wrote over a committed response: %q", body)
	}
}

// TestResponseWriterStateIsSafeToReadWhileWriting is why the recorded state is atomic.
// A response is written by one goroutine, but middleware.Timeout answers from the
// request goroutine while the handler is still running on its own, so a handler asking
// whether the response is committed reads what the timeout just wrote.
//
// Without -race this test cannot fail; CI runs with it, which is where the plain int
// fields were caught.
func TestResponseWriterStateIsSafeToReadWhileWriting(t *testing.T) {
	writer := NewResponseWriter(httptest.NewRecorder())

	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		writer.WriteHeader(http.StatusGatewayTimeout)
		for i := 0; i < 200; i++ {
			_, _ = writer.Write([]byte("x"))
		}
	}()

	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 200; i++ {
			_ = writer.Written()
			_ = writer.Status()
			_ = writer.BytesWritten()
		}
	}()

	group.Wait()

	if writer.Status() != http.StatusGatewayTimeout {
		t.Fatalf("Status() = %d", writer.Status())
	}
	if writer.BytesWritten() != 200 {
		t.Fatalf("BytesWritten() = %d", writer.BytesWritten())
	}
}
