package ossein

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testRepository interface {
	Name() string
}

type postgresRepository struct{}

func (*postgresRepository) Name() string { return "postgres" }

type userService struct {
	repository testRepository
}

type transientService struct {
	sequence int
}

type cycleA struct{ B *cycleB }
type cycleB struct{ A *cycleA }

type missingDependency struct{}

type needsMissingDependency struct {
	Dependency *missingDependency
}

type concreteConstructorError struct{}

func (concreteConstructorError) Error() string { return "constructor error" }

func TestContainerAutowiresDependencies(t *testing.T) {
	app := New()

	if err := ProvideAs[testRepository](app, func() *postgresRepository {
		return &postgresRepository{}
	}); err != nil {
		t.Fatalf("register repository: %v", err)
	}

	if err := app.Provide(func(repository testRepository) *userService {
		return &userService{repository: repository}
	}); err != nil {
		t.Fatalf("register service: %v", err)
	}

	service, err := Resolve[*userService](app)
	if err != nil {
		t.Fatalf("resolve service: %v", err)
	}

	if service.repository == nil || service.repository.Name() != "postgres" {
		t.Fatalf("expected injected repository, got %#v", service.repository)
	}
}

func TestSingletonIsDefaultLifetime(t *testing.T) {
	app := New()
	calls := 0

	if err := app.Provide(func() *userService {
		calls++
		return &userService{}
	}); err != nil {
		t.Fatalf("register service: %v", err)
	}

	first, err := Resolve[*userService](app)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	second, err := Resolve[*userService](app)
	if err != nil {
		t.Fatalf("resolve second: %v", err)
	}

	if first != second {
		t.Fatal("expected singleton resolutions to return the same instance")
	}
	if calls != 1 {
		t.Fatalf("expected constructor to run once, got %d", calls)
	}
}

func TestTransientBuildsNewValue(t *testing.T) {
	app := New()
	calls := 0

	if err := app.Provide(func() *transientService {
		calls++
		return &transientService{sequence: calls}
	}, Transient()); err != nil {
		t.Fatalf("register transient: %v", err)
	}

	first, err := Resolve[*transientService](app)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	second, err := Resolve[*transientService](app)
	if err != nil {
		t.Fatalf("resolve second: %v", err)
	}

	if first == second {
		t.Fatal("expected transient resolutions to return different instances")
	}
	if first.sequence != 1 || second.sequence != 2 || calls != 2 {
		t.Fatalf("expected two constructor calls, got first=%d second=%d calls=%d", first.sequence, second.sequence, calls)
	}
}

func TestConstructorErrorIsReturned(t *testing.T) {
	app := New()
	expected := errors.New("database unavailable")

	if err := app.Provide(func() (*userService, error) {
		return nil, expected
	}); err != nil {
		t.Fatalf("register service: %v", err)
	}

	_, err := Resolve[*userService](app)
	if !errors.Is(err, expected) {
		t.Fatalf("expected constructor error, got %v", err)
	}
}

func TestConstructorRequiresErrorAsSecondReturn(t *testing.T) {
	app := New()

	err := app.Provide(func() (*userService, concreteConstructorError) {
		return &userService{}, concreteConstructorError{}
	})
	if err == nil || !strings.Contains(err.Error(), "second constructor return value must be error") {
		t.Fatalf("expected invalid constructor signature error, got %v", err)
	}
}

func TestValidateDetectsMissingDependency(t *testing.T) {
	app := New()

	if err := app.Provide(func(dependency *missingDependency) *needsMissingDependency {
		return &needsMissingDependency{Dependency: dependency}
	}); err != nil {
		t.Fatalf("register service: %v", err)
	}

	err := app.Services().Validate()
	if err == nil || !strings.Contains(err.Error(), "unregistered service") {
		t.Fatalf("expected missing dependency error, got %v", err)
	}
}

func TestValidateDetectsCircularDependency(t *testing.T) {
	app := New()

	if err := app.Provide(func(b *cycleB) *cycleA {
		return &cycleA{B: b}
	}); err != nil {
		t.Fatalf("register cycle A: %v", err)
	}
	if err := app.Provide(func(a *cycleA) *cycleB {
		return &cycleB{A: a}
	}); err != nil {
		t.Fatalf("register cycle B: %v", err)
	}

	err := app.Services().Validate()
	if err == nil || !strings.Contains(err.Error(), "circular service dependency") {
		t.Fatalf("expected circular dependency error, got %v", err)
	}
}

func TestStartValidatesServicesBeforeHooks(t *testing.T) {
	app := New()
	hookCalled := false

	if err := app.Provide(func(dependency *missingDependency) *needsMissingDependency {
		return &needsMissingDependency{Dependency: dependency}
	}); err != nil {
		t.Fatalf("register service: %v", err)
	}

	app.OnStart(func(context.Context) error {
		hookCalled = true
		return nil
	})

	err := app.Start(context.Background())
	if err == nil {
		t.Fatal("expected service validation error")
	}
	if hookCalled {
		t.Fatal("expected startup hooks not to run after service validation failure")
	}
}

func TestDuplicateRegistrationFails(t *testing.T) {
	app := New()
	constructor := func() *userService { return &userService{} }

	if err := app.Provide(constructor); err != nil {
		t.Fatalf("register first service: %v", err)
	}
	if err := app.Provide(constructor); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestInstanceRegistersExistingValue(t *testing.T) {
	app := New()
	expected := &userService{}

	if err := Instance(app, expected); err != nil {
		t.Fatalf("register instance: %v", err)
	}

	actual, err := Resolve[*userService](app)
	if err != nil {
		t.Fatalf("resolve instance: %v", err)
	}
	if actual != expected {
		t.Fatal("expected Resolve to return the registered instance")
	}
}

// TestVariadicConstructorParametersAreOptionsNotDependencies covers the shape a
// constructor takes once it grows configuration. Nothing can be resolved for a
// []Option, and treating one as a dependency turns adding an option parameter
// into a breaking change: every existing registration fails at startup with
// "service []Option is not registered".
func TestVariadicConstructorParametersAreOptionsNotDependencies(t *testing.T) {
	type option func(*string)
	type widget struct{ name string }
	type dependency struct{ value string }

	t.Run("options only", func(t *testing.T) {
		app := New()
		if err := app.Provide(func(...option) *widget { return &widget{name: "built"} }); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := app.Services().Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}

		resolved, err := Resolve[*widget](app)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.name != "built" {
			t.Fatalf("widget = %+v", resolved)
		}
	})

	t.Run("dependency then options", func(t *testing.T) {
		app := New()
		if err := Instance(app, &dependency{value: "injected"}); err != nil {
			t.Fatalf("Instance: %v", err)
		}
		if err := app.Provide(func(d *dependency, _ ...option) (*widget, error) {
			return &widget{name: d.value}, nil
		}); err != nil {
			t.Fatalf("Provide: %v", err)
		}

		resolved, err := Resolve[*widget](app)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.name != "injected" {
			t.Fatalf("the non-variadic dependency was not injected: %+v", resolved)
		}
	})

	t.Run("a missing dependency is still reported", func(t *testing.T) {
		app := New()
		if err := app.Provide(func(*dependency, ...option) *widget { return &widget{} }); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := app.Services().Validate(); err == nil {
			t.Fatal("the missing dependency was not reported")
		}
	})
}
