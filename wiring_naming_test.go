package ossein

import (
	"reflect"
	"strings"
	"testing"
)

// TestGeneratedParameterNamesAreUniqueAndUsable covers what makes the generated file
// compile. Field names are unique but the names derived from them are not:
// HTTPClient and HttpClient both lower to httpClient, which is two Wire parameters
// with one name — reported as generated, then rejected by the compiler, in a file
// the user is told not to edit. A type named IF lowers to a keyword, with the same
// result.
func TestGeneratedParameterNamesAreUniqueAndUsable(t *testing.T) {
	app := New()
	if err := Instance(app, (*HTTPClient)(nil)); err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if err := Instance(app, (*HttpClient)(nil)); err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if err := Instance(app, (*IF)(nil)); err != nil {
		t.Fatalf("Instance: %v", err)
	}

	source, err := GenerateWiring(app, "wiring")
	if err != nil {
		t.Fatalf("GenerateWiring: %v", err)
	}
	generated := string(source)

	// format.Source parses; it does not type-check, so duplicate parameters are
	// reported as a success. Reading the signature is what catches them.
	parameters := wireParameterNames(t, generated)
	if len(parameters) != 3 {
		t.Fatalf("Wire takes %d parameters, want 3:\n%s", len(parameters), generated)
	}

	seen := map[string]bool{}
	for _, name := range parameters {
		if seen[name] {
			t.Fatalf("parameter %q appears twice:\n%s", name, generated)
		}
		seen[name] = true
		if goKeywords[name] {
			t.Fatalf("parameter %q is a Go keyword:\n%s", name, generated)
		}
	}
}

// wireParameterNames pulls the parameter names out of the generated signature.
func wireParameterNames(t *testing.T, generated string) []string {
	t.Helper()

	const marker = "func Wire("
	start := strings.Index(generated, marker)
	if start < 0 {
		t.Fatalf("no Wire function:\n%s", generated)
	}
	rest := generated[start+len(marker):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatalf("unterminated Wire signature:\n%s", generated)
	}

	var names []string
	for _, parameter := range strings.Split(rest[:end], ",") {
		if fields := strings.Fields(strings.TrimSpace(parameter)); len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func TestUniqueVarNameSuffixesCollisionsAndKeywords(t *testing.T) {
	used := map[string]bool{}

	if got := uniqueVarName("client", used); got != "client" {
		t.Fatalf("first name = %q", got)
	}
	used["client"] = true

	if got := uniqueVarName("client", used); got != "client2" {
		t.Fatalf("collision = %q", got)
	}
	used["client2"] = true

	if got := uniqueVarName("client", used); got != "client3" {
		t.Fatalf("second collision = %q", got)
	}
	if got := uniqueVarName("if", used); got != "if2" {
		t.Fatalf("keyword = %q", got)
	}
	if got := uniqueVarName("", used); got != "value" {
		t.Fatalf("empty name = %q", got)
	}
}

// TestFingerprintCoversConstructorParameters is the doc's headline claim, and the
// one the first version of these tests could not see: its two graphs used
// differently named constructors, so the name alone moved the fingerprint.
//
// The registrations are built directly, which is the only way to hold the
// constructor identical and vary nothing but its inputs.
func TestFingerprintCoversConstructorParameters(t *testing.T) {
	storeType := reflect.TypeOf(&StaleStore{})
	build := func(inputs []reflect.Type) string {
		container := NewContainer()
		if err := container.addRegistration(&serviceRegistration{
			key:         storeType,
			constructor: reflect.ValueOf(NewStaleStore),
			inputs:      inputs,
			output:      storeType,
			lifetime:    SingletonLifetime,
		}); err != nil {
			t.Fatalf("addRegistration: %v", err)
		}
		return container.fingerprint()
	}

	if build(nil) == build([]reflect.Type{reflect.TypeOf("")}) {
		t.Fatal("a constructor's parameters are not part of the fingerprint")
	}
}

// TestFingerprintCoversTheConstructorReturnArity covers what the generated call site
// depends on: one return is `x := New()`, two is `x, err := New()` with an error
// check and an fmt import.
//
// Two functions made with reflect.MakeFunc report the same generated name, which is
// what isolates the arity from everything else in the entry.
func TestFingerprintCoversTheConstructorReturnArity(t *testing.T) {
	storeType := reflect.TypeOf(&StaleStore{})
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	build := func(outputs []reflect.Type) string {
		made := reflect.MakeFunc(
			reflect.FuncOf(nil, outputs, false),
			func([]reflect.Value) []reflect.Value { return nil },
		)
		container := NewContainer()
		if err := container.addRegistration(&serviceRegistration{
			key:         storeType,
			constructor: made,
			output:      storeType,
			lifetime:    SingletonLifetime,
		}); err != nil {
			t.Fatalf("addRegistration: %v", err)
		}
		return container.fingerprint()
	}

	plain := build([]reflect.Type{storeType})
	failing := build([]reflect.Type{storeType, errorType})
	if plain == failing {
		t.Fatal("the constructor's return arity is not part of the fingerprint")
	}
}

// TestFingerprintCoversEveryRegistration keeps the digest from resting on whichever
// entry happens to sort first.
func TestFingerprintCoversEveryRegistration(t *testing.T) {
	build := func(lifetime Lifetime) string {
		app := New()
		if err := app.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		// "Zzz" sorts last, so a digest built from the first entry alone misses it.
		if err := app.Provide(NewZzzService, withLifetime(lifetime)); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		return app.WiringFingerprint()
	}

	if build(SingletonLifetime) == build(TransientLifetime) {
		t.Fatal("a change to the last-sorting registration did not move the fingerprint")
	}
}

// withLifetime selects a lifetime option by value, for a table-driven test.
func withLifetime(lifetime Lifetime) ServiceOption {
	if lifetime == TransientLifetime {
		return Transient()
	}
	return func(*serviceOptions) {}
}

// TestTypeIdentityWalksCompositeTypes covers the shape the fingerprint mostly sees.
// reflect.Type.PkgPath is empty for a pointer and String uses only the short package
// name, so *a/svc.Config and *b/svc.Config would otherwise share an identity — and an
// Instance registration has no constructor name to fall back on.
func TestTypeIdentityWalksCompositeTypes(t *testing.T) {
	const wantPath = "github.com/LoonY20/ossein.StaleStore"

	pointer := typeIdentity(reflect.TypeOf(&StaleStore{}))
	if !strings.Contains(pointer, wantPath) {
		t.Fatalf("pointer identity = %q, want the package path", pointer)
	}
	if !strings.HasPrefix(pointer, "*") {
		t.Fatalf("pointer identity = %q, want it marked as a pointer", pointer)
	}

	for _, value := range []any{
		[]*StaleStore(nil),
		map[string]*StaleStore(nil),
		[3]*StaleStore{},
		make(chan *StaleStore),
	} {
		identity := typeIdentity(reflect.TypeOf(value))
		if !strings.Contains(identity, wantPath) {
			t.Fatalf("%T identity = %q, want the package path", value, identity)
		}
	}

	if got := typeIdentity(reflect.TypeOf(0)); got != "int" {
		t.Fatalf("builtin identity = %q", got)
	}
	if got := typeIdentity(nil); got != "<nil>" {
		t.Fatalf("nil identity = %q", got)
	}
}

// TestFingerprintDigestUsesEveryEntry guards the hash construction, which nothing
// else observes: a digest truncated to a byte, or entries concatenated without a
// separator, would still look like a fingerprint.
func TestFingerprintDigestUsesEveryEntry(t *testing.T) {
	app := New()
	if err := app.Provide(NewStaleStore); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	if len(app.WiringFingerprint()) != 64 {
		t.Fatalf("fingerprint is %d characters, want a full sha256 in hex",
			len(app.WiringFingerprint()))
	}

	// Two graphs differing only in where an entry boundary falls must not collide,
	// which they would if the entries were joined without a separator.
	first := New()
	if err := Instance(first, joinA{}); err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if err := Instance(first, joinBC{}); err != nil {
		t.Fatalf("Instance: %v", err)
	}

	second := New()
	if err := Instance(second, joinAB{}); err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if err := Instance(second, joinC{}); err != nil {
		t.Fatalf("Instance: %v", err)
	}

	if first.WiringFingerprint() == second.WiringFingerprint() {
		t.Fatal("two graphs with different entry boundaries collided")
	}
}

// Types whose names make the boundary between fingerprint entries observable.
type joinA struct{}
type joinBC struct{}
type joinAB struct{}
type joinC struct{}

// Types whose lowered names collide, or are keywords.
type HTTPClient struct{}
type HttpClient struct{}
type IF struct{}

// ZzzService sorts last among the test's registrations.
type ZzzService struct{}

// NewZzzService builds it.
func NewZzzService() *ZzzService { return &ZzzService{} }
