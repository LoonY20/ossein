package queue_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/queue"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// started returns a queue already running, stopped when the test ends.
func started(t *testing.T, options ...queue.Option) *queue.Memory {
	t.Helper()
	all := append([]queue.Option{queue.WithLogger(discardLogger())}, options...)
	worker := queue.NewMemory(all...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return worker
}

func TestEnqueueRunsTheHandler(t *testing.T) {
	worker := started(t)

	received := make(chan queue.Job, 1)
	worker.Handle("invoice.paid", func(_ context.Context, job queue.Job) error {
		received <- job
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{
		Name:    "invoice.paid",
		Payload: []byte(`{"id":7}`),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case job := <-received:
		if job.Name != "invoice.paid" {
			t.Fatalf("name = %q", job.Name)
		}
		if string(job.Payload) != `{"id":7}` {
			t.Fatalf("payload = %q", job.Payload)
		}
		if job.Attempt != 1 {
			t.Fatalf("attempt = %d, want 1 on the first try", job.Attempt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the job was never processed")
	}
}

// TestEnqueueRejectsAnUnknownJob fails at the point of the mistake rather than
// discovering it on a worker goroutine after the caller has moved on.
func TestEnqueueRejectsAnUnknownJob(t *testing.T) {
	worker := started(t)
	worker.Start()

	err := worker.Enqueue(context.Background(), queue.Job{Name: "nobody.handles.this"})
	if !errors.Is(err, queue.ErrNoHandler) {
		t.Fatalf("error = %v, want ErrNoHandler", err)
	}
}

func TestEnqueueJSONRoundTrip(t *testing.T) {
	worker := started(t)

	type delivery struct {
		ID int `json:"id"`
	}
	received := make(chan delivery, 1)
	worker.Handle("delivery", func(_ context.Context, job queue.Job) error {
		var decoded delivery
		if err := json.Unmarshal(job.Payload, &decoded); err != nil {
			return err
		}
		received <- decoded
		return nil
	})
	worker.Start()

	if err := worker.EnqueueJSON(context.Background(), "delivery", delivery{ID: 42}); err != nil {
		t.Fatalf("EnqueueJSON: %v", err)
	}

	select {
	case decoded := <-received:
		if decoded.ID != 42 {
			t.Fatalf("id = %d", decoded.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the job was never processed")
	}
}

// TestRetriesWithBackoff covers the retry loop and the attempt counter a handler
// needs to make an idempotent decision.
func TestRetriesWithBackoff(t *testing.T) {
	worker := started(t,
		queue.WithMaxAttempts(3),
		queue.WithBackoff(func(int) time.Duration { return time.Millisecond }),
	)

	attempts := make(chan int, 4)
	worker.Handle("flaky", func(_ context.Context, job queue.Job) error {
		attempts <- job.Attempt
		if job.Attempt < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "flaky"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	for want := 1; want <= 3; want++ {
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("attempt = %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("attempt %d never happened", want)
		}
	}

	select {
	case extra := <-attempts:
		t.Fatalf("attempt %d happened after success", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestExhaustedAttemptsReportFailureOnce is the seed of dead-letter handling: the
// application gets one callback with the job and the last error.
func TestExhaustedAttemptsReportFailureOnce(t *testing.T) {
	failures := make(chan error, 4)
	expected := errors.New("permanent failure")

	worker := started(t,
		queue.WithMaxAttempts(2),
		queue.WithBackoff(func(int) time.Duration { return time.Millisecond }),
		queue.WithFailureHandler(func(_ context.Context, job queue.Job, err error) {
			if job.Attempt != 2 {
				t.Errorf("failed job attempt = %d, want the last one", job.Attempt)
			}
			failures <- err
		}),
	)
	worker.Handle("doomed", func(context.Context, queue.Job) error { return expected })
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case err := <-failures:
		if !errors.Is(err, expected) {
			t.Fatalf("failure error = %v, want %v", err, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the failure was never reported")
	}

	select {
	case <-failures:
		t.Fatal("the failure was reported more than once")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestAPanickingHandlerDoesNotStopTheWorker keeps one bad job from taking down the
// pool, which would silently stop all deferred work.
func TestAPanickingHandlerDoesNotStopTheWorker(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	worker := queue.NewMemory(
		queue.WithLogger(logger),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})

	survived := make(chan struct{})
	worker.Handle("boom", func(context.Context, queue.Job) error {
		panic("kaboom")
	})
	worker.Handle("after", func(context.Context, queue.Job) error {
		close(survived)
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "boom"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "after"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case <-survived:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker stopped after a panicking job")
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("log = %q, want the panic recorded", logs.String())
	}
}

// TestAPanicIsTreatedAsAFailure keeps a panicking job on the same retry and reporting
// path as a returned error.
func TestAPanicIsTreatedAsAFailure(t *testing.T) {
	failures := make(chan error, 1)
	worker := started(t,
		queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(_ context.Context, _ queue.Job, err error) {
			failures <- err
		}),
	)
	worker.Handle("boom", func(context.Context, queue.Job) error {
		panic("kaboom")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "boom"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case err := <-failures:
		if !strings.Contains(err.Error(), "kaboom") {
			t.Fatalf("error = %v, want the panic value", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the panic was not reported as a failure")
	}
}

// TestEnqueueReportsBackPressure keeps an HTTP handler from blocking on a full queue.
func TestEnqueueReportsBackPressure(t *testing.T) {
	release := make(chan struct{})
	worker := started(t, queue.WithWorkers(1), queue.WithBuffer(1))
	worker.Handle("slow", func(context.Context, queue.Job) error {
		<-release
		return nil
	})
	worker.Start()
	defer close(release)

	// One job occupies the worker, one fills the buffer, the rest must be refused
	// rather than blocking.
	var refused error
	for i := 0; i < 20; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "slow"}); err != nil {
			refused = err
			break
		}
	}

	if !errors.Is(refused, queue.ErrFull) {
		t.Fatalf("error = %v, want ErrFull", refused)
	}
}

// TestEnqueueAfterStopIsRefused keeps work from being accepted once shutdown began.
func TestEnqueueAfterStopIsRefused(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
	worker.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	err := worker.Enqueue(context.Background(), queue.Job{Name: "job"})
	if !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("error = %v, want ErrClosed", err)
	}
}

// TestStopDrainsQueuedWork is the guarantee that makes a queue safe to deploy: work
// already accepted is finished before the process exits.
func TestStopDrainsQueuedWork(t *testing.T) {
	var processed atomic.Int64
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(2),
		queue.WithBuffer(64),
	)
	worker.Handle("slow", func(context.Context, queue.Job) error {
		time.Sleep(10 * time.Millisecond)
		processed.Add(1)
		return nil
	})
	worker.Start()

	const jobs = 16
	for i := 0; i < jobs; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "slow"}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := processed.Load(); got != jobs {
		t.Fatalf("processed %d of %d jobs before stopping", got, jobs)
	}
}

// TestStopReportsAnIncompleteDrain keeps a shutdown deadline from silently dropping
// work.
func TestStopReportsAnIncompleteDrain(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
	)
	release := make(chan struct{})
	defer close(release)

	worker.Handle("stuck", func(context.Context, queue.Job) error {
		<-release
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "stuck"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := worker.Stop(ctx); err == nil {
		t.Fatal("expected an error when the drain could not finish")
	}
}

// TestRegisterTiesTheQueueToTheApplicationLifecycle is the wiring an application
// should not have to write.
func TestRegisterTiesTheQueueToTheApplicationLifecycle(t *testing.T) {
	processed := make(chan struct{})
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))
	worker.Handle("job", func(context.Context, queue.Job) error {
		close(processed)
		return nil
	})

	app := ossein.New()
	if err := queue.Register(app, worker); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("the job was never processed after Start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("error after app.Stop = %v, want ErrClosed", err)
	}
}

// TestRegisteredQueueIsResolvableAsAnEnqueuer covers the container wiring a handler
// needs to submit work.
func TestRegisteredQueueIsResolvableAsAnEnqueuer(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })

	app := ossein.New()
	if err := queue.Register(app, worker); err != nil {
		t.Fatalf("Register: %v", err)
	}

	resolved, err := ossein.Resolve[queue.Enqueuer](app)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved == nil {
		t.Fatal("resolved a nil enqueuer")
	}
}

// TestHandleAfterStartPanics matches the framework's freeze invariant: the dispatch
// table is read by every worker, so it must be complete before they run.
func TestHandleAfterStartPanics(t *testing.T) {
	worker := started(t)
	worker.Start()

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic when registering a handler after Start")
		}
	}()
	worker.Handle("late", func(context.Context, queue.Job) error { return nil })
}

// TestDuplicateHandlerPanics keeps a silently ignored registration from looking like
// it took effect.
func TestDuplicateHandlerPanics(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a panic for a duplicate handler")
		}
	}()
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
}

// TestWorkersRunInParallel confirms the pool size is honoured.
func TestWorkersRunInParallel(t *testing.T) {
	const workers = 4
	worker := started(t, queue.WithWorkers(workers), queue.WithBuffer(workers))

	var concurrent atomic.Int64
	var peak atomic.Int64
	release := make(chan struct{})
	defer close(release)

	worker.Handle("parallel", func(context.Context, queue.Job) error {
		running := concurrent.Add(1)
		for {
			observed := peak.Load()
			if running <= observed || peak.CompareAndSwap(observed, running) {
				break
			}
		}
		<-release
		concurrent.Add(-1)
		return nil
	})
	worker.Start()

	for i := 0; i < workers; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "parallel"}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && peak.Load() < int64(workers) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := peak.Load(); got != workers {
		t.Fatalf("peak concurrency = %d, want %d", got, workers)
	}
}

// TestStatsReportProgress gives an application something to expose on a health
// endpoint.
func TestStatsReportProgress(t *testing.T) {
	worker := started(t,
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(context.Context, queue.Job, error) {}),
	)

	var group sync.WaitGroup
	group.Add(2)
	worker.Handle("ok", func(context.Context, queue.Job) error {
		defer group.Done()
		return nil
	})
	worker.Handle("bad", func(context.Context, queue.Job) error {
		defer group.Done()
		return errors.New("failed")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "ok"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "bad"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	group.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := worker.Stats()
		if stats.Processed == 1 && stats.Failed == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stats = %+v, want one processed and one failed", worker.Stats())
}

// TestJobLoggerCarriesTheJobName gives a failure somewhere to be traced from.
func TestJobLoggerCarriesTheJobName(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	worker := queue.NewMemory(queue.WithLogger(logger), queue.WithMaxAttempts(1),
		queue.WithFailureHandler(func(context.Context, queue.Job, error) {}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})

	logged := make(chan struct{})
	worker.Handle("traced", func(ctx context.Context, _ queue.Job) error {
		ossein.LoggerFromContext(ctx).Info("working")
		close(logged)
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "traced"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-logged:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never ran")
	}

	recorded := logs.String()
	if !strings.Contains(recorded, `job=traced`) {
		t.Fatalf("log = %q, want the job name attached to the job's logger", recorded)
	}
}

// TestStopIsIdempotent keeps a second Stop, which the lifecycle may perform, from
// failing or panicking.
func TestStopIsIdempotent(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
	worker.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestStopWithoutStartIsSafe covers an application that failed before starting.
func TestStopWithoutStartIsSafe(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestEnqueueBeforeStartIsQueued keeps work submitted during wiring from being lost.
func TestEnqueueBeforeStartIsQueued(t *testing.T) {
	worker := started(t)

	processed := make(chan struct{})
	worker.Handle("early", func(context.Context, queue.Job) error {
		close(processed)
		return nil
	})

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "early"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	worker.Start()

	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("a job enqueued before Start was never processed")
	}
}

// TestEnqueueHonoursACancelledContext keeps a caller's cancellation visible.
func TestEnqueueHonoursACancelledContext(t *testing.T) {
	worker := started(t)
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
	worker.Start()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := worker.Enqueue(ctx, queue.Job{Name: "job"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// logCapture collects log output safely, since workers log from their own goroutines
// while a test reads what has been recorded.
func logCapture() (*slog.Logger, *syncBuffer) {
	buffer := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(content)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
