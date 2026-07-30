package cache

import (
	"errors"
	"testing"
	"time"
)

func TestValidateKey(t *testing.T) {
	if err := validateKey(""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("validateKey(\"\") = %v, want ErrInvalidKey", err)
	}
	for _, key := range []string{"users:42", " spaces are valid "} {
		if err := validateKey(key); err != nil {
			t.Fatalf("validateKey(%q) = %v", key, err)
		}
	}
}

func TestValidateTTL(t *testing.T) {
	if err := validateTTL(-time.Nanosecond); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("validateTTL(-1ns) = %v, want ErrInvalidTTL", err)
	}
	for _, ttl := range []time.Duration{0, time.Nanosecond, time.Hour} {
		if err := validateTTL(ttl); err != nil {
			t.Fatalf("validateTTL(%s) = %v", ttl, err)
		}
	}
}
