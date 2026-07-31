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
// Fields opt in with `env:"KEY"`. The optional `default` tag supplies a fallback,
// and `required:"true"` rejects missing or empty values.
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

		if envKey == "" && fieldValue.Kind() == reflect.Struct && fieldValue.Type() != typeOfDuration {
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
func setConfigURL(value reflect.Value, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
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

	// []byte is the raw value, not a list of numbers: it holds a key or a secret, and
	// splitting one on commas would corrupt it. byte is an alias for uint8, so this
	// necessarily covers []uint8 as well — the two are one type, and a list of small
	// numbers needs a wider element type.
	if element == typeOfByte {
		value.SetBytes([]byte(raw))
		return nil
	}
	if element.Kind() == reflect.Slice || element.Kind() == reflect.Map {
		return fmt.Errorf("unsupported field type %s", value.Type())
	}

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
