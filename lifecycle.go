package ossein

import (
	"context"
	"errors"
	"fmt"
)

// LifecycleHook runs during application startup or shutdown.
type LifecycleHook func(context.Context) error

// OnStart registers startup hooks. Hooks run in registration order.
func (a *App) OnStart(hooks ...LifecycleHook) {
	a.startHooks = append(a.startHooks, hooks...)
}

// OnStop registers shutdown hooks. Hooks run in reverse registration order.
func (a *App) OnStop(hooks ...LifecycleHook) {
	a.stopHooks = append(a.stopHooks, hooks...)
}

// Start validates the service graph, then runs all registered startup hooks.
func (a *App) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if a.services != nil {
		if err := a.services.Validate(); err != nil {
			return fmt.Errorf("ossein: validate services: %w", err)
		}
	}

	if err := a.buildHandler(); err != nil {
		return err
	}

	for i, hook := range a.startHooks {
		if hook == nil {
			continue
		}
		if err := hook(ctx); err != nil {
			return fmt.Errorf("ossein: start hook %d: %w", i+1, err)
		}
	}

	return nil
}

// Stop runs all registered shutdown hooks in reverse order.
// All hooks are attempted and their errors are joined.
func (a *App) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	var hookErrors []error
	for i := len(a.stopHooks) - 1; i >= 0; i-- {
		hook := a.stopHooks[i]
		if hook == nil {
			continue
		}
		if err := hook(ctx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("ossein: stop hook %d: %w", i+1, err))
		}
	}

	return errors.Join(hookErrors...)
}
