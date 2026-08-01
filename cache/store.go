// Package cache defines a small, backend-neutral cache contract and
// dependency-free helpers.
package cache

import (
	"context"
	"errors"
	"fmt"
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
	// ErrDecode reports a cached JSON value that cannot be decoded.
	ErrDecode = errors.New("ossein cache: decode failed")
	// ErrEncode reports a value that cannot be encoded as JSON.
	ErrEncode = errors.New("ossein cache: encode failed")
	// ErrNotAtomic reports a Store that cannot claim a key as one operation.
	ErrNotAtomic = errors.New("ossein cache: store does not support atomic claims")
)

// Store is the minimal contract implemented by cache backends.
//
// Get returns ErrMiss when a key does not exist or has expired. Set treats a
// zero TTL as no expiration and rejects negative values with ErrInvalidTTL.
// Delete is idempotent. Set must not retain value in a way that lets later
// caller mutations change the stored value. A successful Get returns bytes
// owned by the caller, which may mutate them freely.
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

// Adder is the capability of storing a value only when a key is not already
// taken, as one operation.
//
// It is separate from Store because not every backend can do it. A backend that
// can implements this too, and a helper that needs the guarantee asks for it by
// type assertion rather than assuming.
//
// Add reports whether the value was stored. False means the key already holds a
// live value; an expired one does not count as taken. The key and TTL rules are
// the same as Set's.
type Adder interface {
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// Claim takes a key for the caller, and reports whether it got it.
//
// It is the primitive behind an idempotency key, a run-once job, or a lock with
// a lease: the first caller gets true, everyone else gets false until the TTL
// runs out or the key is deleted.
//
// It requires a store that implements Adder and reports ErrNotAtomic otherwise,
// rather than falling back to Get-then-Set. The fallback is what applications
// write today and it is racy by construction — two callers both miss, both
// store, and both proceed. The window is sub-microsecond against a local map,
// so it rarely shows up in testing, and routine against a network cache, which
// is exactly where duplicate work is expensive. A guarantee that quietly is not
// one is worse than an error.
func Claim(ctx context.Context, store Store, key string, ttl time.Duration) (bool, error) {
	if store == nil {
		return false, ErrNilStore
	}
	adder, ok := store.(Adder)
	if !ok {
		return false, fmt.Errorf("%w: %T", ErrNotAtomic, store)
	}
	return adder.Add(ctx, key, claimValue, ttl)
}

// claimValue is what Claim stores. The value is never read; a single byte keeps
// a claim from costing more than it has to.
var claimValue = []byte{1}
