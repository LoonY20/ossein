package cache

import (
	"context"
	"errors"
	"time"

	ossein "github.com/LoonY20/ossein"
)

// defaultCleanupInterval is how often the janitor runs when none is configured.
const defaultCleanupInterval = time.Minute

// RegisterOption configures RegisterMemory.
type RegisterOption func(*registerOptions)

type registerOptions struct {
	interval time.Duration
}

// WithCleanupInterval sets how often the janitor reclaims expired entries. A
// non-positive value means the default of one minute.
func WithCleanupInterval(interval time.Duration) RegisterOption {
	return func(o *registerOptions) {
		if interval > 0 {
			o.interval = interval
		}
	}
}

// RegisterMemory makes an in-memory cache resolvable as a Store and reclaims its
// expired entries on a schedule.
//
// The schedule is the point. Reclamation is otherwise driven by traffic — every
// read and write cleans a small sample — so a process that goes quiet after a
// burst holds every expired entry until its next write. With 24-hour idempotency
// keys that is a day of memory nothing will ask for again, and every application
// ends up wiring the same ticker by hand.
//
// The janitor starts with the application and stops during shutdown.
func RegisterMemory(app *ossein.App, memory *Memory, options ...RegisterOption) error {
	if app == nil {
		return errors.New("ossein cache: app cannot be nil")
	}
	if memory == nil {
		return errors.New("ossein cache: cache cannot be nil")
	}

	settings := registerOptions{interval: defaultCleanupInterval}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	if err := ossein.Instance[Store](app, memory); err != nil {
		return err
	}

	// Cancelled by the stop hook rather than derived from the start context,
	// which is only alive for the duration of startup.
	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	app.OnStart(func(context.Context) error {
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(settings.interval)
			defer ticker.Stop()
			for {
				select {
				case <-janitorCtx.Done():
					return
				case <-ticker.C:
					memory.PurgeExpired()
				}
			}
		}()
		return nil
	})

	app.OnStop(func(ctx context.Context) error {
		stopJanitor()
		// Waited for, so the goroutine cannot outlive the application it was
		// registered against — a test that starts and stops several would
		// otherwise accumulate them.
		select {
		case <-stopped:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	return nil
}
