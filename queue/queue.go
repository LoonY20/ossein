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

	// ErrAbandoned reports that a job's remaining retries were cut short because
	// shutdown began. It reaches the failure handler wrapped around the last error,
	// so errors.Is separates a job that is genuinely dead from one that was still
	// retryable — the difference between a dead-letter record and a redelivery.
	ErrAbandoned = errors.New("ossein queue: retries abandoned at shutdown")
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

	// Attempt is the 1-based try number. A handler can use it to decide when to give
	// up on its own, or to make a retry idempotent.
	//
	// The worker owns it: a value set before submission is overwritten, so a Job value
	// can be resubmitted without carrying a spent retry budget with it.
	Attempt int

	// RequestID is the identity of whatever enqueued the job, filled in by Enqueue
	// from the context when it is empty.
	//
	// It is what connects the two halves of an asynchronous request. A webhook is
	// accepted under one request ID and processed some time later on a worker, and
	// without carrying the ID across, finding both in a log means guessing from
	// timestamps. The worker puts it back into the job's context, so
	// ossein.RequestIDFromContext works inside a handler and every line it logs
	// carries it.
	//
	// It travels with the job, so a durable driver keeps the connection across a
	// restart. Set it explicitly for work that has an origin the context does not
	// know about.
	RequestID string
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
	// Abandoned counts jobs whose remaining retries were cut short by shutdown.
	// They are not counted as failed: they were still retryable.
	Abandoned int64
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

// WithFailureHandler sets the callback for a job that will not be retried again,
// with the last error. It is where a dead-letter record belongs.
//
// It is called both for a job that exhausted its attempts and for one whose retries
// were cut short by shutdown; errors.Is(err, ErrAbandoned) separates the two, and
// only the first is genuinely dead.
//
// It runs on the worker's goroutine, so it should be quick and must not panic; a
// panic in it is recovered and logged.
func WithFailureHandler(handler func(context.Context, Job, error)) Option {
	return func(o *options) {
		if handler != nil {
			o.onFailure = handler
		}
	}
}

// defaultBackoff doubles from 100ms and stops growing at thirty seconds.
//
// The shift count is clamped at both ends: a large attempt would shift the whole
// value out and produce a zero delay — a retry storm rather than a backoff — and a
// negative one would panic.
func defaultBackoff(attempt int) time.Duration {
	const (
		base = 100 * time.Millisecond
		most = 30 * time.Second
	)
	delay := base << min(max(attempt-1, 0), 16)
	if delay > most {
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

	// handlersMu guards handlers. Registration is frozen once the workers start, but
	// Enqueue reads the table from request goroutines while Handle may still be
	// writing it during wiring, and an unsynchronized map read against a write is a
	// fatal error no recover can catch.
	handlersMu sync.RWMutex
	handlers   map[string]Handler
	started    atomic.Bool

	// closeMu guards the transition to closed against a submission in flight.
	// Checking a flag and then sending would race with closing the channel, and the
	// loser panics on a closed channel. It also orders Start's workers.Add against
	// the Wait in Stop.
	closeMu sync.RWMutex
	closed  bool

	stopOnce sync.Once
	// shutdown is closed as soon as Stop begins, before draining, so a worker
	// waiting out a retry backoff can abandon it instead of delaying shutdown.
	shutdown chan struct{}
	finished chan struct{}
	workers  sync.WaitGroup

	// jobCtx is the parent of every handler's context. It is cancelled only when a
	// drain runs out of time, which is what lets a handler distinguish "finish what
	// you are doing" from "the process is going away now".
	jobCtx     context.Context
	cancelJobs context.CancelFunc

	// beforeSend runs between the closed check and the send. It exists so a test can
	// interleave Stop with a submission in flight deterministically; nil otherwise.
	beforeSend func()

	processed atomic.Int64
	failed    atomic.Int64
	abandoned atomic.Int64
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

	jobCtx, cancelJobs := context.WithCancel(context.Background())
	return &Memory{
		settings:   settings,
		jobs:       make(chan Job, settings.buffer),
		handlers:   make(map[string]Handler),
		shutdown:   make(chan struct{}),
		finished:   make(chan struct{}),
		jobCtx:     jobCtx,
		cancelJobs: cancelJobs,
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

	m.handlersMu.Lock()
	defer m.handlersMu.Unlock()
	if _, exists := m.handlers[name]; exists {
		panic("ossein queue: handler for " + name + " is already registered")
	}
	m.handlers[name] = handler
}

// handlerFor returns the handler registered for name.
func (m *Memory) handlerFor(name string) (Handler, bool) {
	m.handlersMu.RLock()
	defer m.handlersMu.RUnlock()
	handler, ok := m.handlers[name]
	return handler, ok
}

// Start launches the worker pool. It is safe to call more than once; only the first
// call has an effect, and a call after Stop has none.
func (m *Memory) Start() {
	// The read lock both excludes a concurrent Stop and orders workers.Add before
	// the Wait that Stop's drain goroutine performs.
	m.closeMu.RLock()
	defer m.closeMu.RUnlock()
	if m.closed {
		return
	}
	if !m.started.CompareAndSwap(false, true) {
		return
	}

	// Snapshot the dispatch table: it is frozen from here on, so workers dispatch
	// without touching the lock.
	m.handlersMu.RLock()
	handlers := maps.Clone(m.handlers)
	m.handlersMu.RUnlock()

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
//
// ctx gates admission only. It is not the context the job runs under — the job
// outlives the request that submitted it — so an already-cancelled ctx is reported
// and nothing else about it is observed.
func (m *Memory) Enqueue(ctx context.Context, job Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Name == "" {
		return fmt.Errorf("%w: %q", ErrNoHandler, job.Name)
	}
	if _, ok := m.handlerFor(job.Name); !ok {
		return fmt.Errorf("%w: %q", ErrNoHandler, job.Name)
	}

	if job.RequestID == "" {
		job.RequestID = ossein.RequestIDFromContext(ctx)
	}

	// Held across the send, so the queue cannot be closed between the check and the
	// submission. Stop takes the write side, so it waits for submissions in flight.
	m.closeMu.RLock()
	defer m.closeMu.RUnlock()
	if m.closed {
		return ErrClosed
	}
	if m.beforeSend != nil {
		m.beforeSend()
	}

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
// A drain that does not complete before ctx expires is reported as an error, and the
// context every job runs under is cancelled: an in-flight handler that honors
// cancellation can return, and no worker starts another job. Work still queued at
// that point is lost, which is what the in-memory driver trades away.
//
// Accepted work that no worker ever ran — because the queue was stopped without
// having started — is reported too, so a shutdown that dropped jobs is never
// mistaken for a clean one.
//
// It is safe to call more than once and without a preceding Start.
func (m *Memory) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() {
		// Signalled before draining, so a worker waiting out a backoff gives up
		// rather than holding shutdown open for the full delay.
		close(m.shutdown)

		m.closeMu.Lock()
		m.closed = true
		close(m.jobs)
		m.closeMu.Unlock()

		// Even with no workers running, waiting is correct: the WaitGroup is empty,
		// so this completes immediately and the queue depth below reports the drop.
		go func() {
			m.workers.Wait()
			close(m.finished)
		}()
	})

	select {
	case <-m.finished:
	case <-ctx.Done():
		// Nothing will drain the rest. Cancelling here is the only signal a handler
		// gets that finishing gracefully is no longer on the table.
		m.cancelJobs()
		return fmt.Errorf("ossein queue: drain did not finish: %w", ctx.Err())
	}

	if dropped := len(m.jobs); dropped > 0 {
		return fmt.Errorf(
			"ossein queue: %d accepted job(s) were dropped because no worker ran", dropped,
		)
	}
	return nil
}

// Stats reports progress.
func (m *Memory) Stats() Stats {
	return Stats{
		Queued:    len(m.jobs),
		Processed: m.processed.Load(),
		Failed:    m.failed.Load(),
		Abandoned: m.abandoned.Load(),
		Refused:   m.refused.Load(),
	}
}

// run is one worker. It consumes jobs until the queue is closed and drained, or until
// a drain that ran out of time cancels the job context.
func (m *Memory) run(worker int, handlers map[string]Handler) {
	defer m.workers.Done()

	for {
		// Checked before receiving rather than alongside it: the job context is only
		// cancelled after the channel is closed, so the receive below always returns,
		// and a plain check keeps "stop starting work" from racing with a ready job.
		if m.jobCtx.Err() != nil {
			return
		}
		job, ok := <-m.jobs
		if !ok {
			return
		}
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
	abandoned := false
	for attempt := 1; attempt <= m.settings.maxAttempts; attempt++ {
		job.Attempt = attempt

		logger := m.settings.logger.With(
			"job", job.Name,
			"attempt", attempt,
			"worker", worker,
		)
		if job.RequestID != "" {
			logger = logger.With("request_id", job.RequestID)
		}

		ctx := ossein.ContextWithRequestID(m.jobCtx, job.RequestID)
		ctx = ossein.ContextWithLogger(ctx, logger)

		lastErr = m.invoke(ctx, handler, job)
		if lastErr == nil {
			m.processed.Add(1)
			return
		}

		logger.Warn("ossein queue: job failed", "error", lastErr)

		// No delay after the last attempt: there is nothing left to wait for, and the
		// wait would be added to the drain.
		if attempt < m.settings.maxAttempts {
			if !m.wait(m.settings.backoff(attempt)) {
				abandoned = true
				break
			}
		}
	}

	if abandoned {
		// Still retryable, so it is not counted as failed — but the application is
		// told, because for the in-memory driver this is its last chance to see it.
		m.abandoned.Add(1)
		m.reportFailure(job, fmt.Errorf("%w: %w", ErrAbandoned, lastErr))
		return
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

	// Deliberately not derived from jobCtx. This is the application's last chance to
	// record the job, and a drain that ran out of time has already cancelled that
	// context — handing over a dead context would deny the dead-letter write the very
	// moment it matters most.
	logger := m.settings.logger.With("job", job.Name, "attempt", job.Attempt)
	if job.RequestID != "" {
		logger = logger.With("request_id", job.RequestID)
	}

	ctx := ossein.ContextWithRequestID(context.Background(), job.RequestID)
	m.settings.onFailure(ossein.ContextWithLogger(ctx, logger), job, err)
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
//
// That ordering holds for a drain that completes. If it exceeds the shutdown timeout,
// Stop reports an error and cancels the job context, and a handler that ignores
// cancellation can still be running when its dependencies close — the reason a job
// should honor the context it is given.
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
