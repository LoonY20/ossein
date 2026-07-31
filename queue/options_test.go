package queue_test

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LoonY20/ossein/queue"
)

// TestOptionsRejectUnusableValues covers the guards on every option. They are not
// cosmetic: WithWorkers(0) would produce a queue that accepts work and never runs it,
// WithBuffer(0) an unbuffered channel that refuses everything Enqueue submits, and a nil
// backoff or logger a panic on the first retry.
func TestOptionsRejectUnusableValues(t *testing.T) {
	reported := make(chan error, 1)
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithFailureHandler(func(_ context.Context, _ queue.Job, err error) {
			reported <- err
		}),
		queue.WithWorkers(0),
		queue.WithWorkers(-4),
		queue.WithBuffer(0),
		queue.WithBuffer(-1),
		queue.WithMaxAttempts(0),
		queue.WithMaxAttempts(-2),
		queue.WithBackoff(nil),
		// A later nil never clears a value that was set: an option that rejects an
		// unusable argument has to reject it, not apply it.
		queue.WithLogger(nil),
		queue.WithFailureHandler(nil),
		nil, // a nil option, e.g. from a conditional helper
	)

	var attempts atomic.Int64
	done := make(chan struct{})
	worker.Handle("job", func(context.Context, queue.Job) error {
		if attempts.Add(1) == 3 {
			close(done)
		}
		return errors.New("nope")
	})
	worker.Start()

	// A worker exists to run it, a buffer exists to hold it, and the default three
	// attempts still apply — every rejected value fell back to its default.
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the job ran %d times; the defaults did not survive the rejected options",
			attempts.Load())
	}

	select {
	case err := <-reported:
		if err == nil {
			t.Fatal("the failure handler was called with no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failure handler set before the nil one was never called")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestDefaultWorkerCountRunsJobsConcurrently pins the documented default pool size on a
// machine with more than one CPU: a default of one would serialize every job.
func TestDefaultWorkerCountRunsJobsConcurrently(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("the default pool size is one on a single-CPU machine")
	}

	worker := queue.NewMemory(queue.WithLogger(discardLogger()))

	both := make(chan struct{})
	var running atomic.Int64
	worker.Handle("job", func(context.Context, queue.Job) error {
		if running.Add(1) == 2 {
			close(both)
		}
		<-both
		return nil
	})
	worker.Start()

	for i := 0; i < 2; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	select {
	case <-both:
	case <-time.After(2 * time.Second):
		t.Fatal("two jobs never ran at the same time under the default pool size")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestDefaultAttemptLimitIsThree pins the documented default, which decides how long a
// failing job occupies a worker.
func TestDefaultAttemptLimitIsThree(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithBackoff(func(int) time.Duration { return 0 }),
	)

	var attempts atomic.Int64
	failed := make(chan struct{})
	worker.Handle("job", func(context.Context, queue.Job) error {
		attempts.Add(1)
		return errors.New("nope")
	})
	worker.Handle("marker", func(context.Context, queue.Job) error {
		close(failed)
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// One worker, so the marker cannot run until the failing job is done with.
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "marker"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("the failing job never finished")
	}

	if got := attempts.Load(); got != 3 {
		t.Fatalf("the job was tried %d times, want the default of 3", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestStatsReportRefusedAndQueued covers the two counters a health endpoint uses to see
// back-pressure coming. Both were previously reported by nothing.
func TestStatsReportRefusedAndQueued(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithBuffer(2),
	)
	// Not started, so nothing consumes the buffer.
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })

	for i := 0; i < 2; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	if stats := worker.Stats(); stats.Queued != 2 {
		t.Fatalf("stats.Queued = %d, want 2 waiting jobs", stats.Queued)
	}

	for i := 0; i < 3; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); !errors.Is(err, queue.ErrFull) {
			t.Fatalf("Enqueue past the buffer = %v, want ErrFull", err)
		}
	}

	stats := worker.Stats()
	if stats.Refused != 3 {
		t.Fatalf("stats.Refused = %d, want 3", stats.Refused)
	}
	if stats.Queued != 2 {
		t.Fatalf("stats.Queued = %d, want the buffer to still hold 2", stats.Queued)
	}
	if stats.Processed != 0 {
		t.Fatalf("stats.Processed = %d, want 0 with no workers running", stats.Processed)
	}
}

// TestACallerSuppliedAttemptIsOverwritten keeps the retry budget under the queue's
// control. A job submitted with Attempt already set would otherwise start life partway
// through its budget, and a replayed Job value would get fewer tries each time.
func TestACallerSuppliedAttemptIsOverwritten(t *testing.T) {
	seen := make(chan int, 4)
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
	)
	worker.Handle("job", func(_ context.Context, job queue.Job) error {
		seen <- job.Attempt
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job", Attempt: 99}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case attempt := <-seen:
		if attempt != 1 {
			t.Fatalf("the handler saw Attempt = %d, want 1", attempt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the job never ran")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestAbandonedRetriesAreDistinguishableFromExhaustedOnes is what makes the failure
// handler usable as a dead-letter sink. Both cases arrive through it, and only one of
// them means the job is actually dead.
func TestAbandonedRetriesAreDistinguishableFromExhaustedOnes(t *testing.T) {
	exhausted := make(chan error, 1)
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(2),
		queue.WithBackoff(func(int) time.Duration { return 0 }),
		queue.WithFailureHandler(func(_ context.Context, _ queue.Job, err error) {
			exhausted <- err
		}),
	)
	worker.Handle("job", func(context.Context, queue.Job) error {
		return errors.New("permanent")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case err := <-exhausted:
		if errors.Is(err, queue.ErrAbandoned) {
			t.Fatalf("a job that used up its attempts was reported as abandoned: %v", err)
		}
		if err == nil || err.Error() != "permanent" {
			t.Fatalf("error = %v, want the handler's own error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exhausted job was never reported")
	}

	if stats := worker.Stats(); stats.Failed != 1 || stats.Abandoned != 0 {
		t.Fatalf("stats = %+v, want one failure and no abandonment", stats)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
