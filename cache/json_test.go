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
	onGet    func()
	onSet    func()
}

func (s *stubStore) Get(context.Context, string) ([]byte, error) {
	if s.onGet != nil {
		s.onGet()
	}
	return s.value, s.getErr
}

func (s *stubStore) Set(context.Context, string, []byte, time.Duration) error {
	s.setCalls++
	if s.onSet != nil {
		s.onSet()
	}
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
	} else if !errors.Is(err, ErrDecode) {
		t.Fatalf("GetJSON() error = %v, want ErrDecode", err)
	}
	if err := SetJSON(ctx, store, "channel", make(chan int), 0); !errors.Is(err, ErrEncode) {
		t.Fatalf("SetJSON() = %v, want ErrEncode", err)
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
	var reported []error
	value, err := Remember(ctx, failingGet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}, WithErrorHandler(func(_ context.Context, err error) {
		reported = append(reported, err)
	}))
	if err != nil || string(value) != "value" {
		t.Fatalf("Remember() with get error = %q, %v", value, err)
	}
	if len(reported) != 1 || !errors.Is(reported[0], backendErr) {
		t.Fatalf("reported get errors = %v", reported)
	}

	failingSet := &stubStore{getErr: ErrMiss, setErr: backendErr}
	value, err = Remember(ctx, failingSet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}, WithErrorHandler(func(_ context.Context, err error) {
		reported = append(reported, err)
	}))
	if err != nil || string(value) != "value" {
		t.Fatalf("Remember() with set error = %q, %v", value, err)
	}
	if len(reported) != 2 || !errors.Is(reported[1], backendErr) {
		t.Fatalf("reported set errors = %v", reported)
	}
	if err := SetJSON(ctx, failingSet, "key", cachedUser{}, 0); !errors.Is(err, backendErr) {
		t.Fatalf("SetJSON() store error = %v", err)
	}

	want := cachedUser{ID: 42, Name: "healed"}
	brokenJSON := &stubStore{value: []byte("{")}
	got, err := RememberJSON(ctx, brokenJSON, "key", 0, func(context.Context) (cachedUser, error) {
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("RememberJSON() self-heal = %#v, %v", got, err)
	}
	if brokenJSON.setCalls != 1 {
		t.Fatalf("RememberJSON() Set calls = %d, want 1", brokenJSON.setCalls)
	}
	if failingSet.setCalls != 2 {
		t.Fatalf("Set() calls = %d, want 2", failingSet.setCalls)
	}
}

func TestRememberJSONHealsCachedDecodeError(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	if err := store.Set(ctx, "user", []byte(`{"id":"old-schema"}`), 0); err != nil {
		t.Fatal(err)
	}
	want := cachedUser{ID: 42, Name: "healed"}
	loads := 0
	var reported error
	load := func(context.Context) (cachedUser, error) {
		loads++
		return want, nil
	}

	first, err := RememberJSON(ctx, store, "user", time.Minute, load,
		WithErrorHandler(func(_ context.Context, err error) {
			reported = err
		}),
	)
	if err != nil || first != want {
		t.Fatalf("first RememberJSON() = %#v, %v", first, err)
	}
	second, err := RememberJSON(ctx, store, "user", time.Minute, load)
	if err != nil || second != want {
		t.Fatalf("second RememberJSON() = %#v, %v", second, err)
	}
	if loads != 1 {
		t.Fatalf("loader calls = %d, want 1", loads)
	}
	if !errors.Is(reported, ErrDecode) {
		t.Fatalf("reported error = %v, want ErrDecode", reported)
	}
}

func TestRememberJSONReturnsLoadedValueWhenHealingWriteFails(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend failed")
	store := &stubStore{value: []byte("{"), setErr: backendErr}
	want := cachedUser{ID: 42, Name: "loaded"}

	got, err := RememberJSON(ctx, store, "key", time.Minute, func(context.Context) (cachedUser, error) {
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("RememberJSON() = %#v, %v", got, err)
	}
	if store.setCalls != 1 {
		t.Fatalf("Set() calls = %d, want 1", store.setCalls)
	}
}

func TestRememberUsesCallerContextState(t *testing.T) {
	ctx := context.Background()
	backendTimeoutGet := &stubStore{getErr: context.DeadlineExceeded}
	var reported error
	value, err := Remember(ctx, backendTimeoutGet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}, WithErrorHandler(func(_ context.Context, err error) {
		reported = err
	}))
	if err != nil || string(value) != "value" {
		t.Fatalf("Remember() backend-local get timeout = %q, %v", value, err)
	}
	if !errors.Is(reported, context.DeadlineExceeded) {
		t.Fatalf("reported get error = %v", reported)
	}

	cancelledDuringGet, cancelGet := context.WithCancel(ctx)
	loaderCalled := false
	cancelOnGet := &stubStore{value: []byte("cached"), onGet: cancelGet}
	if _, err := Remember(cancelledDuringGet, cancelOnGet, "key", 0, func(context.Context) ([]byte, error) {
		loaderCalled = true
		return []byte("loaded"), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remember() cancelled get context = %v", err)
	}
	if loaderCalled {
		t.Fatal("loader called after caller context cancellation during Get")
	}

	backendTimeoutSet := &stubStore{getErr: ErrMiss, setErr: context.DeadlineExceeded}
	value, err = Remember(ctx, backendTimeoutSet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	})
	if err != nil || string(value) != "value" {
		t.Fatalf("Remember() backend-local set timeout = %q, %v", value, err)
	}

	cancelledDuringLoad, cancelLoad := context.WithCancel(ctx)
	notWritten := &stubStore{getErr: ErrMiss}
	if _, err := Remember(cancelledDuringLoad, notWritten, "key", 0, func(context.Context) ([]byte, error) {
		cancelLoad()
		return []byte("value"), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remember() cancelled loader context = %v", err)
	}
	if notWritten.setCalls != 0 {
		t.Fatalf("Set() calls after loader cancellation = %d, want 0", notWritten.setCalls)
	}

	cancelledDuringSet, cancelSet := context.WithCancel(ctx)
	cancelOnSet := &stubStore{getErr: ErrMiss, onSet: cancelSet}
	if _, err := Remember(cancelledDuringSet, cancelOnSet, "key", 0, func(context.Context) ([]byte, error) {
		return []byte("value"), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remember() cancelled set context = %v", err)
	}
}

func TestRememberJSONReturnsEncodingErrors(t *testing.T) {
	reported := false
	_, err := RememberJSON(
		context.Background(),
		NewMemory(),
		"channel",
		time.Minute,
		func(context.Context) (chan int, error) {
			return make(chan int), nil
		},
		WithErrorHandler(func(context.Context, error) {
			reported = true
		}),
	)
	if !errors.Is(err, ErrEncode) {
		t.Fatalf("RememberJSON() = %v, want ErrEncode", err)
	}
	if reported {
		t.Fatal("strict JSON encoding error reported as recoverable")
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
