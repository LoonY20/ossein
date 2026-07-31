package queue_test

import (
	"context"
	"errors"
	"sync"
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
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(5),
		// Long enough that a drain waiting for it would blow the deadline below.
		queue.WithBackoff(func(int) time.Duration { return 30 * time.Second }),
		queue.OnFailure(func(_ context.Context, job queue.Job, _ error) {
			failures <- job
		}),
	)

	failed := make(chan struct{})
	var once sync.Once
	worker.Handle("flaky", func(context.Context, queue.Job) error {
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
