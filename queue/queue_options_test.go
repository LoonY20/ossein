package queue_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/queue"
)

// TestDefaultBackoffGrowsAndCaps pins the delay a service relies on when it does not
// configure one: growing enough to let a dependency recover, bounded so a retry is
// never postponed indefinitely.
func TestDefaultBackoffGrowsAndCaps(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(4),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})

	delays := make(chan time.Duration, 8)
	var last time.Time
	var mu sync.Mutex

	worker.Handle("flaky", func(context.Context, queue.Job) error {
		mu.Lock()
		now := time.Now()
		if !last.IsZero() {
			delays <- now.Sub(last)
		}
		last = now
		mu.Unlock()
		return errors.New("temporary failure")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "flaky"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Three gaps between four attempts, each at least as long as the last: the
	// default doubles from 100ms.
	var previous time.Duration
	for gap := 0; gap < 3; gap++ {
		select {
		case delay := <-delays:
			if delay < previous {
				t.Fatalf("gap %d = %v, shorter than the previous %v", gap, delay, previous)
			}
			if delay > 5*time.Second {
				t.Fatalf("gap %d = %v, longer than the default should ever reach here", gap, delay)
			}
			previous = delay
		case <-time.After(10 * time.Second):
			t.Fatalf("gap %d never happened", gap)
		}
	}
}

// TestZeroBackoffRetriesImmediately covers a configuration that opts out of waiting.
func TestZeroBackoffRetriesImmediately(t *testing.T) {
	worker := started(t,
		queue.WithWorkers(1),
		queue.WithMaxAttempts(3),
		queue.WithBackoff(func(int) time.Duration { return 0 }),
	)

	attempts := make(chan int, 4)
	worker.Handle("flaky", func(_ context.Context, job queue.Job) error {
		attempts <- job.Attempt
		return errors.New("temporary failure")
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
}

func TestHandleRejectsInvalidRegistrations(t *testing.T) {
	cases := map[string]func(*queue.Memory){
		"empty name":  func(w *queue.Memory) { w.Handle("", func(context.Context, queue.Job) error { return nil }) },
		"nil handler": func(w *queue.Memory) { w.Handle("job", nil) },
	}

	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			worker := queue.NewMemory(queue.WithLogger(discardLogger()))
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("expected a panic")
				}
			}()
			register(worker)
		})
	}
}

func TestEnqueueRejectsAnEmptyName(t *testing.T) {
	worker := started(t)
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{}); !errors.Is(err, queue.ErrNoHandler) {
		t.Fatalf("error = %v, want ErrNoHandler", err)
	}
}

// TestEnqueueJSONReportsAnUnencodablePayload keeps the failure at the caller rather
// than on a worker.
func TestEnqueueJSONReportsAnUnencodablePayload(t *testing.T) {
	worker := started(t)
	worker.Handle("job", func(context.Context, queue.Job) error { return nil })
	worker.Start()

	err := worker.EnqueueJSON(context.Background(), "job", math.Inf(1))
	if err == nil {
		t.Fatal("expected an encoding error")
	}
	if !strings.Contains(err.Error(), "encode") {
		t.Fatalf("error = %v, want it to name encoding", err)
	}
}

// TestStartIsIdempotent keeps a second Start, which Register plus a manual call would
// produce, from doubling the pool.
func TestStartIsIdempotent(t *testing.T) {
	worker := started(t, queue.WithWorkers(1))

	var concurrent, peak int64
	var mu sync.Mutex
	release := make(chan struct{})
	defer close(release)

	worker.Handle("job", func(context.Context, queue.Job) error {
		mu.Lock()
		concurrent++
		if concurrent > peak {
			peak = concurrent
		}
		mu.Unlock()
		<-release
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	})

	worker.Start()
	worker.Start()

	for i := 0; i < 4; i++ {
		if err := worker.Enqueue(context.Background(), queue.Job{Name: "job"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	observed := peak
	mu.Unlock()

	if observed > 1 {
		t.Fatalf("peak concurrency = %d with one worker; Start ran twice", observed)
	}
}

// TestFailureHandlerPanicDoesNotStopTheWorker keeps a broken dead-letter callback from
// taking the pool down with it.
func TestFailureHandlerPanicDoesNotStopTheWorker(t *testing.T) {
	worker := queue.NewMemory(
		queue.WithLogger(discardLogger()),
		queue.WithWorkers(1),
		queue.WithMaxAttempts(1),
		queue.OnFailure(func(context.Context, queue.Job, error) {
			panic("the failure handler is broken")
		}),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})

	survived := make(chan struct{})
	worker.Handle("doomed", func(context.Context, queue.Job) error {
		return errors.New("failed")
	})
	worker.Handle("after", func(context.Context, queue.Job) error {
		close(survived)
		return nil
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := worker.Enqueue(context.Background(), queue.Job{Name: "after"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case <-survived:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker stopped after the failure handler panicked")
	}
}

// TestFailureIsLoggedWithoutAHandler keeps an exhausted job from disappearing when no
// dead-letter callback is configured.
func TestFailureIsLoggedWithoutAHandler(t *testing.T) {
	logger, logs := logCapture()

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

	worker.Handle("doomed", func(context.Context, queue.Job) error {
		return errors.New("permanent failure")
	})
	worker.Start()

	if err := worker.Enqueue(context.Background(), queue.Job{Name: "doomed"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "exhausted") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log = %q, want the exhausted job recorded", logs.String())
}

func TestRegisterRejectsNilArguments(t *testing.T) {
	worker := queue.NewMemory(queue.WithLogger(discardLogger()))

	if err := queue.Register(nil, worker); err == nil {
		t.Fatal("expected an error for a nil app")
	}
	if err := queue.Register(ossein.New(), nil); err == nil {
		t.Fatal("expected an error for a nil queue")
	}
}

// TestRegisterReportsADuplicateRegistration keeps a second queue from silently
// replacing the first in the container.
func TestRegisterReportsADuplicateRegistration(t *testing.T) {
	app := ossein.New()
	first := queue.NewMemory(queue.WithLogger(discardLogger()))
	second := queue.NewMemory(queue.WithLogger(discardLogger()))

	if err := queue.Register(app, first); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := queue.Register(app, second); err == nil {
		t.Fatal("expected an error for a duplicate registration")
	}
}
