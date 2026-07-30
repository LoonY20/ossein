package ossein

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLifecycleHooksRunInExpectedOrder(t *testing.T) {
	app := New()
	order := make([]string, 0, 4)

	app.OnStart(
		func(context.Context) error {
			order = append(order, "start-1")
			return nil
		},
		func(context.Context) error {
			order = append(order, "start-2")
			return nil
		},
	)

	app.OnStop(
		func(context.Context) error {
			order = append(order, "stop-1")
			return nil
		},
		func(context.Context) error {
			order = append(order, "stop-2")
			return nil
		},
	)

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	expected := []string{"start-1", "start-2", "stop-2", "stop-1"}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("expected lifecycle order %v, got %v", expected, order)
	}
}

func TestStopAttemptsAllHooksAndJoinsErrors(t *testing.T) {
	app := New()
	app.OnStop(
		func(context.Context) error { return errors.New("first") },
		func(context.Context) error { return errors.New("second") },
	)

	err := app.Stop(context.Background())
	if err == nil {
		t.Fatal("expected stop error")
	}
	for _, expected := range []string{"first", "second"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected joined error to contain %q, got %v", expected, err)
		}
	}
}
