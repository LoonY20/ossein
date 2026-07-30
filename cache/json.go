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
		return zero, fmt.Errorf("ossein cache: decode %q: %w", key, err)
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
		return fmt.Errorf("ossein cache: encode %q: %w", key, err)
	}
	if err := store.Set(ctx, key, data, ttl); err != nil {
		return fmt.Errorf("ossein cache: set %q: %w", key, err)
	}
	return nil
}

// Remember returns a cached value or loads and stores it on a miss.
//
// Remember does not suppress concurrent loader calls. Applications that need
// stampede protection should coordinate loaders at their own boundary.
func Remember(
	ctx context.Context,
	store Store,
	key string,
	ttl time.Duration,
	load func(context.Context) ([]byte, error),
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
	value, err := store.Get(ctx, key)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, ErrMiss) {
		return nil, err
	}
	value, err = load(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.Set(ctx, key, value, ttl); err != nil {
		return nil, fmt.Errorf("ossein cache: set %q: %w", key, err)
	}
	return value, nil
}

// RememberJSON returns a decoded cache entry or loads, encodes, and stores it
// on a miss.
func RememberJSON[T any](
	ctx context.Context,
	store Store,
	key string,
	ttl time.Duration,
	load func(context.Context) (T, error),
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
	value, err := GetJSON[T](ctx, store, key)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, ErrMiss) {
		return zero, err
	}
	value, err = load(ctx)
	if err != nil {
		return zero, err
	}
	if err := SetJSON(ctx, store, key, value, ttl); err != nil {
		return zero, err
	}
	return value, nil
}
