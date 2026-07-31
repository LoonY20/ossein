package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestDefaultBackoffDoublesAndCaps covers the whole curve, including the attempts a
// timing-based test cannot reach without waiting minutes.
func TestDefaultBackoffDoublesAndCaps(t *testing.T) {
	cases := map[int]time.Duration{
		1:  100 * time.Millisecond,
		2:  200 * time.Millisecond,
		3:  400 * time.Millisecond,
		9:  25600 * time.Millisecond,
		10: 30 * time.Second,
		20: 30 * time.Second,
		// Far enough that a naive shift would overflow to zero or negative.
		64:  30 * time.Second,
		200: 30 * time.Second,
	}

	for attempt, want := range cases {
		if got := defaultBackoff(attempt); got != want {
			t.Fatalf("defaultBackoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// TestDefaultBackoffNeverReturnsANonPositiveDelay is the property that matters: a
// zero or negative delay would turn a retry into a spin.
func TestDefaultBackoffNeverReturnsANonPositiveDelay(t *testing.T) {
	for attempt := 1; attempt <= 256; attempt++ {
		if delay := defaultBackoff(attempt); delay <= 0 {
			t.Fatalf("defaultBackoff(%d) = %v, want a positive delay", attempt, delay)
		}
	}
}

// TestProcessReportsAJobWithNoHandler covers the guard a durable driver could reach by
// replaying a job whose handler the current build no longer registers. Enqueue rejects
// unknown names, so this cannot happen with the in-process queue alone.
func TestProcessReportsAJobWithNoHandler(t *testing.T) {
	failures := make(chan error, 1)
	worker := NewMemory(
		WithLogger(discardTestLogger()),
		WithFailureHandler(func(_ context.Context, _ Job, err error) {
			failures <- err
		}),
	)

	worker.process(0, map[string]Handler{}, Job{Name: "retired.job"})

	select {
	case err := <-failures:
		if !errors.Is(err, ErrNoHandler) {
			t.Fatalf("error = %v, want ErrNoHandler", err)
		}
	default:
		t.Fatal("the job was not reported as failed")
	}
	if stats := worker.Stats(); stats.Failed != 1 {
		t.Fatalf("failed = %d, want 1", stats.Failed)
	}
}

// discardTestLogger keeps the internal tests quiet.
func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
