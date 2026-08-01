package ossein

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// TestGeneratedWiringCarriesAFingerprintAndDirective covers what makes generated
// wiring keepable. Without a fingerprint nothing notices a stale file, and without
// the directive regenerating it is something a person has to remember.
func TestGeneratedWiringCarriesAFingerprintAndDirective(t *testing.T) {
	app := New()
	if err := app.Provide(NewStaleStore); err != nil {
		t.Fatalf("Provide: %v", err)
	}

	source, err := GenerateWiring(app, "wiring")
	if err != nil {
		t.Fatalf("GenerateWiring: %v", err)
	}
	generated := string(source)

	if !strings.Contains(generated, "//go:generate ossein wire") {
		t.Fatalf("no generate directive:\n%s", generated)
	}

	fingerprint := app.WiringFingerprint()
	if fingerprint == "" {
		t.Fatal("the application reported no fingerprint")
	}
	if !strings.Contains(generated, `const Fingerprint = "`+fingerprint+`"`) {
		t.Fatalf("the generated fingerprint does not match the application's %q:\n%s",
			fingerprint, generated)
	}
}

// TestWiringFingerprintChangesWithTheGraph is the whole point: it has to move when
// the generated file would differ, and stay put when it would not.
func TestWiringFingerprintChangesWithTheGraph(t *testing.T) {
	base := New()
	if err := base.Provide(NewStaleStore); err != nil {
		t.Fatalf("Provide: %v", err)
	}
	baseline := base.WiringFingerprint()

	t.Run("stable across applications with the same graph", func(t *testing.T) {
		same := New()
		if err := same.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if got := same.WiringFingerprint(); got != baseline {
			t.Fatalf("the same graph produced %q and %q", baseline, got)
		}
	})

	t.Run("stable across repeated calls", func(t *testing.T) {
		if got := base.WiringFingerprint(); got != baseline {
			t.Fatalf("two calls produced %q and %q", baseline, got)
		}
	})

	t.Run("a new service moves it", func(t *testing.T) {
		extended := New()
		if err := extended.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := extended.Provide(NewStaleService); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if extended.WiringFingerprint() == baseline {
			t.Fatal("adding a service did not change the fingerprint")
		}
	})

	t.Run("a different constructor moves it", func(t *testing.T) {
		other := New()
		if err := other.Provide(NewOtherStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if other.WiringFingerprint() == baseline {
			t.Fatal("swapping the constructor did not change the fingerprint")
		}
	})

	t.Run("a different lifetime moves it", func(t *testing.T) {
		transient := New()
		if err := transient.Provide(NewStaleStore, Transient()); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if transient.WiringFingerprint() == baseline {
			t.Fatal("changing the lifetime did not change the fingerprint")
		}
	})

	t.Run("a new dependency moves it", func(t *testing.T) {
		before := New()
		if err := before.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := before.Provide(NewStaleService); err != nil {
			t.Fatalf("Provide: %v", err)
		}

		after := New()
		if err := after.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := after.Provide(NewDependentStaleService); err != nil {
			t.Fatalf("Provide: %v", err)
		}

		if before.WiringFingerprint() == after.WiringFingerprint() {
			t.Fatal("changing a constructor's parameters did not change the fingerprint")
		}
	})

	t.Run("registration order does not move it", func(t *testing.T) {
		forwards := New()
		if err := forwards.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := forwards.Provide(NewStaleService); err != nil {
			t.Fatalf("Provide: %v", err)
		}

		backwards := New()
		if err := backwards.Provide(NewStaleService); err != nil {
			t.Fatalf("Provide: %v", err)
		}
		if err := backwards.Provide(NewStaleStore); err != nil {
			t.Fatalf("Provide: %v", err)
		}

		if forwards.WiringFingerprint() != backwards.WiringFingerprint() {
			t.Fatal("the order services were registered in changed the fingerprint")
		}
	})
}

func TestWiringFingerprintOnAnEmptyOrNilApplication(t *testing.T) {
	empty := New().WiringFingerprint()
	if empty == "" {
		t.Fatal("an application with no services reported no fingerprint")
	}
	if New().WiringFingerprint() != empty {
		t.Fatal("an empty graph is not stable")
	}

	var nilApp *App
	if got := nilApp.WiringFingerprint(); got != "" {
		t.Fatalf("a nil application reported %q", got)
	}
}

// TestLowerCamelHandlesAcronyms covers the parameter names the generator writes.
// Lowercasing only the first rune produces "dB" for an *sql.DB parameter, which
// compiles and reads like a typo in every generated file.
func TestLowerCamelHandlesAcronyms(t *testing.T) {
	for name, want := range map[string]string{
		"DB":          "db",
		"ID":          "id",
		"URL":         "url",
		"HTTPClient":  "httpClient",
		"DBPool":      "dbPool",
		"Store":       "store",
		"LinkService": "linkService",
		"A":           "a",
		"AB":          "ab",
		"ABc":         "aBc",
		"store":       "store",
		"":            "",
	} {
		if got := lowerCamel(name); got != want {
			t.Fatalf("lowerCamel(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestGeneratedInstanceParameterIsReadable is the same property through the
// generator, where it is actually seen.
func TestGeneratedInstanceParameterIsReadable(t *testing.T) {
	app := New()
	if err := Instance(app, (*sql.DB)(nil)); err != nil {
		t.Fatalf("Instance: %v", err)
	}

	source, err := GenerateWiring(app, "wiring")
	if err != nil {
		t.Fatalf("GenerateWiring: %v", err)
	}
	generated := string(source)

	if !strings.Contains(generated, "func Wire(db *sql.DB)") {
		t.Fatalf("the *sql.DB parameter is not named db:\n%s", generated)
	}
	if strings.Contains(generated, "dB") {
		t.Fatalf("the generated source still contains dB:\n%s", generated)
	}
}

// Types used by the tests above. They live outside package main and are exported,
// which is what the generator requires.
type StaleStore struct{}

func NewStaleStore() *StaleStore { return &StaleStore{} }

func NewOtherStaleStore() *StaleStore { return &StaleStore{} }

type StaleService struct{}

func NewStaleService() *StaleService { return &StaleService{} }

func NewDependentStaleService(*StaleStore) *StaleService { return &StaleService{} }

// TestTypeIdentityDistinguishesSameNamedTypes covers the part of the fingerprint
// that has to be a full identity rather than a name. Two packages routinely define
// a Store, and a fingerprint that could not tell them apart would miss a swap
// between them — the change most likely to break generated wiring.
func TestTypeIdentityDistinguishesSameNamedTypes(t *testing.T) {
	named := typeIdentity(reflect.TypeOf(StaleStore{}))
	if !strings.Contains(named, "ossein.StaleStore") {
		t.Fatalf("typeIdentity = %q, want the package path included", named)
	}

	// An unnamed type has no package path and falls back to its structural
	// description, which is still stable.
	unnamed := typeIdentity(reflect.TypeOf(struct{ A int }{}))
	if unnamed == "" || strings.Contains(unnamed, "ossein.") {
		t.Fatalf("typeIdentity for an unnamed type = %q", unnamed)
	}
	if typeIdentity(nil) != "<nil>" {
		t.Fatalf("typeIdentity(nil) = %q", typeIdentity(nil))
	}

	// And the identity of a pointer differs from its element, which the container
	// treats as different registrations.
	if typeIdentity(reflect.TypeOf(&StaleStore{})) == named {
		t.Fatal("a pointer and its element share an identity")
	}
}
