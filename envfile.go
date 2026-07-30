package ossein

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var envFileKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadEnvFile loads KEY=VALUE entries into the process environment.
//
// Existing environment variables take precedence over file values. Blank
// lines and comments are ignored. Values may be unquoted, single-quoted, or
// double-quoted.
func LoadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("ossein env: open %s: %w", path, err)
	}
	defer file.Close()

	values, err := parseEnv(file)
	if err != nil {
		return fmt.Errorf("ossein env: parse %s: %w", path, err)
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("ossein env: set %s: %w", key, err)
		}
	}
	return nil
}

// LoadEnvFileIfExists loads an environment file when it exists.
func LoadEnvFileIfExists(path string) error {
	err := LoadEnvFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func parseEnv(reader io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, rawValue, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !envFileKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: invalid assignment", lineNumber)
		}
		value, err := parseEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '"':
		end := -1
		escaped := false
		for index := 1; index < len(value); index++ {
			switch {
			case escaped:
				escaped = false
			case value[index] == '\\':
				escaped = true
			case value[index] == '"':
				end = index
				index = len(value)
			}
		}
		if end == -1 || !validEnvRemainder(value[end+1:]) {
			return "", errors.New("invalid double-quoted value")
		}
		parsed, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", errors.New("invalid double-quoted value")
		}
		return parsed, nil
	case '\'':
		end := strings.IndexByte(value[1:], '\'')
		if end == -1 {
			return "", errors.New("invalid single-quoted value")
		}
		end++
		if !validEnvRemainder(value[end+1:]) {
			return "", errors.New("invalid single-quoted value")
		}
		return value[1:end], nil
	default:
		for index := 1; index < len(value); index++ {
			if value[index] == '#' && (value[index-1] == ' ' || value[index-1] == '\t') {
				return strings.TrimSpace(value[:index]), nil
			}
		}
		return value, nil
	}
}

func validEnvRemainder(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.HasPrefix(value, "#")
}
