// Package cache defines a small, backend-neutral cache contract and
// dependency-free helpers.
package cache

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrMiss reports that a key is not present or has expired.
	ErrMiss = errors.New("ossein cache: miss")
	// ErrInvalidKey reports an empty cache key.
	ErrInvalidKey = errors.New("ossein cache: key cannot be empty")
	// ErrInvalidTTL reports a negative cache lifetime.
	ErrInvalidTTL = errors.New("ossein cache: TTL cannot be negative")
	// ErrNilStore reports a nil Store passed to a helper.
	ErrNilStore = errors.New("ossein cache: store cannot be nil")
	// ErrNilLoader reports a nil loader passed to a remember helper.
	ErrNilLoader = errors.New("ossein cache: loader cannot be nil")
)

// Store is the minimal contract implemented by cache backends.
//
// Get returns ErrMiss when a key does not exist or has expired. Set treats a
// zero TTL as no expiration and rejects negative values with ErrInvalidTTL.
// Delete is idempotent.
type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

func validateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}
	return nil
}

func validateTTL(ttl time.Duration) error {
	if ttl < 0 {
		return ErrInvalidTTL
	}
	return nil
}
