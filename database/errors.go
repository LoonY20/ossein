package database

import (
	"errors"
	"strings"
)

// ErrorClass names a database failure that applications routinely branch on.
//
// The point is that the branch survives a change of dialect. A unique violation
// is the same decision — "that code is taken, generate another" — whether the
// driver reports 23505, 2067, or 1062, and an application should not have to know
// which.
type ErrorClass int

const (
	// ClassUnknown means the error was not recognised. It is not "no error": a
	// failure this package cannot name is still a failure.
	ClassUnknown ErrorClass = iota
	// ClassUniqueViolation is a duplicate key.
	ClassUniqueViolation
	// ClassExclusionViolation is a row rejected by an EXCLUDE constraint. It is the
	// same shape of decision as a duplicate key but not the same thing, so it is
	// named separately rather than reported as one.
	ClassExclusionViolation
	// ClassForeignKeyViolation is a reference to a row that is not there, or a
	// delete that would orphan one.
	ClassForeignKeyViolation
	// ClassNotNullViolation is a missing required column.
	ClassNotNullViolation
	// ClassCheckViolation is a failed CHECK constraint.
	ClassCheckViolation
	// ClassDeadlock is a transaction chosen as the victim of a deadlock. The engine
	// has rolled it back; running it again is the remedy.
	ClassDeadlock
	// ClassSerializationFailure is a transaction that could not be serialized
	// against a concurrent one. As with a deadlock it is rolled back, and meant to
	// be retried.
	ClassSerializationFailure
	// ClassLockTimeout is a statement that gave up waiting for a lock.
	//
	// Deliberately not the same class as a deadlock. MySQL rolls back only the
	// failed statement unless innodb_rollback_on_timeout is on, and SQLite's busy
	// errors leave the transaction open too, so the caller has to roll back before
	// retrying. Reporting it as retryable would tell an application to re-run a
	// transaction that is still open, from a partially applied state.
	ClassLockTimeout

	// classCount bounds the enum. A class added above it is picked up by the tests
	// that iterate the range, which is why it is here.
	classCount
)

// classNames is indexed by ErrorClass.
var classNames = [classCount]string{
	ClassUnknown:              "unknown",
	ClassUniqueViolation:      "unique_violation",
	ClassExclusionViolation:   "exclusion_violation",
	ClassForeignKeyViolation:  "foreign_key_violation",
	ClassNotNullViolation:     "not_null_violation",
	ClassCheckViolation:       "check_violation",
	ClassDeadlock:             "deadlock",
	ClassSerializationFailure: "serialization_failure",
	ClassLockTimeout:          "lock_timeout",
}

// String returns the class name, for logs and test failures.
func (c ErrorClass) String() string {
	if c < 0 || c >= classCount {
		return "unknown"
	}
	return classNames[c]
}

// Sentinel errors an application can return to say what happened, for code that is
// not a SQL driver: a fake store in a test, an in-memory adapter behind the same
// interface, a service translating another system's failure.
//
// The predicates below match these as well as driver errors, so the two are
// interchangeable at a call site:
//
//	return fmt.Errorf("create link: %w", database.ErrUniqueViolation)
var (
	ErrUniqueViolation      = errors.New("ossein database: unique violation")
	ErrExclusionViolation   = errors.New("ossein database: exclusion violation")
	ErrForeignKeyViolation  = errors.New("ossein database: foreign key violation")
	ErrNotNullViolation     = errors.New("ossein database: not null violation")
	ErrCheckViolation       = errors.New("ossein database: check violation")
	ErrDeadlock             = errors.New("ossein database: deadlock")
	ErrSerializationFailure = errors.New("ossein database: serialization failure")
	ErrLockTimeout          = errors.New("ossein database: lock timeout")
)

// classSentinels pairs each class with the error an application can raise for it.
var classSentinels = [classCount]error{
	ClassUniqueViolation:      ErrUniqueViolation,
	ClassExclusionViolation:   ErrExclusionViolation,
	ClassForeignKeyViolation:  ErrForeignKeyViolation,
	ClassNotNullViolation:     ErrNotNullViolation,
	ClassCheckViolation:       ErrCheckViolation,
	ClassDeadlock:             ErrDeadlock,
	ClassSerializationFailure: ErrSerializationFailure,
	ClassLockTimeout:          ErrLockTimeout,
}

// Recognizer names a database error, reporting false when it cannot.
//
// It is the extension point: this package cannot import a driver — the core has no
// third-party dependencies, and an application should not inherit one it does not
// use — so it recognises errors through the interfaces and message shapes drivers
// happen to expose. A driver it does not know about is handled by supplying one of
// these.
//
// Returning (ClassUnknown, true) is meaningful: it stops the search, which is how a
// recognizer says "this error is mine, and it is none of these classes".
type Recognizer func(error) (ErrorClass, bool)

// Classifier names database errors using an ordered list of recognizers.
//
// The zero value is not useful; build one with NewClassifier.
type Classifier struct {
	recognizers []Recognizer
}

// NewClassifier returns a classifier that tries the given recognizers before the
// built-in ones, so an application can correct or extend what this package knows
// without waiting for it to learn.
//
// "Before" means across the whole error chain, not merely at its outermost level: a
// custom recognizer is consulted at every depth before any built-in is consulted at
// any depth, so an override is not lost the moment a repository wraps the error.
func NewClassifier(recognizers ...Recognizer) *Classifier {
	combined := make([]Recognizer, 0, len(recognizers)+len(builtinRecognizers))
	for _, recognizer := range recognizers {
		if recognizer != nil {
			combined = append(combined, recognizer)
		}
	}
	return &Classifier{recognizers: append(combined, builtinRecognizers...)}
}

// Classify names err, returning ClassUnknown when nothing recognises it.
//
// Each recognizer is tried against the whole error chain before the next one is
// tried at all. That ordering is the guarantee that a driver reporting a code is
// never overruled by text: a wrapper's message contains the text of everything it
// wraps, so the other nesting would let a quoted string decide the class of the
// error underneath it — turning a serialization failure that should be retried into
// a unique violation that should not.
//
// Errors joined with errors.Join, or wrapped with several %w verbs, are walked too,
// which errors.Unwrap alone does not do.
func (c *Classifier) Classify(err error) ErrorClass {
	for _, recognize := range c.recognizers {
		if class, ok := classifyChain(err, recognize); ok {
			return class
		}
	}
	return ClassUnknown
}

// classifyChain applies one recognizer to every error in a chain, depth first, so a
// joined or multiply-wrapped error is covered as well as a singly-wrapped one.
func classifyChain(err error, recognize Recognizer) (ErrorClass, bool) {
	for current := err; current != nil; {
		if class, ok := recognize(current); ok {
			return class, true
		}

		switch unwrapped := current.(type) {
		case interface{ Unwrap() error }:
			current = unwrapped.Unwrap()
		case interface{ Unwrap() []error }:
			for _, branch := range unwrapped.Unwrap() {
				if class, ok := classifyChain(branch, recognize); ok {
					return class, true
				}
			}
			return ClassUnknown, false
		default:
			return ClassUnknown, false
		}
	}
	return ClassUnknown, false
}

// defaultClassifier knows only the built-in recognizers.
var defaultClassifier = NewClassifier()

// Classify names err using the built-in recognizers.
func Classify(err error) ErrorClass {
	return defaultClassifier.Classify(err)
}

// IsUniqueViolation reports whether err is a duplicate key.
func IsUniqueViolation(err error) bool {
	return isClass(err, ClassUniqueViolation)
}

// IsExclusionViolation reports whether err conflicts with an EXCLUDE constraint.
func IsExclusionViolation(err error) bool {
	return isClass(err, ClassExclusionViolation)
}

// IsForeignKeyViolation reports whether err is a broken reference.
func IsForeignKeyViolation(err error) bool {
	return isClass(err, ClassForeignKeyViolation)
}

// IsNotNullViolation reports whether err is a missing required column.
func IsNotNullViolation(err error) bool {
	return isClass(err, ClassNotNullViolation)
}

// IsCheckViolation reports whether err is a failed CHECK constraint.
func IsCheckViolation(err error) bool {
	return isClass(err, ClassCheckViolation)
}

// IsLockTimeout reports whether a statement gave up waiting for a lock.
//
// Retrying is usually right, but only after rolling back: unlike a deadlock, the
// engine may have rolled back just the failed statement and left the transaction
// open, holding every lock it already had.
func IsLockTimeout(err error) bool {
	return isClass(err, ClassLockTimeout)
}

// IsRetryable reports whether err is a deadlock or a serialization failure — the
// two classes the engine has already rolled back, where running the same
// transaction again is the documented remedy rather than a hope.
//
// A lock timeout is deliberately excluded; see IsLockTimeout.
func IsRetryable(err error) bool {
	return isClass(err, ClassDeadlock) || isClass(err, ClassSerializationFailure)
}

// isClass reports whether err belongs to a class.
func isClass(err error, class ErrorClass) bool {
	if err == nil {
		return false
	}
	return Classify(err) == class
}

// builtinRecognizers are tried in order, each against the whole error chain.
// Structured mechanisms come before message shapes, so a driver that reports a code
// is never classified by text.
var builtinRecognizers = []Recognizer{
	recognizeSentinel,
	recognizeSQLState,
	recognizeSQLiteCode,
	recognizeMessage,
}

// recognizeSentinel names the errors this package exports, so an adapter that is
// not a SQL driver can report a class and have Classify agree with the predicates.
func recognizeSentinel(err error) (ErrorClass, bool) {
	for class, sentinel := range classSentinels {
		if sentinel != nil && errors.Is(err, sentinel) {
			return ErrorClass(class), true
		}
	}
	return ClassUnknown, false
}

// recognizeSQLState reads the five-character SQLSTATE that PostgreSQL drivers
// expose, and that some others do.
//
// SQLSTATE is a standard, which is why this comes first, but it is only as precise
// as the server that produced it: MySQL reports every integrity constraint as
// 23000, so that value is left for the message recognizer to resolve rather than
// guessed at here.
func recognizeSQLState(err error) (ErrorClass, bool) {
	stater, ok := err.(interface{ SQLState() string })
	if !ok {
		return ClassUnknown, false
	}

	switch stater.SQLState() {
	case "23505":
		return ClassUniqueViolation, true
	case "23P01":
		return ClassExclusionViolation, true
	case "23503":
		return ClassForeignKeyViolation, true
	case "23502":
		return ClassNotNullViolation, true
	case "23514":
		return ClassCheckViolation, true
	case "40P01":
		return ClassDeadlock, true
	case "40001":
		return ClassSerializationFailure, true
	case "55P03": // lock_not_available
		return ClassLockTimeout, true
	default:
		// 40003, statement_completion_unknown, is deliberately absent: the outcome
		// is unknown, so calling it retryable would invite a duplicate write.
		return ClassUnknown, false
	}
}

// recognizeSQLiteCode reads the result code that some SQLite drivers expose as a
// Code method.
//
// The message is checked as well, and not as a fallback: a bare Code() int is no
// evidence of SQLite. An Oracle driver has one, carrying ORA numbers that collide
// outright — ORA-01555, "snapshot too old", is SQLITE_CONSTRAINT_PRIMARYKEY — and
// so does any small application enum, where a code of 5 would become a lock
// timeout. The code decides which class; the message only confirms the family.
func recognizeSQLiteCode(err error) (ErrorClass, bool) {
	coder, ok := err.(interface{ Code() int })
	if !ok || !looksLikeSQLite(err.Error()) {
		return ClassUnknown, false
	}

	switch coder.Code() {
	case 2067, 1555, 2579: // CONSTRAINT_UNIQUE, CONSTRAINT_PRIMARYKEY, CONSTRAINT_ROWID
		return ClassUniqueViolation, true
	case 787: // CONSTRAINT_FOREIGNKEY
		return ClassForeignKeyViolation, true
	case 1299: // CONSTRAINT_NOTNULL
		return ClassNotNullViolation, true
	case 275, 1811: // CONSTRAINT_CHECK, CONSTRAINT_TRIGGER
		return ClassCheckViolation, true
	case 517: // BUSY_SNAPSHOT, the write-write conflict and a real serialization failure
		return ClassSerializationFailure, true
	case 5, 261, 773, 6, 262, 518: // BUSY and LOCKED, primary and extended
		return ClassLockTimeout, true
	default:
		// 19 is the primary constraint code, which says only that some constraint
		// failed — not a class an application can act on.
		return ClassUnknown, false
	}
}

// looksLikeSQLite reports whether a message came from SQLite. The strings are the
// library's own, and it does not localize them.
func looksLikeSQLite(message string) bool {
	return strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "SQLITE_")
}

// recognizeMessage is the fallback for drivers that put the code in the message and
// nowhere else.
//
// It is last, and it is the part of this package most likely to be wrong: a driver
// is free to reword its errors, a message in another language will not match, and a
// wrapper quoting the wrong text can mislead it. It is here because the alternative
// for those drivers is every application doing the same matching, worse — and when
// it is wrong the result is ClassUnknown, which is the answer the application would
// have had without this package.
func recognizeMessage(err error) (ErrorClass, bool) {
	message := err.Error()

	switch {
	// go-sql-driver/mysql renders "Error 1062 (23000): Duplicate entry ...", and
	// older versions "Error 1062: ...". The delimiter is required, so "Error 10620"
	// — MySQL 8 uses the 10000 range for server log events — does not match.
	case hasMySQLNumber(message, "1062"):
		return ClassUniqueViolation, true
	case hasMySQLNumber(message, "1452"), hasMySQLNumber(message, "1451"),
		hasMySQLNumber(message, "1216"), hasMySQLNumber(message, "1217"):
		return ClassForeignKeyViolation, true
	case hasMySQLNumber(message, "1048"):
		return ClassNotNullViolation, true
	// 4025 is MariaDB's CHECK violation, and go-sql-driver is its driver too.
	case hasMySQLNumber(message, "3819"), hasMySQLNumber(message, "4025"):
		return ClassCheckViolation, true
	case hasMySQLNumber(message, "1213"):
		return ClassDeadlock, true
	case hasMySQLNumber(message, "1205"):
		return ClassLockTimeout, true

	// mattn/go-sqlite3 exposes its codes as struct fields and nothing else.
	case strings.Contains(message, "UNIQUE constraint failed"):
		return ClassUniqueViolation, true
	case strings.Contains(message, "FOREIGN KEY constraint failed"):
		return ClassForeignKeyViolation, true
	case strings.Contains(message, "NOT NULL constraint failed"):
		return ClassNotNullViolation, true
	case strings.Contains(message, "CHECK constraint failed"):
		return ClassCheckViolation, true
	case strings.Contains(message, "database is locked"),
		strings.Contains(message, "database table is locked"):
		return ClassLockTimeout, true

	default:
		return ClassUnknown, false
	}
}

// hasMySQLNumber reports whether a message carries a MySQL error number, requiring
// the delimiter that follows it so a longer number cannot match a shorter one.
func hasMySQLNumber(message, number string) bool {
	prefix := "Error " + number
	index := strings.Index(message, prefix)
	if index < 0 {
		return false
	}
	rest := message[index+len(prefix):]
	return strings.HasPrefix(rest, " (") || strings.HasPrefix(rest, ":")
}
