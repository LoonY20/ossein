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
	// ClassForeignKeyViolation is a reference to a row that is not there, or a
	// delete that would orphan one.
	ClassForeignKeyViolation
	// ClassNotNullViolation is a missing required column.
	ClassNotNullViolation
	// ClassCheckViolation is a failed CHECK constraint.
	ClassCheckViolation
	// ClassDeadlock is a transaction chosen as the victim of a deadlock. Retrying
	// it is usually correct.
	ClassDeadlock
	// ClassSerializationFailure is a transaction that could not be serialized
	// against a concurrent one. Retrying it is usually correct.
	ClassSerializationFailure
)

// String returns the class name, for logs and test failures.
func (c ErrorClass) String() string {
	switch c {
	case ClassUniqueViolation:
		return "unique_violation"
	case ClassForeignKeyViolation:
		return "foreign_key_violation"
	case ClassNotNullViolation:
		return "not_null_violation"
	case ClassCheckViolation:
		return "check_violation"
	case ClassDeadlock:
		return "deadlock"
	case ClassSerializationFailure:
		return "serialization_failure"
	default:
		return "unknown"
	}
}

// Recognizer names a database error, reporting false when it cannot.
//
// It is the extension point: this package cannot import a driver — the core has no
// third-party dependencies, and an application should not inherit one it does not
// use — so it recognises errors through the interfaces and message shapes drivers
// happen to expose. A driver it does not know about is handled by supplying one of
// these.
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
// Wrapped errors are unwrapped, so an error carried up through a repository is
// still recognised.
func (c *Classifier) Classify(err error) ErrorClass {
	// A nil error needs no guard: the loop below does not run for one, and the
	// answer is the same ClassUnknown a guard would return.
	for current := err; current != nil; current = errors.Unwrap(current) {
		for _, recognize := range c.recognizers {
			if class, ok := recognize(current); ok {
				return class
			}
		}
	}
	return ClassUnknown
}

// defaultClassifier knows only the built-in recognizers.
var defaultClassifier = NewClassifier()

// Classify names err using the built-in recognizers.
func Classify(err error) ErrorClass {
	return defaultClassifier.Classify(err)
}

// IsUniqueViolation reports whether err is a duplicate key.
func IsUniqueViolation(err error) bool {
	return Classify(err) == ClassUniqueViolation
}

// IsForeignKeyViolation reports whether err is a broken reference.
func IsForeignKeyViolation(err error) bool {
	return Classify(err) == ClassForeignKeyViolation
}

// IsNotNullViolation reports whether err is a missing required column.
func IsNotNullViolation(err error) bool {
	return Classify(err) == ClassNotNullViolation
}

// IsCheckViolation reports whether err is a failed CHECK constraint.
func IsCheckViolation(err error) bool {
	return Classify(err) == ClassCheckViolation
}

// IsRetryable reports whether err is a deadlock or a serialization failure, the
// two classes where running the same transaction again is the documented remedy
// rather than a hope.
func IsRetryable(err error) bool {
	switch Classify(err) {
	case ClassDeadlock, ClassSerializationFailure:
		return true
	default:
		return false
	}
}

// builtinRecognizers are tried in order. Structured mechanisms come before
// message shapes, so a driver that exposes a code is never classified by text.
var builtinRecognizers = []Recognizer{
	recognizeSQLState,
	recognizeSQLiteCode,
	recognizeMessage,
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
	default:
		return ClassUnknown, false
	}
}

// recognizeSQLiteCode reads the extended result code that some SQLite drivers
// expose as a Code method.
//
// The extended codes are what distinguish the constraint kinds; the primary code
// for all of them is 19, which says only "a constraint failed".
func recognizeSQLiteCode(err error) (ErrorClass, bool) {
	coder, ok := err.(interface{ Code() int })
	if !ok {
		return ClassUnknown, false
	}

	switch coder.Code() {
	case 2067, 1555: // SQLITE_CONSTRAINT_UNIQUE, SQLITE_CONSTRAINT_PRIMARYKEY
		return ClassUniqueViolation, true
	case 787: // SQLITE_CONSTRAINT_FOREIGNKEY
		return ClassForeignKeyViolation, true
	case 1299: // SQLITE_CONSTRAINT_NOTNULL
		return ClassNotNullViolation, true
	case 275: // SQLITE_CONSTRAINT_CHECK
		return ClassCheckViolation, true
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return ClassSerializationFailure, true
	default:
		return ClassUnknown, false
	}
}

// recognizeMessage is the fallback for drivers that put the code in the message
// and nowhere else.
//
// It is last, and it is the part of this package most likely to be wrong: a driver
// is free to reword its errors, and a message in another language will not match.
// It is here because the alternative for those drivers is every application doing
// the same matching, worse — and when it is wrong the result is ClassUnknown,
// which is the same answer the application would have had without this package.
func recognizeMessage(err error) (ErrorClass, bool) {
	message := err.Error()

	switch {
	// go-sql-driver/mysql renders "Error 1062 (23000): Duplicate entry ...".
	case strings.Contains(message, "Error 1062"):
		return ClassUniqueViolation, true
	case strings.Contains(message, "Error 1452"), strings.Contains(message, "Error 1451"):
		return ClassForeignKeyViolation, true
	case strings.Contains(message, "Error 1048"):
		return ClassNotNullViolation, true
	case strings.Contains(message, "Error 3819"):
		return ClassCheckViolation, true
	case strings.Contains(message, "Error 1213"):
		return ClassDeadlock, true
	case strings.Contains(message, "Error 1205"):
		return ClassSerializationFailure, true

	// mattn/go-sqlite3 exposes its codes as struct fields and nothing else.
	case strings.Contains(message, "UNIQUE constraint failed"):
		return ClassUniqueViolation, true
	case strings.Contains(message, "FOREIGN KEY constraint failed"):
		return ClassForeignKeyViolation, true
	case strings.Contains(message, "NOT NULL constraint failed"):
		return ClassNotNullViolation, true
	case strings.Contains(message, "CHECK constraint failed"):
		return ClassCheckViolation, true
	case strings.Contains(message, "database is locked"):
		return ClassSerializationFailure, true

	default:
		return ClassUnknown, false
	}
}
