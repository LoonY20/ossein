package cache_test

import (
	"context"
	"testing"

	ossein "github.com/LoonY20/ossein"
	"github.com/LoonY20/ossein/cache"
)

func TestMemoryStoreDependencyInjection(t *testing.T) {
	app := ossein.New()
	if err := ossein.ProvideAs[cache.Store](app, cache.NewMemory); err != nil {
		t.Fatal(err)
	}
	store, err := ossein.Resolve[cache.Store](app)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "value" {
		t.Fatalf("Get() = %q", value)
	}
}
