package queue_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LoonY20/ossein/queue"
)

// TestStopWithoutStartReportsDroppedWork covers a shutdown that drops accepted work.
// Enqueue before Start is supported, and a start hook registered earlier can fail, in
// which case the queue is stopped having never run: reporting success there would make
// Stop's error return meaningless for the one case it exists to describe.
func TestStopWithoutStartReportsDroppedWork(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()), queue.WithBuffer(8))
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })

	for i := 0; i < 3; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := worker.Stop(ctx)
	if err == nil {
		t.Fatal("Stop reported a clean shutdown after dropping three accepted jobs")
	}
	if !strings.Contains(err.Error(), "3 accepted job") {
		t.Fatalf("error = %v, want it to name the three dropped jobs", err)
	}
}

// TestStopAfterAnIncompleteDrainStillWaits covers the second call. The first Stop
// reports a timeout; a later one must not answer instantly with success while a worker
// is still executing, which is what happens if the "finished" signal is closed eagerly
// rather than by the drain itself.
func TestStopAfterAnIncompleteDrainStillWaits(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Bool
	var finished atomic.Bool

	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
	)
	worker.Handle("slow", func(context.Context, queue.Job) error {
		running.Store(true)
		<-release
		finished.Store(true)
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "slow"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitFor(t, running.Load, "the job never started")

	tight, cancelTight := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelTight()
	if err := worker.Stop(tight); err == nil {
		t.Fatal("the first Stop reported success while a job was still running")
	}

	// The second Stop must observe the same in-flight job.
	second := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		second <- worker.Stop(ctx)
	}()

	select {
	case err := <-second:
		t.Fatalf("the second Stop returned %v while the handler was still running", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("the second Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Stop never returned")
	}
	if !finished.Load() {
		t.Fatal("the handler did not run to completion")
	}
}

// TestJobContextIsCancelledWhenTheDrainRunsOutOfTime is the only signal a handler gets
// that finishing gracefully is no longer on the table. Without it, a job holds a
// context that is never done, and keeps using dependencies the stop hooks are closing.
func TestJobContextIsCancelledWhenTheDrainRunsOutOfTime(t *testing.T) {
	observed := make(chan error, 1)

	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
	)
	started := make(chan struct{})
	worker.Handle("blocking", func(ctx context.Context, _ queue.Job) error {
		close(started)
		<-ctx.Done()
		observed <- ctx.Err()
		return ctx.Err()
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "blocking"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := worker.Stop(ctx); err == nil {
		t.Fatal("Stop reported success while the handler was blocked")
	}

	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the handler saw %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler's context was never cancelled, so the job cannot be stopped")
	}
}

// TestJobContextSurvivesAGracefulDrain is the other half of the contract. A drain that
// completes must not cancel anything: a job that checks its context would otherwise
// abort halfway through the very shutdown that is waiting for it to finish.
func TestJobContextSurvivesAGracefulDrain(t *testing.T) {
	seen := make(chan error, 4)

	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
	)
	worker.Handle("slow", func(ctx context.Context, _ queue.Job) error {
		time.Sleep(30 * time.Millisecond)
		seen <- ctx.Err()
		return nil
	})
	worker.Start()

	for i := 0; i < 3; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "slow"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	close(seen)
	count := 0
	for err := range seen {
		count++
		if err != nil {
			t.Fatalf("a job drained during shutdown saw ctx.Err() = %v, want nil", err)
		}
	}
	if count != 3 {
		t.Fatalf("%d jobs ran, want 3", count)
	}
}

// TestNoJobStartsAfterTheDrainRunsOutOfTime covers the queued remainder. Once Stop has
// given up, a worker releasing its current job must not pick up the next one: the stop
// hooks below it are already closing the dependencies that job would use.
func TestNoJobStartsAfterTheDrainRunsOutOfTime(t *testing.T) {
	var started atomic.Int64
	release := make(chan struct{})

	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithBuffer(8),
		queue.WithMaxAttempts(1),
	)
	worker.Handle("job", func(context.Context, queue.Job) error {
		if started.Add(1) == 1 {
			<-release
		}
		return nil
	})
	worker.Start()

	for i := 0; i < 4; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	waitFor(t, func() bool { return started.Load() == 1 }, "the first job never started")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := worker.Stop(ctx); err == nil {
		t.Fatal("Stop reported success while a job was blocked")
	}

	close(release)
	time.Sleep(200 * time.Millisecond)

	if ran := started.Load(); ran != 1 {
		t.Fatalf("%d jobs started, want 1: a worker took new work after the drain gave up", ran)
	}
}

// TestStartAfterStopDoesNothing covers a lifecycle that runs out of order — a start
// hook that fires after shutdown began, or a Start call in application code. Spawning
// workers then would add to a WaitGroup another goroutine is already waiting on, and
// they would run jobs the shutdown has already reported as dropped.
func TestStartAfterStopDoesNothing(t *testing.T) {
	var ran atomic.Int64
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(2),
		queue.WithBuffer(4),
	)
	worker.Handle("job", func(context.Context, queue.Job) error {
		ran.Add(1)
		return nil
	})

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err == nil {
		t.Fatal("expected the undrained job to be reported")
	}

	worker.Start()
	time.Sleep(100 * time.Millisecond)

	if got := ran.Load(); got != 0 {
		t.Fatalf("%d jobs ran after Stop, want 0", got)
	}

	// And the queue is still stopped, not resurrected.
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("Enqueue = %v, want ErrClosed", err)
	}
}

// TestConcurrentHandleAndEnqueueIsSafe covers wiring: Handle writes the dispatch table
// while Enqueue reads it, and both are legal before Start. An unsynchronized map read
// against a write is a fatal error, which no recover and no middleware can contain, so
// this test crashes the binary rather than failing if the synchronization is removed.
func TestConcurrentHandleAndEnqueueIsSafe(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()), queue.WithBuffer(512))
	worker.Handle("known", func(context.Context, queue.Job) error { return nil })

	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 2000; i++ {
			worker.Handle("job-"+strconv.Itoa(i),
				func(context.Context, queue.Job) error { return nil })
		}
	}()

	group.Add(1)
	go func() {
		defer group.Done()
		for i := 0; i < 2000; i++ {
			// The result is irrelevant; the lookup itself is what races.
			_ = worker.Enqueue(context.Background(), queue.Job{Name: "known"})
		}
	}()

	group.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Nothing drained the queue, so the drop is expected and reported.
	if err := worker.Stop(ctx); err == nil {
		t.Fatal("expected the undrained jobs to be reported")
	}
}

// waitFor polls a condition, so a test does not depend on a fixed sleep.
func waitFor(t *testing.T, done func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}
