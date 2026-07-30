// Package factory builds deterministic test data without requiring an ORM.
package factory

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// Builder creates one value for a sequence number.
type Builder[T any] func(sequence uint64) T

// State customizes a value after its defaults are built.
type State[T any] func(*T)

// Persister stores a built value.
type Persister[T any] func(context.Context, T) error

// Factory builds values from a concurrency-safe sequence.
type Factory[T any] struct {
	next    atomic.Uint64
	builder Builder[T]
}

// New creates a factory whose first sequence number is 1.
//
// It panics when builder is nil because a factory without a definition is a
// programming error.
func New[T any](builder Builder[T]) *Factory[T] {
	return NewSequence(1, builder)
}

// NewSequence creates a factory with an explicit first sequence number.
func NewSequence[T any](start uint64, builder Builder[T]) *Factory[T] {
	if builder == nil {
		panic("ossein factory: builder cannot be nil")
	}
	factory := &Factory[T]{builder: builder}
	factory.next.Store(start)
	return factory
}

// Build creates one value and applies states in order.
func (f *Factory[T]) Build(states ...State[T]) T {
	sequence := f.next.Add(1) - 1
	value := f.builder(sequence)
	for _, state := range states {
		if state != nil {
			state(&value)
		}
	}
	return value
}

// BuildN creates count values.
func (f *Factory[T]) BuildN(count int, states ...State[T]) ([]T, error) {
	if count < 0 {
		return nil, errors.New("ossein factory: count cannot be negative")
	}
	values := make([]T, 0, count)
	for range count {
		values = append(values, f.Build(states...))
	}
	return values, nil
}

// Create builds and persists one value.
func (f *Factory[T]) Create(
	ctx context.Context,
	persist Persister[T],
	states ...State[T],
) (T, error) {
	var zero T
	if persist == nil {
		return zero, errors.New("ossein factory: persister cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	value := f.Build(states...)
	if err := persist(ctx, value); err != nil {
		return value, fmt.Errorf("ossein factory: persist sequence value: %w", err)
	}
	return value, nil
}

// CreateN builds and persists count values, stopping at the first error.
//
// The returned slice contains only values persisted successfully.
func (f *Factory[T]) CreateN(
	ctx context.Context,
	count int,
	persist Persister[T],
	states ...State[T],
) ([]T, error) {
	if count < 0 {
		return nil, errors.New("ossein factory: count cannot be negative")
	}
	if persist == nil {
		return nil, errors.New("ossein factory: persister cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	values := make([]T, 0, count)
	for range count {
		value, err := f.Create(ctx, persist, states...)
		if err != nil {
			return values, err
		}
		values = append(values, value)
	}
	return values, nil
}
