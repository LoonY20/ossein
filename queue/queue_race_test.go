package queue_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LoonY20/ossein/queue"
)

// TestConcurrentEnqueueAndStopDoesNotPanic covers the window between deciding the
// queue is open and submitting to it. Closing the channel in between would make the
// submission panic on a closed channel, taking down whichever request goroutine
// happened to be enqueuing during shutdown.
func TestConcurrentEnqueueAndStopDoesNotPanic(t *testing.T) {
	for round := 0; round < 20; round++ {
		worker := queue.NewMemory(
			queue.WithLogger(discardLogger()),
			queue.WithWorkers(2),
			queue.WithBuffer(8),
		)
		worker.Handle("job", func(context.Context, queue.Job) error { return nil })
		worker.Start()

		var group sync.WaitGroup
		for submitter := 0; submitter < 8; submitter++ {
			group.Add(1)
			go func() {
				defer group.Done()
				for i := 0; i < 50; i++ {
					err := worker.Enqueue(context.Background(), queue.Job{Name: "job"})
					// Refusal is expected once shutdown begins or the buffer fills;
					// a panic is not, and neither is any other error.
					if err != nil && !errors.Is(err, queue.ErrClosed) &&
						!errors.Is(err, queue.ErrFull) {
						t.Errorf("Enqueue: %v", err)
						return
					}
				}
			}()
		}

		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := worker.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()

		group.Wait()
	}
}

// TestStopAbandonsPendingRetries keeps a backoff from holding shutdown open. A job
// that just failed would otherwise sleep for its full delay while the drain waits,
// which for the default backoff reaches thirty seconds.
func TestStopAbandonsPendingRetries(t *testing.T) {
	failures := make(chan queue.Job, 1)
	reported := make(chan error, 1)
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(5),
		// Long enough that a drain waiting for it would blow the deadline below.
		queue.WithBackoff(func(int) time.Duration { return 30 * time.Second }),
		queue.WithFailureHandler(func(_ context.Context, job queue.Job, err error) {
			reported <- err
			failures <- job
		}),
	)

	failed := make(chan struct{})
	var once sync.Once
	var attempts atomic.Int64
	worker.Handle("flaky", func(context.Context, queue.Job) error {
		attempts.Add(1)
		once.Do(func() { close(failed) })
		return errors.New("temporary failure")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "flaky"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait until the first attempt has failed and the worker is in its backoff.
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never ran")
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v; it waited for the retry backoff", elapsed)
	}

	select {
	case <-failures:
	case <-time.After(2 * time.Second):
		t.Fatal("the abandoned job was not reported as failed")
	}

	// The report must say which kind of ending this was, and still carry the error
	// that caused the retry, or a dead-letter sink cannot tell live work from dead.
	err := <-reported
	if !errors.Is(err, queue.ErrAbandoned) {
		t.Fatalf("error = %v, want it to wrap ErrAbandoned", err)
	}
	if !strings.Contains(err.Error(), "temporary failure") {
		t.Fatalf("error = %v, want the handler's own error preserved", err)
	}

	// The point is that the remaining attempts are abandoned, not merely that their
	// delay is skipped: skipping the wait and then running attempts 2 through 5
	// back-to-back would also be fast, and would also report a failure.
	if ran := attempts.Load(); ran != 1 {
		t.Fatalf("the handler ran %d times, want 1: the retries were not abandoned", ran)
	}

	// Abandoned is not failed. The job was still retryable, and a dead-letter table
	// that cannot tell the difference records live work as dead on every deploy.
	stats := worker.Stats()
	if stats.Abandoned != 1 {
		t.Fatalf("stats.Abandoned = %d, want 1", stats.Abandoned)
	}
	if stats.Failed != 0 {
		t.Fatalf("stats.Failed = %d, want 0 for an abandoned job", stats.Failed)
	}
}

// TestStatsAreSafeUnderConcurrency covers reading progress from a health endpoint
// while workers are running.
func TestStatsAreSafeUnderConcurrency(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(4),
		queue.WithBuffer(64),
	)
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
	worker.Start()

	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 200; i++ {
			_ = worker.Enqueue(context.Background(), queue.Job{Name: "job"})
		}
	}()

	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 200; i++ {
			_ = worker.Stats()
		}
	}()

	group.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
