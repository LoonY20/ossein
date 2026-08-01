package queue_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/queue"
)

// TestAJobCarriesTheRequestThatEnqueuedIt is the property that makes an
// asynchronous request debuggable: the delivery and its processing share an ID, so
// one grep finds both. Without it, connecting them means guessing from timestamps.
func TestAJobCarriesTheRequestThatEnqueuedIt(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	seen := make(chan queue.Job, 1)
	observed := make(chan string, 1)

	work := queue.NewMemory(queue.WithLogger(logger), queue.WithWorkers(1))
	work.Handle("deferred", func(ctx context.Context, job queue.Job) error {
		observed <- ossein.RequestIDFromContext(ctx)
		ossein.LoggerFromContext(ctx).Info("processing")
		seen <- job
		return nil
	})
	work.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := work.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	app := ossein.New(
		ossein.WithLogger(logger),
		ossein.WithRequestIDGenerator(func() string { return "req-abc" }),
	)
	app.Post("/deliveries", func(c *ossein.Context) error {
		if err := work.Enqueue(c.Context(), queue.Job{Name: "deferred"}); err != nil {
			return err
		}
		c.Logger().Info("accepted")
		return c.NoContent(http.StatusAccepted)
	})

	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/deliveries", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d", recorder.Code)
	}

	var job queue.Job
	select {
	case job = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}

	if job.RequestID != "req-abc" {
		t.Fatalf("job.RequestID = %q, want the enqueuing request's", job.RequestID)
	}
	if got := <-observed; got != "req-abc" {
		t.Fatalf("RequestIDFromContext in the handler = %q", got)
	}
	if output := logs.String(); !strings.Contains(output, "request_id=req-abc") {
		t.Fatalf("the job's log lines do not carry the request ID: %q", output)
	}
	// Both halves under one ID, which is the point.
	if strings.Count(logs.String(), "request_id=req-abc") < 2 {
		t.Fatalf("only one half of the request logged under the ID: %q", logs.String())
	}
}

// TestAnExplicitRequestIDIsKept covers work whose origin the context does not know,
// such as a job replayed by an operator or restored by a durable driver.
func TestAnExplicitRequestIDIsKept(t *testing.T) {
	seen := make(chan queue.Job, 1)

	work := queue.NewMemory(queue.WithLogger(discardLogger()), queue.WithWorkers(1))
	work.Handle("replay", func(_ context.Context, job queue.Job) error {
		seen <- job
		return nil
	})
	work.Start()
	t.Cleanup(func() { stopQueue(t, work) })

	ctx := ossein.ContextWithRequestID(context.Background(), "from-the-context")
	if err := work.Enqueue(ctx, queue.Job{Name: "replay", RequestID: "from-the-caller"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case job := <-seen:
		if job.RequestID != "from-the-caller" {
			t.Fatalf("job.RequestID = %q, want the explicit value", job.RequestID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}
}

// TestAJobWithoutAnOriginLogsWithoutOne keeps a job enqueued outside a request from
// carrying an empty request_id attribute, which reads as a lost ID rather than as
// work that never had one.
func TestAJobWithoutAnOriginLogsWithoutOne(t *testing.T) {
	logs := &syncBuffer{}
	done := make(chan struct{})

	work := queue.NewMemory(
		queue.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		queue.WithWorkers(1),
	)
	work.Handle("scheduled", func(ctx context.Context, _ queue.Job) error {
		if got := ossein.RequestIDFromContext(ctx); got != "" {
			t.Errorf("RequestIDFromContext = %q, want empty", got)
		}
		ossein.LoggerFromContext(ctx).Info("running")
		close(done)
		return nil
	})
	work.Start()
	t.Cleanup(func() { stopQueue(t, work) })

	if err := work.Enqueue(context.Background(), queue.Job{Name: "scheduled"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}

	if strings.Contains(logs.String(), "request_id=") {
		t.Fatalf("a job with no origin logged a request ID: %q", logs.String())
	}
}

// TestTheFailureHandlerSeesTheRequestID matters because a dead letter is where the
// connection is needed most: the record has to name the request that produced it.
func TestTheFailureHandlerSeesTheRequestID(t *testing.T) {
	reported := make(chan string, 1)

	work := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(ctx context.Context, job queue.Job, _ error) {
			if job.RequestID != ossein.RequestIDFromContext(ctx) {
				t.Errorf("job %q and context %q disagree",
					job.RequestID, ossein.RequestIDFromContext(ctx))
			}
			reported <- job.RequestID
		}),
	)
	work.Handle("doomed", func(context.Context, queue.Job) error {
		return errors.New("nope")
	})
	work.Start()
	t.Cleanup(func() { stopQueue(t, work) })

	ctx := ossein.ContextWithRequestID(context.Background(), "req-dead")
	if err := work.Enqueue(ctx, queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case got := <-reported:
		if got != "req-dead" {
			t.Fatalf("the dead letter carries %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never reported")
	}
}

// stopQueue drains a queue in a test cleanup.
func stopQueue(t *testing.T, work *queue.Memory) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := work.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestADeadJobLogsUnderItsRequestID covers the default path, where no failure
// handler is configured and this line is the only record the job died. The ID was
// on the retry warnings and missing from the death, so grepping a request found
// the failures and then lost the outcome — which reads as a job still being
// retried.
func TestADeadJobLogsUnderItsRequestID(t *testing.T) {
	logs := &syncBuffer{}

	work := queue.NewMemory(
		queue.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(2),
		queue.WithBackoff(func(int) time.Duration { return 0 }),
	)
	work.Handle("doomed", func(context.Context, queue.Job) error {
		return errors.New("nope")
	})
	work.Start()

	ctx := ossein.ContextWithRequestID(context.Background(), "req-dead")
	if err := work.Enqueue(ctx, queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	stopQueue(t, work)

	output := logs.String()
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, "request_id=req-dead") {
			t.Fatalf("a line about the job carries no request ID: %q\nfull log: %q",
				line, output)
		}
	}
	if !strings.Contains(output, "exhausted its attempts") {
		t.Fatalf("the death was never logged: %q", output)
	}
}

// TestTheFailureHandlerLoggerCarriesTheRequestID covers the other half of the
// dead-letter contract: the handler writes through the context's logger, so that
// logger has to be scoped to the job rather than bare.
func TestTheFailureHandlerLoggerCarriesTheRequestID(t *testing.T) {
	logs := &syncBuffer{}
	done := make(chan struct{})

	work := queue.NewMemory(
		queue.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(ctx context.Context, _ queue.Job, err error) {
			ossein.LoggerFromContext(ctx).Error("dead letter", "error", err)
			close(done)
		}),
	)
	work.Handle("doomed", func(context.Context, queue.Job) error {
		return errors.New("nope")
	})
	work.Start()
	t.Cleanup(func() { stopQueue(t, work) })

	ctx := ossein.ContextWithRequestID(context.Background(), "req-letter")
	if err := work.Enqueue(ctx, queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never reported")
	}

	if !strings.Contains(logs.String(), `msg="dead letter"`) {
		t.Fatalf("the handler's line is missing: %q", logs.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, "dead letter") && !strings.Contains(line, "request_id=req-letter") {
			t.Fatalf("the dead-letter line carries no request ID: %q", line)
		}
	}
}

// TestTheFailureHandlerContextOutlivesAFailedDrain pins the rule the comment above
// reportFailure states. A drain that ran out of time cancels the job context, and
// deriving the failure context from it would deny the dead-letter write at exactly
// the moment it matters.
func TestTheFailureHandlerContextOutlivesAFailedDrain(t *testing.T) {
	reported := make(chan error, 1)
	release := make(chan struct{})

	work := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(ctx context.Context, _ queue.Job, _ error) {
			reported <- ctx.Err()
		}),
	)
	started := make(chan struct{})
	work.Handle("slow", func(context.Context, queue.Job) error {
		close(started)
		<-release
		return errors.New("nope")
	})
	work.Start()

	if err := work.Enqueue(context.Background(), queue.Job{Name: "slow"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	<-started

	// Give up on the drain, which cancels the job context.
	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := work.Stop(stopCtx); err == nil {
		t.Fatal("Stop reported success while the handler was blocked")
	}
	close(release)

	select {
	case err := <-reported:
		if err != nil {
			t.Fatalf("the failure handler was given a dead context (%v); "+
				"a dead-letter write would be refused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was never reported")
	}
}

// TestEnqueueJSONCarriesTheRequestID covers the other half of the Enqueuer
// interface, which every other test in this file skips.
func TestEnqueueJSONCarriesTheRequestID(t *testing.T) {
	seen := make(chan queue.Job, 1)

	work := queue.NewMemory(queue.WithLogger(discardLogger()), queue.WithWorkers(1))
	work.Handle("encoded", func(_ context.Context, job queue.Job) error {
		seen <- job
		return nil
	})
	work.Start()
	t.Cleanup(func() { stopQueue(t, work) })

	ctx := ossein.ContextWithRequestID(context.Background(), "req-json")
	if err := work.EnqueueJSON(ctx, "encoded", map[string]int{"n": 1}); err != nil {
		t.Fatalf("EnqueueJSON: %v", err)
	}

	select {
	case job := <-seen:
		if job.RequestID != "req-json" {
			t.Fatalf("job.RequestID = %q", job.RequestID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}
}
