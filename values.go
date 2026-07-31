package ossein

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Values provides typed access to a set of submitted values, whether they came
// from a query string or a form body.
//
// Accessors record a field-level error instead of returning one, so a bind method
// reads as a list of assignments. Values that are absent yield the zero value;
// values that are present but malformed are reported. Call Err, or let BindQuery
// and BindForm do it, to collect the result.
//
// Required and the typed accessors trim surrounding whitespace, since it is never
// meaningful for a number, a boolean, or a presence check. String returns the
// value exactly as submitted.
type Values struct {
	values url.Values
	errs   *ValidationError
}

// All returns the underlying values, for cases the accessors do not cover.
func (v *Values) All() url.Values {
	return v.values
}

// Has reports whether a field was submitted at all, which distinguishes an absent
// field from one submitted empty.
func (v *Values) Has(field string) bool {
	_, ok := v.values[field]
	return ok
}

// String returns a field value, or "" when it is absent.
func (v *Values) String(field string) string {
	return v.values.Get(field)
}

// Strings returns every value submitted for a repeated field.
func (v *Values) Strings(field string) []string {
	return v.values[field]
}

// Required returns a trimmed field value, recording an error when it is absent or
// blank.
func (v *Values) Required(field string) string {
	value := strings.TrimSpace(v.values.Get(field))
	if value == "" {
		v.AddError(field, "is required")
	}
	return value
}

// Int returns a field as an int. An absent field yields zero; a malformed one is
// reported.
func (v *Values) Int(field string) int {
	return int(v.parseInt(field, strconv.IntSize))
}

// Int64 returns a field as an int64.
func (v *Values) Int64(field string) int64 {
	return v.parseInt(field, 64)
}

func (v *Values) parseInt(field string, bits int) int64 {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(raw, 10, bits)
	if err != nil {
		v.AddError(field, "must be a whole number")
		return 0
	}
	return parsed
}

// Float64 returns a field as a float64.
//
// NaN and infinities are rejected: they parse successfully but make every
// comparison in an application's rules false, so a range check would silently
// pass, and encoding the value as JSON afterwards fails.
func (v *Values) Float64(field string) float64 {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		v.AddError(field, "must be a number")
		return 0
	}
	return parsed
}

// Bool returns a field as a bool, accepting the forms strconv.ParseBool does plus
// the "on" and "off" that HTML checkboxes and their clients use.
func (v *Values) Bool(field string) bool {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return false
	}
	switch {
	case strings.EqualFold(raw, "on"):
		return true
	case strings.EqualFold(raw, "off"):
		return false
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		v.AddError(field, "must be true or false")
		return false
	}
	return parsed
}

// AddError records an application rule failure against a field, so a bind method
// can add checks alongside the accessors.
func (v *Values) AddError(field, message string) {
	if v.errs == nil {
		v.errs = NewValidationError()
	}
	v.errs.Add(field, message)
}

// Err returns the recorded field errors, or nil when there are none.
func (v *Values) Err() error {
	return v.errs.OrNil()
}
