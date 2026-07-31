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

// NewValues wraps a set of values, so a bind method can be exercised directly
// rather than only through a request.
func NewValues(values url.Values) *Values {
	if values == nil {
		values = url.Values{}
	}
	return &Values{values: values}
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
	parsed, _ := v.parseInt(field, strconv.IntSize)
	return int(parsed)
}

// Int64 returns a field as an int64.
func (v *Values) Int64(field string) int64 {
	parsed, _ := v.parseInt(field, 64)
	return parsed
}

// Float64 returns a field as a float64.
//
// NaN and infinities are rejected: they parse successfully but make every
// comparison in an application's rules false, so a range check would silently
// pass, and encoding the value as JSON afterwards fails.
func (v *Values) Float64(field string) float64 {
	parsed, _ := v.parseFloat(field)
	return parsed
}

// Bool returns a field as a bool, accepting the forms strconv.ParseBool does plus
// the "on" and "off" that HTML checkboxes and their clients use.
func (v *Values) Bool(field string) bool {
	parsed, _ := v.parseBool(field)
	return parsed
}

// The parse helpers report whether this field yielded a usable value, so the Or
// accessors can fall back without consulting Err, which would also see errors
// recorded for other fields.

func (v *Values) parseInt(field string, bits int) (int64, bool) {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(raw, 10, bits)
	if err != nil {
		v.AddError(field, "must be a whole number")
		return 0, false
	}
	return parsed, true
}

func (v *Values) parseFloat(field string) (float64, bool) {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		v.AddError(field, "must be a number")
		return 0, false
	}
	return parsed, true
}

func (v *Values) parseBool(field string) (bool, bool) {
	raw := strings.TrimSpace(v.values.Get(field))
	if raw == "" {
		return false, false
	}
	switch {
	case strings.EqualFold(raw, "on"):
		return true, true
	case strings.EqualFold(raw, "off"):
		return false, true
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		v.AddError(field, "must be true or false")
		return false, false
	}
	return parsed, true
}

// StringOr returns a field value, or fallback when the field is absent or blank.
//
// The Or accessors exist because a field submitted empty is still present: an HTML
// form sends untouched inputs as "?page=", so pairing Has with a typed accessor
// would skip the default and then report the zero value as invalid.
func (v *Values) StringOr(field, fallback string) string {
	if strings.TrimSpace(v.values.Get(field)) == "" {
		return fallback
	}
	return v.values.Get(field)
}

// IntOr returns a field as an int, or fallback when it is absent or blank. A
// malformed value is still reported and yields fallback.
func (v *Values) IntOr(field string, fallback int) int {
	if parsed, ok := v.parseInt(field, strconv.IntSize); ok {
		return int(parsed)
	}
	return fallback
}

// Int64Or returns a field as an int64, or fallback when it is absent or blank.
func (v *Values) Int64Or(field string, fallback int64) int64 {
	if parsed, ok := v.parseInt(field, 64); ok {
		return parsed
	}
	return fallback
}

// Float64Or returns a field as a float64, or fallback when it is absent or blank.
func (v *Values) Float64Or(field string, fallback float64) float64 {
	if parsed, ok := v.parseFloat(field); ok {
		return parsed
	}
	return fallback
}

// BoolOr returns a field as a bool, or fallback when it is absent or blank.
func (v *Values) BoolOr(field string, fallback bool) bool {
	if parsed, ok := v.parseBool(field); ok {
		return parsed
	}
	return fallback
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
