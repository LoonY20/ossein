package ossein

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// EnvLookup resolves an environment variable by key.
type EnvLookup func(string) (string, bool)

// LoadConfig loads a typed configuration struct from environment variables.
//
// Fields opt in with `env:"KEY"`. The optional `default` tag supplies a fallback, and
// `required:"true"` rejects a missing or empty value — and, for a list, one that parses
// to no entries. Every field is attempted, so one call reports every problem rather
// than the first.
//
// Supported field types are strings, booleans, signed and unsigned integers, floats,
// time.Duration, url.URL and *url.URL, any type implementing encoding.TextUnmarshaler
// (slog.Level, net/netip addresses, net.IP, time.Time, and application types), and
// slices of any of those from a comma-separated value. List entries are trimmed and
// empty ones dropped, so a trailing comma is not an entry. []byte is the raw value
// rather than a list, since it holds a key or a secret; byte is an alias for uint8, so
// []uint8 is the same type and behaves the same way. A nested struct without an env tag
// is a group of settings, which means a self-parsing struct type such as time.Time
// needs its tag: without one it is descended into and its own parsing never runs.
// Maps are not supported.
func LoadConfig[T any]() (T, error) {
	return LoadConfigFromEnv[T](os.LookupEnv)
}

// LoadConfigFromEnv is LoadConfig with a replaceable environment lookup function.
// It is useful for tests and custom environment sources.
func LoadConfigFromEnv[T any](lookup EnvLookup) (T, error) {
	var config T
	if lookup == nil {
		lookup = os.LookupEnv
	}

	value := reflect.ValueOf(&config).Elem()
	if value.Kind() != reflect.Struct {
		return config, errors.New("ossein: config target must be a struct")
	}

	if err := loadConfigStruct(value, lookup, ""); err != nil {
		return config, err
	}

	return config, nil
}

func loadConfigStruct(value reflect.Value, lookup EnvLookup, path string) error {
	var configErrors []error
	typeInfo := value.Type()

	for i := 0; i < value.NumField(); i++ {
		fieldInfo := typeInfo.Field(i)
		fieldValue := value.Field(i)

		if !fieldValue.CanSet() {
			continue
		}

		fieldPath := fieldInfo.Name
		if path != "" {
			fieldPath = path + "." + fieldPath
		}

		envKey := fieldInfo.Tag.Get("env")
		if envKey == "-" {
			continue
		}

		// A struct with no env tag is a nested group of settings. time.Duration needed
		// excluding here once; it never did, since its kind is Int64, not Struct.
		if envKey == "" && fieldValue.Kind() == reflect.Struct {
			if err := loadConfigStruct(fieldValue, lookup, fieldPath); err != nil {
				configErrors = append(configErrors, err)
			}
			continue
		}

		if envKey == "" {
			continue
		}

		raw, found := lookup(envKey)
		if !found {
			if fallback, ok := fieldInfo.Tag.Lookup("default"); ok {
				raw = fallback
				found = true
			}
		}

		required := fieldInfo.Tag.Get("required") == "true"
		if !found || (required && raw == "") {
			if required {
				configErrors = append(configErrors, fmt.Errorf("ossein: config %s (%s) is required", fieldPath, envKey))
			}
			continue
		}

		if err := setConfigValue(fieldValue, raw); err != nil {
			configErrors = append(configErrors, fmt.Errorf("ossein: config %s (%s): %w", fieldPath, envKey, err))
			continue
		}

		// A list is the one type where a non-empty value can still parse to nothing:
		// ",," is not empty, so the check above passes, and an allowlist that required
		// entries would then load with none. Checked after parsing because that is the
		// only place the outcome is known.
		if required && fieldValue.Kind() == reflect.Slice && fieldValue.Len() == 0 {
			configErrors = append(configErrors, fmt.Errorf(
				"ossein: config %s (%s) is required, but %q contains no entries",
				fieldPath, envKey, raw,
			))
		}
	}

	return errors.Join(configErrors...)
}

// Types the loader parses itself rather than through a general mechanism.
var (
	typeOfDuration = reflect.TypeOf(time.Duration(0))
	typeOfURL      = reflect.TypeOf(url.URL{})
	typeOfByte     = reflect.TypeOf(byte(0))
)

func setConfigValue(value reflect.Value, raw string) error {
	// url.URL is special-cased for the same reason time.Duration is: it is a
	// ubiquitous configuration type, and it implements BinaryUnmarshaler rather than
	// TextUnmarshaler, so the general mechanism below does not reach it. url.Parse
	// also reports a bad value better than a byte decoder would.
	if value.Type() == typeOfURL || value.Type() == reflect.PointerTo(typeOfURL) {
		return setConfigURL(value, raw)
	}

	// The general escape hatch, checked before the kind switch so a named type parses
	// itself rather than being assigned as its underlying string or integer.
	if unmarshaler, ok := textUnmarshalerFor(value); ok {
		if err := unmarshaler.UnmarshalText([]byte(raw)); err != nil {
			return fmt.Errorf("invalid %s %q: %w", value.Type(), raw, err)
		}
		return nil
	}

	if value.Type() == typeOfDuration {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		value.SetInt(int64(parsed))
		return nil
	}

	switch value.Kind() {
	case reflect.String:
		value.SetString(raw)
		return nil
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid boolean %q: %w", raw, err)
		}
		value.SetBool(parsed)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(raw, 10, value.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", raw, err)
		}
		value.SetInt(parsed)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		parsed, err := strconv.ParseUint(raw, 10, value.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid unsigned integer %q: %w", raw, err)
		}
		value.SetUint(parsed)
		return nil
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(raw, value.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid number %q: %w", raw, err)
		}
		value.SetFloat(parsed)
		return nil
	case reflect.Slice:
		return setConfigSlice(value, raw)
	default:
		return fmt.Errorf("unsupported field type %s", value.Type())
	}
}

// setConfigURL parses a URL field, allocating a pointer one.
//
// Two values url.Parse accepts are rejected here, because both produce a URL that is
// silently useless rather than one that fails where it is used. An empty value yields
// a URL with nothing in it, and "localhost:8080" — the shape of a default with its
// scheme dropped — parses as an opaque URL whose scheme is "localhost", which
// JoinPath and ResolveReference then refuse to extend.
func setConfigURL(value reflect.Value, raw string) error {
	if raw == "" {
		return errors.New("a URL is required, but the value is empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if parsed.Opaque != "" {
		return fmt.Errorf(
			"invalid URL %q: %q is read as a scheme, so the value has no host; "+
				"write it as a full URL such as %q",
			raw, parsed.Scheme, "http://"+raw,
		)
	}
	if value.Kind() == reflect.Pointer {
		value.Set(reflect.ValueOf(parsed))
		return nil
	}
	value.Set(reflect.ValueOf(*parsed))
	return nil
}

// setConfigSlice parses a comma-separated list. Entries are trimmed, and empty ones
// are dropped, so a trailing comma or a value spaced out for readability is not an
// error and does not become a phantom empty element.
func setConfigSlice(value reflect.Value, raw string) error {
	element := value.Type().Elem()

	// An element that parses itself is decided before the rules below, which go by
	// kind: net.IP is a []byte that implements TextUnmarshaler, so judging by kind
	// first would reject a list of trusted proxies as a nested list.
	if _, selfParsing := textUnmarshalerFor(reflect.New(element).Elem()); !selfParsing {
		// []byte is the raw value, not a list of numbers: it holds a key or a secret,
		// and splitting one on commas would corrupt it. byte is an alias for uint8, so
		// this necessarily covers []uint8 as well — the two are one type, and a list of
		// small numbers needs a wider element type.
		if element == typeOfByte {
			value.SetBytes([]byte(raw))
			return nil
		}
		// A nested list has no second separator to split on, so every element would
		// otherwise receive the whole value.
		if element.Kind() == reflect.Slice {
			return fmt.Errorf("unsupported field type %s", value.Type())
		}
	}

	// Positions count the fields as written, so the number in the error matches what
	// an operator sees in the value rather than the entries that survived trimming.
	parts := strings.Split(raw, ",")
	parsed := reflect.MakeSlice(value.Type(), 0, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		item := reflect.New(element).Elem()
		if err := setConfigValue(item, part); err != nil {
			return fmt.Errorf("element %d: %w", index+1, err)
		}
		parsed = reflect.Append(parsed, item)
	}

	value.Set(parsed)
	return nil
}

// textUnmarshalerFor returns the field as an encoding.TextUnmarshaler when its type
// implements one, allocating a pointer field first.
//
// This is the general escape hatch: it covers net/netip addresses, slog.Level,
// time.Time, net.IP, and any application type that can parse itself, without the
// loader knowing about any of them.
func textUnmarshalerFor(value reflect.Value) (encoding.TextUnmarshaler, bool) {
	if value.Kind() == reflect.Pointer {
		if _, ok := reflect.New(value.Type().Elem()).Interface().(encoding.TextUnmarshaler); !ok {
			return nil, false
		}
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		unmarshaler, ok := value.Interface().(encoding.TextUnmarshaler)
		return unmarshaler, ok
	}

	// Addressable by construction: a struct field reached through reflection is, and
	// so is a slice element built with reflect.New.
	unmarshaler, ok := value.Addr().Interface().(encoding.TextUnmarshaler)
	return unmarshaler, ok
}
