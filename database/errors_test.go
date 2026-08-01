package database_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/LoonY20/ossein/database"
)

// sqlStateError is the shape PostgreSQL drivers expose.
type sqlStateError struct {
	state string
}

func (e sqlStateError) Error() string    { return "pq: " + e.state }
func (e sqlStateError) SQLState() string { return e.state }

// sqliteCodeError is the shape some SQLite drivers expose.
type sqliteCodeError struct {
	code int
}

func (e sqliteCodeError) Error() string { return fmt.Sprintf("sqlite: %d", e.code) }
func (e sqliteCodeError) Code() int     { return e.code }

func TestClassifyReadsSQLState(t *testing.T) {
	for state, want := range map[string]database.ErrorClass{
		"23505": database.ClassUniqueViolation,
		"23503": database.ClassForeignKeyViolation,
		"23502": database.ClassNotNullViolation,
		"23514": database.ClassCheckViolation,
		"40P01": database.ClassDeadlock,
		"40001": database.ClassSerializationFailure,
		"42P01": database.ClassUnknown,
	} {
		if got := database.Classify(sqlStateError{state: state}); got != want {
			t.Fatalf("SQLSTATE %s classified as %v, want %v", state, got, want)
		}
	}
}

// TestClassifyLeavesMySQLsAmbiguousSQLStateAlone covers the reason SQLSTATE is not
// the only mechanism. MySQL reports every integrity constraint as 23000, so
// reading it would name a foreign-key failure a duplicate key.
func TestClassifyLeavesMySQLsAmbiguousSQLStateAlone(t *testing.T) {
	if got := database.Classify(sqlStateError{state: "23000"}); got != database.ClassUnknown {
		t.Fatalf("SQLSTATE 23000 classified as %v; it names any integrity constraint", got)
	}
}

func TestClassifyReadsSQLiteExtendedCodes(t *testing.T) {
	for code, want := range map[int]database.ErrorClass{
		2067: database.ClassUniqueViolation,
		1555: database.ClassUniqueViolation,
		787:  database.ClassForeignKeyViolation,
		1299: database.ClassNotNullViolation,
		275:  database.ClassCheckViolation,
		5:    database.ClassSerializationFailure,
		6:    database.ClassSerializationFailure,
		// The primary constraint code says only that some constraint failed, which
		// is not a class an application can branch on.
		19: database.ClassUnknown,
	} {
		if got := database.Classify(sqliteCodeError{code: code}); got != want {
			t.Fatalf("SQLite code %d classified as %v, want %v", code, got, want)
		}
	}
}

func TestClassifyFallsBackToTheMessage(t *testing.T) {
	for message, want := range map[string]database.ErrorClass{
		"Error 1062 (23000): Duplicate entry 'abc' for key 'links.code'": database.ClassUniqueViolation,
		"Error 1452 (23000): Cannot add or update a child row":           database.ClassForeignKeyViolation,
		"Error 1451 (23000): Cannot delete or update a parent row":       database.ClassForeignKeyViolation,
		"Error 1048 (23000): Column 'target' cannot be null":             database.ClassNotNullViolation,
		"Error 3819 (HY000): Check constraint 'c' is violated":           database.ClassCheckViolation,
		"Error 1213 (40001): Deadlock found when trying to get lock":     database.ClassDeadlock,
		"Error 1205 (HY000): Lock wait timeout exceeded":                 database.ClassSerializationFailure,
		"UNIQUE constraint failed: links.code":                           database.ClassUniqueViolation,
		"FOREIGN KEY constraint failed":                                  database.ClassForeignKeyViolation,
		"NOT NULL constraint failed: links.target":                       database.ClassNotNullViolation,
		"CHECK constraint failed: positive_clicks":                       database.ClassCheckViolation,
		"database is locked":                                             database.ClassSerializationFailure,
		"connection refused":                                             database.ClassUnknown,
	} {
		if got := database.Classify(errors.New(message)); got != want {
			t.Fatalf("%q classified as %v, want %v", message, got, want)
		}
	}
}

// TestAStructuredCodeBeatsTheMessage is the ordering that keeps text matching from
// overruling a driver that actually said what happened. A PostgreSQL deadlock
// whose message happens to quote a row containing "UNIQUE constraint failed" must
// still be a deadlock.
func TestAStructuredCodeBeatsTheMessage(t *testing.T) {
	err := misleadingError{
		state:   "40P01",
		message: "deadlock detected while inserting: UNIQUE constraint failed: links.code",
	}
	if got := database.Classify(err); got != database.ClassDeadlock {
		t.Fatalf("classified as %v, want the SQLSTATE to win", got)
	}
}

type misleadingError struct {
	state   string
	message string
}

func (e misleadingError) Error() string    { return e.message }
func (e misleadingError) SQLState() string { return e.state }

// TestClassifyUnwraps covers the shape an error has by the time it reaches a
// handler: wrapped by a repository, and wrapped again by a service.
func TestClassifyUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("create link: %w",
		fmt.Errorf("insert: %w", sqlStateError{state: "23505"}))

	if !database.IsUniqueViolation(wrapped) {
		t.Fatalf("a wrapped unique violation was classified as %v", database.Classify(wrapped))
	}
}

func TestClassifyOnANilError(t *testing.T) {
	if got := database.Classify(nil); got != database.ClassUnknown {
		t.Fatalf("Classify(nil) = %v", got)
	}
	if database.IsUniqueViolation(nil) || database.IsRetryable(nil) {
		t.Fatal("a nil error was classified as a failure")
	}
}

func TestHelpersMatchTheirClasses(t *testing.T) {
	cases := []struct {
		err   error
		check func(error) bool
		name  string
	}{
		{sqlStateError{state: "23505"}, database.IsUniqueViolation, "unique"},
		{sqlStateError{state: "23503"}, database.IsForeignKeyViolation, "foreign key"},
		{sqlStateError{state: "23502"}, database.IsNotNullViolation, "not null"},
		{sqlStateError{state: "23514"}, database.IsCheckViolation, "check"},
		{sqlStateError{state: "40P01"}, database.IsRetryable, "deadlock is retryable"},
		{sqlStateError{state: "40001"}, database.IsRetryable, "serialization is retryable"},
	}
	for _, testCase := range cases {
		if !testCase.check(testCase.err) {
			t.Fatalf("%s: %v was not matched", testCase.name, testCase.err)
		}
	}

	// And the two classes that are not retryable must not be reported as such: a
	// unique violation retried is a unique violation again, forever.
	for _, err := range []error{
		sqlStateError{state: "23505"},
		sqlStateError{state: "23503"},
		errors.New("UNIQUE constraint failed: links.code"),
	} {
		if database.IsRetryable(err) {
			t.Fatalf("%v was reported as retryable", err)
		}
	}
}

// TestErrorClassStringNamesEveryClass keeps a log line from reading as a number,
// and keeps a new class from silently rendering as "unknown".
func TestErrorClassStringNamesEveryClass(t *testing.T) {
	for class, want := range map[database.ErrorClass]string{
		database.ClassUnknown:              "unknown",
		database.ClassUniqueViolation:      "unique_violation",
		database.ClassForeignKeyViolation:  "foreign_key_violation",
		database.ClassNotNullViolation:     "not_null_violation",
		database.ClassCheckViolation:       "check_violation",
		database.ClassDeadlock:             "deadlock",
		database.ClassSerializationFailure: "serialization_failure",
	} {
		if got := class.String(); got != want {
			t.Fatalf("class %d = %q, want %q", int(class), got, want)
		}
	}
}

// TestACustomRecognizerRunsFirst is the escape hatch. This package cannot import a
// driver, so a driver it does not know about — or knows wrongly — has to be
// teachable without waiting for a release.
func TestACustomRecognizerRunsFirst(t *testing.T) {
	classifier := database.NewClassifier(
		nil, // tolerated, as options are elsewhere
		func(err error) (database.ErrorClass, bool) {
			if errors.Is(err, errHouseStyle) {
				return database.ClassUniqueViolation, true
			}
			return database.ClassUnknown, false
		},
		// Corrects a built-in: this deployment treats lock waits as deadlocks.
		func(err error) (database.ErrorClass, bool) {
			if errors.Is(err, errLockWait) {
				return database.ClassDeadlock, true
			}
			return database.ClassUnknown, false
		},
	)

	if got := classifier.Classify(fmt.Errorf("save: %w", errHouseStyle)); got != database.ClassUniqueViolation {
		t.Fatalf("the custom recognizer was not consulted: %v", got)
	}
	if got := classifier.Classify(errLockWait); got != database.ClassDeadlock {
		t.Fatalf("a custom recognizer did not override the built-in: %v", got)
	}
	// The built-ins still apply to everything else.
	if got := classifier.Classify(sqlStateError{state: "23505"}); got != database.ClassUniqueViolation {
		t.Fatalf("the built-ins were lost: %v", got)
	}
	// And the package-level classifier is unaffected by another one's rules.
	if got := database.Classify(errLockWait); got != database.ClassSerializationFailure {
		t.Fatalf("a custom classifier changed the package default: %v", got)
	}
}

var (
	errHouseStyle = errors.New("store: duplicate")
	errLockWait   = errors.New("Error 1205 (HY000): Lock wait timeout exceeded")
)
