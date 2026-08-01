package ossein

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Lifetime controls how often a service constructor is invoked.
type Lifetime uint8

const (
	// SingletonLifetime creates a service once and reuses it for subsequent resolutions.
	SingletonLifetime Lifetime = iota
	// TransientLifetime creates a new service for every resolution.
	TransientLifetime
)

// ServiceOption configures a service registration.
type ServiceOption func(*serviceOptions)

type serviceOptions struct {
	lifetime Lifetime
}

// Transient configures a provider to build a new value on every resolution.
func Transient() ServiceOption {
	return func(options *serviceOptions) {
		options.lifetime = TransientLifetime
	}
}

// Singleton explicitly configures a provider as a singleton.
// Singleton is also the default lifetime.
func Singleton() ServiceOption {
	return func(options *serviceOptions) {
		options.lifetime = SingletonLifetime
	}
}

// Container stores application service providers and resolves their dependency graph.
type Container struct {
	mu            sync.RWMutex
	registrations map[reflect.Type]*serviceRegistration
}

type serviceRegistration struct {
	key         reflect.Type
	constructor reflect.Value
	inputs      []reflect.Type
	output      reflect.Type
	lifetime    Lifetime
	instance    reflect.Value
	ready       bool
	mu          sync.Mutex
}

// NewContainer creates an empty service container.
func NewContainer() *Container {
	return &Container{registrations: make(map[reflect.Type]*serviceRegistration)}
}

// Provide registers a constructor under its concrete output type.
// Constructors may return either T or (T, error).
func (c *Container) Provide(constructor any, options ...ServiceOption) error {
	return c.provide(nil, constructor, options...)
}

// Provide registers a constructor in the application's service container.
func (a *App) Provide(constructor any, options ...ServiceOption) error {
	return a.services.Provide(constructor, options...)
}

// ProvideAs registers a constructor under T, commonly an interface implemented by
// the constructor's concrete return type.
func ProvideAs[T any](app *App, constructor any, options ...ServiceOption) error {
	if app == nil {
		return errors.New("ossein: app cannot be nil")
	}
	return app.services.provide(typeOf[T](), constructor, options...)
}

// Instance registers an existing value under T as a singleton.
func Instance[T any](app *App, value T) error {
	if app == nil {
		return errors.New("ossein: app cannot be nil")
	}

	key := typeOf[T]()
	instance := reflect.ValueOf(value)
	if !instance.IsValid() {
		return fmt.Errorf("ossein: cannot register nil instance for %s", key)
	}
	if !instance.Type().AssignableTo(key) {
		return fmt.Errorf("ossein: instance type %s is not assignable to %s", instance.Type(), key)
	}

	registration := &serviceRegistration{
		key:      key,
		output:   instance.Type(),
		lifetime: SingletonLifetime,
		instance: instance,
		ready:    true,
	}

	return app.services.addRegistration(registration)
}

// Resolve builds or returns the service registered for T.
func Resolve[T any](app *App) (T, error) {
	var zero T
	if app == nil {
		return zero, errors.New("ossein: app cannot be nil")
	}

	value, err := app.services.resolve(typeOf[T](), nil)
	if err != nil {
		return zero, err
	}

	resolved, ok := value.Interface().(T)
	if !ok {
		return zero, fmt.Errorf("ossein: resolved %s cannot be assigned to requested type %s", value.Type(), typeOf[T]())
	}

	return resolved, nil
}

// Services returns the application's service container.
func (a *App) Services() *Container {
	return a.services
}

// Validate checks the full dependency graph without invoking constructors.
func (c *Container) Validate() error {
	registrations := c.snapshot()
	state := make(map[reflect.Type]uint8, len(registrations))
	path := make([]reflect.Type, 0, len(registrations))

	var visit func(reflect.Type) error
	visit = func(key reflect.Type) error {
		switch state[key] {
		case 1:
			return circularDependencyError(path, key)
		case 2:
			return nil
		}

		registration, ok := registrations[key]
		if !ok {
			return fmt.Errorf("ossein: service %s is not registered", key)
		}

		state[key] = 1
		path = append(path, key)
		defer func() { path = path[:len(path)-1] }()

		for _, dependency := range registration.inputs {
			if _, ok := registrations[dependency]; !ok {
				return fmt.Errorf("ossein: service %s depends on unregistered service %s", key, dependency)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}

		state[key] = 2
		return nil
	}

	for key := range registrations {
		if err := visit(key); err != nil {
			return err
		}
	}

	return nil
}

func (c *Container) provide(key reflect.Type, constructor any, options ...ServiceOption) error {
	constructorValue := reflect.ValueOf(constructor)
	if !constructorValue.IsValid() || constructorValue.Kind() != reflect.Func {
		return errors.New("ossein: service constructor must be a function")
	}

	constructorType := constructorValue.Type()
	if constructorType.NumOut() < 1 || constructorType.NumOut() > 2 {
		return errors.New("ossein: service constructor must return T or (T, error)")
	}

	output := constructorType.Out(0)
	if constructorType.NumOut() == 2 && constructorType.Out(1) != errorType {
		return errors.New("ossein: second constructor return value must be error")
	}

	if key == nil {
		key = output
	}
	if !output.AssignableTo(key) {
		return fmt.Errorf("ossein: constructor output %s is not assignable to registration type %s", output, key)
	}

	settings := serviceOptions{lifetime: SingletonLifetime}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}

	// A variadic parameter is options, not a dependency: nothing can be resolved
	// for a []Option, and the useful reading of a constructor that takes one is
	// "call it with none". Without this, adding an option parameter to an existing
	// constructor breaks every application that registers it.
	parameters := constructorType.NumIn()
	if constructorType.IsVariadic() {
		parameters--
	}

	inputs := make([]reflect.Type, parameters)
	for i := range inputs {
		inputs[i] = constructorType.In(i)
	}

	return c.addRegistration(&serviceRegistration{
		key:         key,
		constructor: constructorValue,
		inputs:      inputs,
		output:      output,
		lifetime:    settings.lifetime,
	})
}

func (c *Container) addRegistration(registration *serviceRegistration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.registrations[registration.key]; exists {
		return fmt.Errorf("ossein: service %s is already registered", registration.key)
	}

	c.registrations[registration.key] = registration
	return nil
}

func (c *Container) resolve(key reflect.Type, stack []reflect.Type) (reflect.Value, error) {
	for _, current := range stack {
		if current == key {
			return reflect.Value{}, circularDependencyError(stack, key)
		}
	}

	c.mu.RLock()
	registration, ok := c.registrations[key]
	c.mu.RUnlock()
	if !ok {
		return reflect.Value{}, fmt.Errorf("ossein: service %s is not registered", key)
	}

	if registration.lifetime == TransientLifetime {
		return c.build(registration, append(stack, key))
	}

	registration.mu.Lock()
	defer registration.mu.Unlock()

	if registration.ready {
		return registration.instance, nil
	}

	value, err := c.build(registration, append(stack, key))
	if err != nil {
		return reflect.Value{}, err
	}

	registration.instance = value
	registration.ready = true
	return value, nil
}

func (c *Container) build(registration *serviceRegistration, stack []reflect.Type) (reflect.Value, error) {
	arguments := make([]reflect.Value, len(registration.inputs))
	for i, dependency := range registration.inputs {
		value, err := c.resolve(dependency, stack)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("ossein: build %s: %w", registration.key, err)
		}
		arguments[i] = value
	}

	results := registration.constructor.Call(arguments)
	if len(results) == 2 && !results[1].IsNil() {
		return reflect.Value{}, fmt.Errorf("ossein: build %s: %w", registration.key, results[1].Interface().(error))
	}

	return results[0], nil
}

func (c *Container) snapshot() map[reflect.Type]*serviceRegistration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	registrations := make(map[reflect.Type]*serviceRegistration, len(c.registrations))
	for key, registration := range c.registrations {
		registrations[key] = registration
	}
	return registrations
}

func circularDependencyError(path []reflect.Type, repeated reflect.Type) error {
	start := 0
	for i, item := range path {
		if item == repeated {
			start = i
			break
		}
	}

	cycle := append(append([]reflect.Type(nil), path[start:]...), repeated)
	parts := make([]string, len(cycle))
	for i, item := range cycle {
		parts[i] = item.String()
	}
	return fmt.Errorf("ossein: circular service dependency: %s", strings.Join(parts, " -> "))
}

func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()
