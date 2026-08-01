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
//
// A backend that can also store a value only when a key is free should implement
// Adder, which is what Claim and Once need. A backend that cannot is still a
// complete Store; those two helpers report ErrNotAtomic against it rather than
// doing something weaker.
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
// live value; an expired one does not count as taken. Keys and TTLs follow Set's
// rules, including that a zero TTL never expires, and value must not be retained
// in a way that lets a later caller mutation change what is stored.
//
// A decorator around a Store — key prefixing, metrics, a test spy — erases this
// capability unless it forwards Add, and the erasure is silent: the wrapper still
// satisfies Store, and Claim starts reporting ErrNotAtomic at runtime. Forward it.
type Adder interface {
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// Claim takes a key for the caller, and reports whether it got it.
//
// It is the primitive behind an idempotency key, a run-once job, or a lock with
// a lease: the first caller gets true, everyone else gets false until the TTL
// runs out or the key is deleted.
//
// The TTL must be positive. A claim is a lease, and one with no expiry is a
// tombstone that only an explicit Delete can lift — which for an idempotency key
// means a single crash between claiming and working blocks that key forever.
//
// A claim taken before doing the work has to be released if the work does not
// happen, or the caller has traded doing something twice for never doing it at
// all. Once handles that; taking a claim by hand means handling it by hand.
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
	if err := validateKey(key); err != nil {
		return false, err
	}
	if ttl <= 0 {
		return false, fmt.Errorf("%w: a claim needs a positive lease, got %v", ErrInvalidTTL, ttl)
	}

	adder, ok := store.(Adder)
	if !ok {
		return false, fmt.Errorf("%w: %T", ErrNotAtomic, store)
	}

	claimed, err := adder.Add(ctx, key, newClaimValue(), ttl)
	if err != nil {
		return false, fmt.Errorf("ossein cache: claim %q: %w", key, err)
	}
	return claimed, nil
}

// Once runs work at most once per key, and reports whether it ran.
//
// It is Claim with the release that a bare claim leaves to the caller: if work
// returns an error, the claim is dropped so a retry can take it. Without that,
// a claim taken for a unit of work that then failed turns "this might run twice"
// into "this can never run again", which for a webhook receiver means a delivery
// the provider was told to resend is answered as a duplicate and lost.
//
// False with a nil error means another caller holds the key. An error from work
// is returned as it is, after the release.
func Once(
	ctx context.Context,
	store Store,
	key string,
	ttl time.Duration,
	work func(context.Context) error,
) (bool, error) {
	if work == nil {
		return false, ErrNilLoader
	}

	claimed, err := Claim(ctx, store, key, ttl)
	if err != nil || !claimed {
		return false, err
	}

	if err := work(ctx); err != nil {
		// Released with a context that cannot already be cancelled: the work
		// failing because its context ended is the case where releasing matters
		// most, and a cancelled context would deny exactly that.
		if releaseErr := store.Delete(context.WithoutCancel(ctx), key); releaseErr != nil {
			return true, errors.Join(err, fmt.Errorf(
				"ossein cache: release claim %q: %w", key, releaseErr,
			))
		}
		return false, err
	}
	return true, nil
}

// newClaimValue returns the byte a claim stores. The value is never read; it is
// built per call rather than shared, so no driver can retain one caller's slice
// and hand it to another.
func newClaimValue() []byte {
	return []byte{1}
}
