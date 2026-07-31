// Package queue runs deferred work in the background of an Ossein application.
//
// It exists so a handler can acknowledge a request in milliseconds and process it
// afterwards without every application writing the same bounded channel, worker
// pool, retry loop, and drain-on-shutdown.
//
// The in-process driver keeps jobs in memory, so work that has been accepted but not
// yet processed is lost if the process dies. That is the right trade for work that
// can be retried from its source — a webhook the provider will redeliver, a cache
// warm-up — and the wrong one for work that must survive a restart, which needs a
// durable driver.
//
//	work := queue.NewMemory(queue.WithWorkers(4))
//	work.Handle("invoice.paid", func(ctx context.Context, job queue.Job) error {
//		return billing.Settle(ctx, job.Payload)
//	})
//	queue.Register(app, work)
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// Errors reported by Enqueue.
var (
	// ErrFull reports that the queue has no room. It is a signal to shed load, not
	// a reason to block the caller: an HTTP handler should answer 503 rather than
	// wait.
	ErrFull = errors.New("ossein queue: queue is full")

	// ErrClosed reports that the queue has stopped accepting work because shutdown
	// began.
	ErrClosed = errors.New("ossein queue: queue is closed")

	// ErrNoHandler reports a job name with no registered handler. It is returned by
	// Enqueue rather than discovered on a worker, so the mistake surfaces where it
	// was made.
	ErrNoHandler = errors.New("ossein queue: no handler registered for job")
)

// stackBufferBytes bounds the stack trace captured for a panicking handler.
const stackBufferBytes = 64 << 10

// Job is one unit of deferred work.
//
// Payload is opaque bytes so the same job survives a move to a durable driver;
// EnqueueJSON encodes for the common case.
type Job struct {
	// Name selects the handler.
	Name string

	// Payload is the job's data, untouched by the queue.
	Payload []byte

	// Attempt is the 1-based try number, set by the worker. A handler can use it to
	// decide when to give up on its own, or to make a retry idempotent.
	Attempt int
}

// Handler processes one job. Returning an error schedules a retry until the attempt
// limit is reached.
type Handler func(context.Context, Job) error

// Enqueuer submits work. Handlers depend on this rather than on a concrete queue, so
// a durable driver can replace the in-process one.
type Enqueuer interface {
	Enqueue(ctx context.Context, job Job) error
	EnqueueJSON(ctx context.Context, name string, payload any) error
}

// Stats reports queue progress, for a health endpoint or a metric.
type Stats struct {
	// Queued is the number of jobs accepted and not yet started.
	Queued int
	// Processed counts jobs that succeeded.
	Processed int64
	// Failed counts jobs that exhausted their attempts.
	Failed int64
	// Refused counts jobs rejected because the queue was full.
	Refused int64
}

// Option configures a queue.
type Option func(*options)

type options struct {
	workers     int
	buffer      int
	maxAttempts int
	backoff     func(attempt int) time.Duration
	logger      *slog.Logger
	onFailure   func(context.Context, Job, error)
}

// WithWorkers sets how many jobs run concurrently. The default is the number of
// CPUs, capped at eight.
func WithWorkers(workers int) Option {
	return func(o *options) {
		if workers > 0 {
			o.workers = workers
		}
	}
}

// WithBuffer sets how many jobs may wait. Beyond it, Enqueue reports ErrFull rather
// than blocking the caller.
func WithBuffer(buffer int) Option {
	return func(o *options) {
		if buffer > 0 {
			o.buffer = buffer
		}
	}
}

// WithMaxAttempts sets how many times a job is tried before it is reported as
// failed. The default is three.
func WithMaxAttempts(attempts int) Option {
	return func(o *options) {
		if attempts > 0 {
			o.maxAttempts = attempts
		}
	}
}

// WithBackoff sets the delay before a retry, given the attempt that just failed. The
// default doubles from 100ms, capped at thirty seconds.
func WithBackoff(backoff func(attempt int) time.Duration) Option {
	return func(o *options) {
		if backoff != nil {
			o.backoff = backoff
		}
	}
}

// WithLogger sets the logger jobs and failures are reported through. Each job's
// context carries a logger with the job name and attempt attached, reachable with
// ossein.LoggerFromContext.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		if logger != nil {
			o.logger = logger
		}
	}
}

// OnFailure is called once per job that exhausts its attempts, with the last error.
// It is where a dead-letter record belongs.
//
// It runs on the worker's goroutine, so it should be quick and must not panic; a
// panic in it is recovered and logged.
func OnFailure(handler func(context.Context, Job, error)) Option {
	return func(o *options) {
		if handler != nil {
			o.onFailure = handler
		}
	}
}

// defaultBackoff doubles from 100ms and stops growing at thirty seconds.
func defaultBackoff(attempt int) time.Duration {
	const (
		base = 100 * time.Millisecond
		most = 30 * time.Second
	)
	delay := base << min(attempt-1, 16)
	if delay > most || delay <= 0 {
		return most
	}
	return delay
}

// defaultWorkers returns a pool size that is useful without configuration.
func defaultWorkers() int {
	return min(runtime.NumCPU(), 8)
}

// Memory is an in-process queue with a bounded buffer and a worker pool.
type Memory struct {
	settings options
	jobs     chan Job

	// handlers is written only before Start and read by every worker afterwards, so
	// registration is frozen rather than locked on the hot path.
	handlers map[string]Handler
	started  atomic.Bool

	// closeMu guards the transition to closed against a submission in flight.
	// Checking a flag and then sending would race with closing the channel, and the
	// loser panics on a closed channel.
	closeMu sync.RWMutex
	closed  bool

	stopOnce sync.Once
	// shutdown is closed as soon as Stop begins, before draining, so a worker
	// waiting out a retry backoff can abandon it instead of delaying shutdown.
	shutdown chan struct{}
	finished chan struct{}
	workers  sync.WaitGroup

	processed atomic.Int64
	failed    atomic.Int64
	refused   atomic.Int64
}

// NewMemory creates an in-process queue. Register handlers with Handle, then start it
// with Register or Start.
func NewMemory(opts ...Option) *Memory {
	settings := options{
		workers:     defaultWorkers(),
		buffer:      256,
		maxAttempts: 3,
		backoff:     defaultBackoff,
		logger:      slog.Default(),
	}
	for _, option := range opts {
		if option != nil {
			option(&settings)
		}
	}

	return &Memory{
		settings: settings,
		jobs:     make(chan Job, settings.buffer),
		handlers: make(map[string]Handler),
		shutdown: make(chan struct{}),
		finished: make(chan struct{}),
	}
}

// Handle registers the handler for a job name.
//
// It panics on a duplicate name, and after Start: every worker reads the dispatch
// table, so it must be complete before they run. This matches how the application
// freezes routes and middleware.
func (m *Memory) Handle(name string, handler Handler) {
	if m.started.Load() {
		panic("ossein queue: handlers must be registered before the queue starts")
	}
	if name == "" {
		panic("ossein queue: job name cannot be empty")
	}
	if handler == nil {
		panic("ossein queue: handler cannot be nil")
	}
	if _, exists := m.handlers[name]; exists {
		panic("ossein queue: handler for " + name + " is already registered")
	}
	m.handlers[name] = handler
}

// Start launches the worker pool. It is safe to call more than once; only the first
// call has an effect.
func (m *Memory) Start() {
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	// Snapshot the dispatch table so a worker never reads a map another goroutine
	// could still be writing.
	handlers := maps.Clone(m.handlers)

	for worker := 0; worker < m.settings.workers; worker++ {
		m.workers.Add(1)
		go m.run(worker, handlers)
	}
}

// Enqueue submits a job.
//
// It never blocks: a full queue reports ErrFull so the caller can shed load. An
// unknown job name reports ErrNoHandler, and a queue that has begun shutting down
// reports ErrClosed.
func (m *Memory) Enqueue(ctx context.Context, job Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Name == "" {
		return fmt.Errorf("%w: %q", ErrNoHandler, job.Name)
	}
	if _, ok := m.handlers[job.Name]; !ok {
		return fmt.Errorf("%w: %q", ErrNoHandler, job.Name)
	}

	// Held across the send, so the queue cannot be closed between the check and the
	// submission. Stop takes the write side, so it waits for submissions in flight.
	m.closeMu.RLock()
	defer m.closeMu.RUnlock()
	if m.closed {
		return ErrClosed
	}

	job.Attempt = 0
	select {
	case m.jobs <- job:
		return nil
	default:
		m.refused.Add(1)
		return fmt.Errorf("%w: %q", ErrFull, job.Name)
	}
}

// EnqueueJSON encodes payload as JSON and submits it.
func (m *Memory) EnqueueJSON(ctx context.Context, name string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("ossein queue: encode %q: %w", name, err)
	}
	return m.Enqueue(ctx, Job{Name: name, Payload: encoded})
}

// Stop stops accepting work and waits for what has been accepted to finish.
//
// It reports an error if the queue could not drain before ctx expired, so a shutdown
// that dropped work is visible rather than silent. It is safe to call more than once
// and without a preceding Start.
func (m *Memory) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() {
		// Signalled before draining, so a worker waiting out a backoff gives up
		// rather than holding shutdown open for the full delay.
		close(m.shutdown)

		m.closeMu.Lock()
		m.closed = true
		close(m.jobs)
		m.closeMu.Unlock()

		if !m.started.Load() {
			// Nothing is draining it, but the queue is closed to new work.
			close(m.finished)
			return
		}

		go func() {
			m.workers.Wait()
			close(m.finished)
		}()
	})

	select {
	case <-m.finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ossein queue: drain did not finish: %w", ctx.Err())
	}
}

// Stats reports progress.
func (m *Memory) Stats() Stats {
	return Stats{
		Queued:    len(m.jobs),
		Processed: m.processed.Load(),
		Failed:    m.failed.Load(),
		Refused:   m.refused.Load(),
	}
}

// run is one worker. It consumes jobs until the queue is closed and drained.
func (m *Memory) run(worker int, handlers map[string]Handler) {
	defer m.workers.Done()

	for job := range m.jobs {
		m.process(worker, handlers, job)
	}
}

// process runs one job through its attempts.
func (m *Memory) process(worker int, handlers map[string]Handler, job Job) {
	handler, ok := handlers[job.Name]
	if !ok {
		// Enqueue rejects unknown names and the dispatch table is frozen at Start,
		// so the in-process queue cannot reach this. A durable driver replaying a job
		// whose handler this build no longer registers can, so it is reported through
		// the normal failure path rather than dropped.
		job.Attempt = 1
		m.failed.Add(1)
		m.reportFailure(job, fmt.Errorf("%w: %q", ErrNoHandler, job.Name))
		return
	}

	var lastErr error
	for attempt := 1; attempt <= m.settings.maxAttempts; attempt++ {
		job.Attempt = attempt

		logger := m.settings.logger.With(
			"job", job.Name,
			"attempt", attempt,
			"worker", worker,
		)
		ctx := ossein.ContextWithLogger(context.Background(), logger)

		lastErr = m.invoke(ctx, handler, job)
		if lastErr == nil {
			m.processed.Add(1)
			return
		}

		logger.Warn("ossein queue: job failed", "error", lastErr)

		if attempt < m.settings.maxAttempts {
			if !m.wait(m.settings.backoff(attempt)) {
				break
			}
		}
	}

	m.failed.Add(1)
	m.reportFailure(job, lastErr)
}

// invoke runs the handler, turning a panic into an error so one bad job cannot take
// the worker pool down and stop all deferred work.
func (m *Memory) invoke(ctx context.Context, handler Handler, job Job) (err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		stack := make([]byte, stackBufferBytes)
		stack = stack[:runtime.Stack(stack, false)]
		ossein.LoggerFromContext(ctx).Error(
			"ossein queue: job panicked",
			"panic", fmt.Sprint(recovered),
			"stack", string(stack),
		)
		err = fmt.Errorf("ossein queue: job panicked: %v", recovered)
	}()

	return handler(ctx, job)
}

// reportFailure hands an exhausted job to the application, guarding against a panic
// in the callback.
func (m *Memory) reportFailure(job Job, err error) {
	if m.settings.onFailure == nil {
		m.settings.logger.Error(
			"ossein queue: job exhausted its attempts",
			"job", job.Name,
			"attempts", job.Attempt,
			"error", err,
		)
		return
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			m.settings.logger.Error(
				"ossein queue: failure handler panicked",
				"job", job.Name,
				"panic", fmt.Sprint(recovered),
			)
		}
	}()

	logger := m.settings.logger.With("job", job.Name, "attempt", job.Attempt)
	m.settings.onFailure(ossein.ContextWithLogger(context.Background(), logger), job, err)
}

// wait sleeps between attempts, returning false if shutdown began meanwhile so the
// remaining retries are abandoned rather than delaying the drain.
func (m *Memory) wait(delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-m.shutdown:
		return false
	}
}

// Register ties a queue to the application lifecycle and makes it resolvable as an
// Enqueuer, so handlers can depend on the interface rather than the driver.
//
// The queue starts with the application and drains during shutdown. Register it after
// anything its jobs depend on, such as a database: stop hooks run in reverse, so the
// workers then finish before their dependencies close.
func Register(app *ossein.App, work *Memory) error {
	if app == nil {
		return errors.New("ossein queue: app cannot be nil")
	}
	if work == nil {
		return errors.New("ossein queue: queue cannot be nil")
	}

	if err := ossein.Instance[Enqueuer](app, work); err != nil {
		return err
	}

	app.OnStart(func(context.Context) error {
		work.Start()
		return nil
	})
	app.OnStop(work.Stop)
	return nil
}
