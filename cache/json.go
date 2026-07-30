package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetJSON loads and decodes a JSON cache entry.
func GetJSON[T any](ctx context.Context, store Store, key string) (T, error) {
	var zero T
	if store == nil {
		return zero, ErrNilStore
	}
	if err := validateKey(key); err != nil {
		return zero, err
	}
	data, err := store.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return zero, &decodeError{key: key, err: err}
	}
	return value, nil
}

// SetJSON encodes value as JSON and stores it.
func SetJSON(
	ctx context.Context,
	store Store,
	key string,
	value any,
	ttl time.Duration,
) error {
	if store == nil {
		return ErrNilStore
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateTTL(ttl); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return &encodeError{key: key, err: err}
	}
	if err := store.Set(ctx, key, data, ttl); err != nil {
		return fmt.Errorf("ossein cache: set %q: %w", key, err)
	}
	return nil
}

// RememberOption configures the cache-aside remember helpers.
type RememberOption func(*rememberOptions)

type rememberOptions struct {
	onError func(context.Context, error)
}

type decodeError struct {
	key string
	err error
}

type encodeError struct {
	key string
	err error
}

func (err *decodeError) Error() string {
	return fmt.Sprintf("ossein cache: decode %q: %v", err.key, err.err)
}

func (err *decodeError) Unwrap() error {
	return err.err
}

func (*decodeError) Is(target error) bool {
	return target == ErrDecode
}

func (err *encodeError) Error() string {
	return fmt.Sprintf("ossein cache: encode %q: %v", err.key, err.err)
}

func (err *encodeError) Unwrap() error {
	return err.err
}

func (*encodeError) Is(target error) bool {
	return target == ErrEncode
}

// WithErrorHandler observes recoverable cache read, decode, and write errors.
// Remember helpers still return successfully loaded values after reporting
// these errors.
func WithErrorHandler(handler func(context.Context, error)) RememberOption {
	return func(options *rememberOptions) {
		options.onError = handler
	}
}

// Remember returns a cached value or loads and stores it when the cache misses
// or is temporarily unavailable.
//
// Remember does not suppress concurrent loader calls. Applications that need
// stampede protection should coordinate loaders at their own boundary. Cache
// errors are fail-open after a successful load; context cancellation and
// loader errors are always returned.
func Remember(
	ctx context.Context,
	store Store,
	key string,
	ttl time.Duration,
	load func(context.Context) ([]byte, error),
	options ...RememberOption,
) ([]byte, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if load == nil {
		return nil, ErrNilLoader
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if err := validateTTL(ttl); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := resolveRememberOptions(options)
	value, err := store.Get(ctx, key)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, ErrMiss) {
		config.report(ctx, fmt.Errorf("ossein cache: get %q: %w", key, err))
	}
	value, err = load(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	if err := store.Set(ctx, key, value, ttl); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		config.report(ctx, fmt.Errorf("ossein cache: set %q: %w", key, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}

// RememberJSON returns a decoded cache entry or loads, encodes, and stores it
// when the cache misses, contains incompatible JSON, or is unavailable.
func RememberJSON[T any](
	ctx context.Context,
	store Store,
	key string,
	ttl time.Duration,
	load func(context.Context) (T, error),
	options ...RememberOption,
) (T, error) {
	var zero T
	if store == nil {
		return zero, ErrNilStore
	}
	if load == nil {
		return zero, ErrNilLoader
	}
	if err := validateKey(key); err != nil {
		return zero, err
	}
	if err := validateTTL(ttl); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	config := resolveRememberOptions(options)
	value, err := GetJSON[T](ctx, store, key)
	if contextErr := ctx.Err(); contextErr != nil {
		return zero, contextErr
	}
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, ErrMiss) {
		config.report(ctx, err)
	}
	value, err = load(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return zero, contextErr
	}
	if err != nil {
		return zero, err
	}
	if err := SetJSON(ctx, store, key, value, ttl); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return zero, contextErr
		}
		if errors.Is(err, ErrEncode) {
			return zero, err
		}
		config.report(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return value, nil
}

func resolveRememberOptions(options []RememberOption) rememberOptions {
	var config rememberOptions
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return config
}

func (options rememberOptions) report(ctx context.Context, err error) {
	if options.onError != nil {
		options.onError(ctx, err)
	}
}
