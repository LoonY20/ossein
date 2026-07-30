package ossein

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
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
	typeOfDuration := reflect.TypeOf(time.Duration(0))
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

func setConfigValue(value reflect.Value, raw string) error {
	if value.Type() == reflect.TypeOf(time.Duration(0)) {
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
	default:
		return fmt.Errorf("unsupported field type %s", value.Type())
	}
}
