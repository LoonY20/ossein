package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cachedUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type stubStore struct {
	value    []byte
	getErr   error
	setErr   error
	setCalls int
}

func (s *stubStore) Get(context.Context, string) ([]byte, error) {
	return s.value, s.getErr
}

func (s *stubStore) Set(context.Context, string, []byte, time.Duration) error {
	s.setCalls++
	return s.setErr
}

func (*stubStore) Delete(context.Context, string) error {
	return nil
}

func TestJSONRoundTrip(t *testing.T) {
	store := NewMemory()
	user := cachedUser{ID: 42, Name: "Erik"}
	if err := SetJSON(context.Background(), store, "users:42", user, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := GetJSON[cachedUser](context.Background(), store, "users:42")
	if err != nil {
		t.Fatal(err)
	}
	if got != user {
		t.Fatalf("GetJSON() = %#v", got)
	}
}

func TestRemember(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	loads := 0
	load := func(context.Context) ([]byte, error) {
		loads++
		return []byte("loaded"), nil
	}

	first, err := Remember(ctx, store, "key", time.Minute, load)
	if err != nil || string(first) != "loaded" {
		t.Fatalf("first Remember() = %q, %v", first, err)
	}
	second, err := Remember(ctx, store, "key", time.Minute, load)
	if err != nil || string(second) != "loaded" {
		t.Fatalf("second Remember() = %q, %v", second, err)
	}
	if loads != 1 {
		t.Fatalf("loader calls = %d, want 1", loads)
	}
}

func TestRememberJSON(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	loads := 0
	load := func(context.Context) (cachedUser, error) {
		loads++
		return cachedUser{ID: 7, Name: "Seven"}, nil
	}

	first, err := RememberJSON(ctx, store, "users:7", time.Minute, load)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RememberJSON(ctx, store, "users:7", time.Minute, load)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || loads != 1 {
		t.Fatalf("RememberJSON() = %#v, %#v, loads %d", first, second, loads)
	}
}

func TestHelperErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := GetJSON[cachedUser](ctx, nil, "key"); !errors.Is(err, ErrNilStore) {
		t.Fatalf("GetJSON(nil) = %v, want ErrNilStore", err)
	}
	if err := SetJSON(ctx, nil, "key", cachedUser{}, 0); !errors.Is(err, ErrNilStore) {
		t.Fatalf("SetJSON(nil) = %v, want ErrNilStore", err)
	}
	if _, err := Remember(ctx, nil, "key", 0, func(context.Context) ([]byte, error) {
		return nil, nil
	}); !errors.Is(err, ErrNilStore) {
		t.Fatalf("Remember(nil store) = %v, want ErrNilStore", err)
	}
	if _, err := Remember(ctx, NewMemory(), "key", 0, nil); !errors.Is(err, ErrNilLoader) {
		t.Fatalf("Remember(nil loader) = %v, want ErrNilLoader", err)
	}
	if _, err := RememberJSON[cachedUser](ctx, NewMemory(), "key", 0, nil); !errors.Is(err, ErrNilLoader) {
		t.Fatalf("RememberJSON(nil loader) = %v, want ErrNilLoader", err)
	}

	store := NewMemory()
	if err := store.Set(ctx, "broken", []byte("{"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := GetJSON[cachedUser](ctx, store, "broken"); err == nil {
		t.Fatal("expected JSON decoding error")
	}
	if err := SetJSON(ctx, store, "channel", make(chan int), 0); err == nil {
		t.Fatal("expected JSON encoding error")
	}

	loadErr := errors.New("load failed")
	if _, err := Remember(ctx, store, "missing", 0, func(context.Context) ([]byte, error) {
		return nil, loadErr
	}); !errors.Is(err, loadErr) {
		t.Fatalf("Remember() loader error = %v", err)
	}
	if _, err := RememberJSON(ctx, store, "missing-json", 0, func(context.Context) (cachedUser, error) {
		return cachedUser{}, loadErr
	}); !errors.Is(err, loadErr) {
		t.Fatalf("RememberJSON() loader error = %v", err)
	}
}

func TestHelperStoreErrors(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend failed")
	failingGet := &stubStore{getErr: backendErr}
	if _, err := Remember(ctx, failingGet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}); !errors.Is(err, backendErr) {
		t.Fatalf("Remember() get error = %v", err)
	}

	failingSet := &stubStore{getErr: ErrMiss, setErr: backendErr}
	if _, err := Remember(ctx, failingSet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}); !errors.Is(err, backendErr) {
		t.Fatalf("Remember() set error = %v", err)
	}
	if err := SetJSON(ctx, failingSet, "key", cachedUser{}, 0); !errors.Is(err, backendErr) {
		t.Fatalf("SetJSON() store error = %v", err)
	}

	loaderCalled := false
	brokenJSON := &stubStore{value: []byte("{")}
	if _, err := RememberJSON(ctx, brokenJSON, "key", 0, func(context.Context) (cachedUser, error) {
		loaderCalled = true
		return cachedUser{}, nil
	}); err == nil {
		t.Fatal("expected cached JSON decoding error")
	}
	if loaderCalled {
		t.Fatal("loader called for malformed cached JSON")
	}
	if failingSet.setCalls != 2 {
		t.Fatalf("Set() calls = %d, want 2", failingSet.setCalls)
	}
}

func TestRememberValidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	load := func(context.Context) ([]byte, error) { return nil, nil }
	if _, err := GetJSON[cachedUser](ctx, store, ""); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("GetJSON() empty key = %v", err)
	}
	if err := SetJSON(ctx, store, "", cachedUser{}, 0); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("SetJSON() empty key = %v", err)
	}
	if err := SetJSON(ctx, store, "key", cachedUser{}, -time.Second); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("SetJSON() negative TTL = %v", err)
	}
	if _, err := Remember(ctx, store, "", 0, load); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Remember() empty key = %v", err)
	}
	if _, err := Remember(ctx, store, "key", -time.Second, load); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("Remember() negative TTL = %v", err)
	}
	if _, err := RememberJSON(ctx, store, "", 0, func(context.Context) (cachedUser, error) {
		return cachedUser{}, nil
	}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("RememberJSON() empty key = %v", err)
	}
}
