package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEnqueueHoldsTheLockAcrossTheSend pins the fix for a send on a closed channel,
// deterministically rather than by stress.
//
// A stress test is the obvious way to cover this and a bad one: the window between
// checking the closed flag and sending is a few instructions wide, so a loop that
// happens not to hit it passes whether the lock is there or not. This drives the
// interleaving directly through beforeSend, which runs at exactly that point.
//
// With the read lock held, Stop cannot take the write side, so it is still blocked
// when beforeSend gives up waiting and the send lands on an open channel. Without the
// lock, Stop runs to completion inside beforeSend and the send panics.
func TestEnqueueHoldsTheLockAcrossTheSend(t *testing.T) {
	worker := NewMemory(WithLogger(discardTestLogger()), WithBuffer(4))
	worker.Handle("job", func(context.Context, Job) error { return nil })

	stopped := make(chan struct{})
	worker.beforeSend = func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = worker.Stop(ctx)
			close(stopped)
		}()

		// Give Stop every chance to close the channel underneath us. It cannot,
		// unless the lock this test exists for has been removed.
		select {
		case <-stopped:
		case <-time.After(250 * time.Millisecond):
		}
	}

	// The send must succeed: the queue was open when the flag was checked, and the
	// lock is what keeps that answer true until the job is in.
	if err := worker.Enqueue(context.Background(), Job{Name: "job"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never completed after the submission finished")
	}

	// And once Stop has finished, submissions are refused rather than panicking.
	worker.beforeSend = nil
	if err := worker.Enqueue(context.Background(), Job{Name: "job"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Enqueue after Stop = %v, want ErrClosed", err)
	}
}

// TestBackoffIsNotConsultedAfterTheFinalAttempt covers the guard on the last try. A
// delay there is pure shutdown latency: nothing follows it, and with the default
// backoff it adds up to thirty seconds per doomed job.
func TestBackoffIsNotConsultedAfterTheFinalAttempt(t *testing.T) {
	var delays []int
	worker := NewMemory(
		WithLogger(discardTestLogger()),
		WithWorkers(1),
		WithMaxAttempts(3),
		WithBackoff(func(attempt int) time.Duration {
			delays = append(delays, attempt)
			return 0
		}),
	)

	done := make(chan struct{})
	attempts := 0
	worker.Handle("doomed", func(context.Context, Job) error {
		attempts++
		if attempts == 3 {
			defer close(done)
		}
		return errors.New("nope")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never exhausted its attempts")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Between three attempts there are two gaps, not three.
	if len(delays) != 2 {
		t.Fatalf("the backoff was consulted %v, want the two delays between three attempts", delays)
	}
	for index, attempt := range delays {
		if attempt != index+1 {
			t.Fatalf("delays = %v, want the attempt that just failed each time", delays)
		}
	}
}

// TestDefaultBackoffClampsTheShiftAtBothEnds covers the two ways the shift count can go
// wrong. A count past the width of the value shifts it away entirely, turning the
// backoff into a spin; a negative one panics.
func TestDefaultBackoffClampsTheShiftAtBothEnds(t *testing.T) {
	if delay := defaultBackoff(0); delay != 100*time.Millisecond {
		t.Fatalf("defaultBackoff(0) = %v, want the base delay", delay)
	}
	if delay := defaultBackoff(-5); delay != 100*time.Millisecond {
		t.Fatalf("defaultBackoff(-5) = %v, want the base delay", delay)
	}
	for _, attempt := range []int{17, 64, 200, 1 << 20} {
		if delay := defaultBackoff(attempt); delay != 30*time.Second {
			t.Fatalf("defaultBackoff(%d) = %v, want the cap", attempt, delay)
		}
	}
}
