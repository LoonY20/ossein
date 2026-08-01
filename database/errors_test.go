package database_test

import (
	"errors"
	"fmt"
	"strings"
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

func (e sqliteCodeError) Error() string {
	// The classifier requires a SQLite-shaped message alongside the code, since a
	// bare Code() int is no evidence of SQLite.
	return fmt.Sprintf("constraint failed: something (%d)", e.code)
}
func (e sqliteCodeError) Code() int { return e.code }

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
		2579: database.ClassUniqueViolation,
		1811: database.ClassCheckViolation,
		// The WAL write-write conflict, which is the real serialization failure.
		517: database.ClassSerializationFailure,
		// Busy and locked are lock contention, not serialization: the engine may
		// have left the transaction open, so retrying needs a rollback first.
		5:   database.ClassLockTimeout,
		6:   database.ClassLockTimeout,
		261: database.ClassLockTimeout,
		262: database.ClassLockTimeout,
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
		"Error 1205 (HY000): Lock wait timeout exceeded":                 database.ClassLockTimeout,
		"Error 4025 (23000): CONSTRAINT `c` failed":                      database.ClassCheckViolation,
		"Error 1216 (23000): Cannot add or update a child row":           database.ClassForeignKeyViolation,
		"Error 1062: Duplicate entry 'abc'":                              database.ClassUniqueViolation,
		"UNIQUE constraint failed: links.code":                           database.ClassUniqueViolation,
		"FOREIGN KEY constraint failed":                                  database.ClassForeignKeyViolation,
		"NOT NULL constraint failed: links.target":                       database.ClassNotNullViolation,
		"CHECK constraint failed: positive_clicks":                       database.ClassCheckViolation,
		"database is locked":                                             database.ClassLockTimeout,
		"connection refused":                                             database.ClassUnknown,
		// A longer number must not match a shorter one: MySQL 8 uses the 10000
		// range for server log events.
		"Error 10620: server log event": database.ClassUnknown,
		"Error 12062: something":        database.ClassUnknown,
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
		{sqlStateError{state: "23P01"}, database.IsExclusionViolation, "exclusion"},
		{sqlStateError{state: "55P03"}, database.IsLockTimeout, "lock timeout"},
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
		// A lock timeout is not in the retryable set: the engine may have left the
		// transaction open, so re-running it without a rollback is unsafe.
		sqlStateError{state: "55P03"},
		errors.New("Error 1205 (HY000): Lock wait timeout exceeded"),
	} {
		if database.IsRetryable(err) {
			t.Fatalf("%v was reported as retryable", err)
		}
	}

	// Every predicate must say no to something, or an always-true one would pass
	// every check above.
	unrelated := errors.New("connection refused")
	for name, check := range map[string]func(error) bool{
		"unique":       database.IsUniqueViolation,
		"exclusion":    database.IsExclusionViolation,
		"foreign key":  database.IsForeignKeyViolation,
		"not null":     database.IsNotNullViolation,
		"check":        database.IsCheckViolation,
		"lock timeout": database.IsLockTimeout,
		"retryable":    database.IsRetryable,
	} {
		if check(unrelated) {
			t.Fatalf("%s matched an unrelated error", name)
		}
		if check(sqlStateError{state: "23502"}) && name != "not null" {
			t.Fatalf("%s matched a not-null violation", name)
		}
	}
}

// TestErrorClassStringNamesEveryClass keeps a log line from reading as a number,
// and keeps a new class from silently rendering as "unknown".
func TestErrorClassStringNamesEveryClass(t *testing.T) {
	named := map[database.ErrorClass]string{
		database.ClassUnknown:              "unknown",
		database.ClassUniqueViolation:      "unique_violation",
		database.ClassExclusionViolation:   "exclusion_violation",
		database.ClassForeignKeyViolation:  "foreign_key_violation",
		database.ClassNotNullViolation:     "not_null_violation",
		database.ClassCheckViolation:       "check_violation",
		database.ClassDeadlock:             "deadlock",
		database.ClassSerializationFailure: "serialization_failure",
		database.ClassLockTimeout:          "lock_timeout",
	}
	for class, want := range named {
		if got := class.String(); got != want {
			t.Fatalf("class %d = %q, want %q", int(class), got, want)
		}
	}

	// Walked rather than listed, so a class added to the enum without a name shows
	// up here instead of silently rendering as "unknown". The first version of this
	// test iterated only the map above, which could not fail for that reason.
	for class := database.ClassUnknown; ; class++ {
		name := class.String()
		if _, known := named[class]; !known {
			if name != "unknown" {
				t.Fatalf("class %d = %q but is not in this test's list", int(class), name)
			}
			// Two consecutive unnamed classes means the end of the enum.
			if next := class + 1; next.String() == "unknown" {
				break
			}
			t.Fatalf("class %d has no name and renders as %q", int(class), name)
		}
		if class > 0 && name == "unknown" {
			t.Fatalf("class %d renders as \"unknown\"", int(class))
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
	if got := database.Classify(errLockWait); got != database.ClassLockTimeout {
		t.Fatalf("a custom classifier changed the package default: %v", got)
	}
}

var (
	errHouseStyle = errors.New("store: duplicate")
	errLockWait   = errors.New("Error 1205 (HY000): Lock wait timeout exceeded")
)

// TestAStructuredCodeBeatsTextAnywhereInTheChain is the guarantee the ordering
// exists for, and the one the first version of this file only appeared to cover.
//
// A wrapper's message contains the text of everything it wraps, so running every
// recognizer at the outermost level first lets a quoted string decide the class of
// the error underneath it. That turns a serialization failure the engine wants
// retried into a unique violation that will not be — the worst direction for the
// mistake to go.
func TestAStructuredCodeBeatsTextAnywhereInTheChain(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want database.ErrorClass
	}{
		{
			name: "wrapper text over an inner SQLSTATE",
			err: fmt.Errorf("insert link %q: %w",
				"UNIQUE constraint failed", sqlStateError{state: "40001"}),
			want: database.ClassSerializationFailure,
		},
		{
			name: "wrapper quoting a MySQL number over an inner SQLSTATE",
			err: fmt.Errorf("replaying %q: %w",
				"Error 1062 (23000): duplicate", sqlStateError{state: "40P01"}),
			want: database.ClassDeadlock,
		},
		{
			name: "wrapper text over an inner SQLite code",
			err: fmt.Errorf("import %q: %w",
				"Error 1213 (40001): deadlock", sqliteCodeError{code: 2067}),
			want: database.ClassUniqueViolation,
		},
	}

	for _, testCase := range cases {
		if got := database.Classify(testCase.err); got != testCase.want {
			t.Fatalf("%s: classified as %v, want %v", testCase.name, got, testCase.want)
		}
	}

	// And the retry decision, which is what the misclassification would break.
	retryable := fmt.Errorf("save %q: %w",
		"UNIQUE constraint failed", sqlStateError{state: "40001"})
	if !database.IsRetryable(retryable) {
		t.Fatal("a serialization failure wrapped by misleading text was not retryable")
	}
}

// TestClassifyWalksJoinedErrors covers the shape a retry loop produces. A
// multi-%w wrap and errors.Join are ordinary in exactly the code that needs to
// know whether to try again, and errors.Unwrap alone does not traverse them.
func TestClassifyWalksJoinedErrors(t *testing.T) {
	other := errors.New("cleanup also failed")

	joined := errors.Join(other, sqlStateError{state: "23505"})
	if !database.IsUniqueViolation(joined) {
		t.Fatalf("errors.Join hid the violation: %v", database.Classify(joined))
	}

	multi := fmt.Errorf("attempt 1: %w; attempt 2: %w", other, sqlStateError{state: "40P01"})
	if !database.IsRetryable(multi) {
		t.Fatalf("a multi-wrapped deadlock was not retryable: %v", database.Classify(multi))
	}
}

// TestACodeMethodAloneIsNotEvidenceOfSQLite covers a collision that classified
// other libraries' errors with confidence. An Oracle driver exposes Code() carrying
// ORA numbers — ORA-01555, "snapshot too old", is SQLITE_CONSTRAINT_PRIMARYKEY —
// and any small application enum has one too, where a code of 5 became a lock
// timeout and IsRetryable-adjacent advice followed.
func TestACodeMethodAloneIsNotEvidenceOfSQLite(t *testing.T) {
	for _, err := range []error{
		codeError{code: 1555, message: "ORA-01555: snapshot too old"},
		codeError{code: 2067, message: "ORA-02067: transaction or savepoint rollback required"},
		codeError{code: 5, message: "rpc: upstream unavailable"},
		codeError{code: 6, message: "billing: card declined"},
		codeError{code: 787, message: "http 787: nonsense"},
	} {
		if got := database.Classify(err); got != database.ClassUnknown {
			t.Fatalf("%q classified as %v", err.Error(), got)
		}
	}

	// A SQLite-shaped message with the same code is classified, so the guard has
	// not simply disabled the mechanism.
	sqlite := codeError{code: 2067, message: "constraint failed: UNIQUE constraint failed: t.c (2067)"}
	if got := database.Classify(sqlite); got != database.ClassUniqueViolation {
		t.Fatalf("a real SQLite error was not classified: %v", got)
	}
}

// codeError has the method set an Oracle driver and many unrelated libraries have.
type codeError struct {
	code    int
	message string
}

func (e codeError) Error() string { return e.message }
func (e codeError) Code() int     { return e.code }

// TestSentinelsAreClassified is what lets code that is not a SQL driver take part:
// a fake store in a test, or an adapter in front of another system.
func TestSentinelsAreClassified(t *testing.T) {
	for sentinel, want := range map[error]database.ErrorClass{
		database.ErrUniqueViolation:      database.ClassUniqueViolation,
		database.ErrExclusionViolation:   database.ClassExclusionViolation,
		database.ErrForeignKeyViolation:  database.ClassForeignKeyViolation,
		database.ErrNotNullViolation:     database.ClassNotNullViolation,
		database.ErrCheckViolation:       database.ClassCheckViolation,
		database.ErrDeadlock:             database.ClassDeadlock,
		database.ErrSerializationFailure: database.ClassSerializationFailure,
		database.ErrLockTimeout:          database.ClassLockTimeout,
	} {
		wrapped := fmt.Errorf("fake store: %w", sentinel)
		if got := database.Classify(wrapped); got != want {
			t.Fatalf("%v classified as %v, want %v", sentinel, got, want)
		}
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("%v is not matched by errors.Is after wrapping", sentinel)
		}
	}

	if !database.IsUniqueViolation(fmt.Errorf("create: %w", database.ErrUniqueViolation)) {
		t.Fatal("a sentinel was not matched by its predicate")
	}
	if database.IsRetryable(fmt.Errorf("create: %w", database.ErrUniqueViolation)) {
		t.Fatal("a unique violation sentinel was reported as retryable")
	}
}

// TestARecognizerCanStopTheSearch pins the meaning of the boolean, which is
// otherwise indistinguishable from returning the class alone.
func TestARecognizerCanStopTheSearch(t *testing.T) {
	classifier := database.NewClassifier(func(err error) (database.ErrorClass, bool) {
		if strings.Contains(err.Error(), "not ours") {
			// Claimed, and deliberately not one of the classes.
			return database.ClassUnknown, true
		}
		return database.ClassUnknown, false
	})

	// The built-in message recognizer would call this a unique violation.
	err := errors.New("not ours: UNIQUE constraint failed: t.c")
	if got := classifier.Classify(err); got != database.ClassUnknown {
		t.Fatalf("classified as %v; the recognizer claimed the error", got)
	}
}

// TestACustomRecognizerOverridesThroughAWrapper covers the guarantee that made the
// override worth having. A recognizer written with a plain type assertion used to
// lose to a built-in the moment a repository wrapped the error, because each level
// was tried against every recognizer before unwrapping.
func TestACustomRecognizerOverridesThroughAWrapper(t *testing.T) {
	classifier := database.NewClassifier(func(err error) (database.ErrorClass, bool) {
		if _, ok := err.(sqlStateError); ok {
			// This deployment treats every constraint failure as retryable.
			return database.ClassSerializationFailure, true
		}
		return database.ClassUnknown, false
	})

	wrapped := fmt.Errorf("repository: %w", sqlStateError{state: "23505"})
	if got := classifier.Classify(wrapped); got != database.ClassSerializationFailure {
		t.Fatalf("classified as %v; the override was lost through the wrapper", got)
	}
}
