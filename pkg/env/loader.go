package env

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Parse reads environment variables and fills the provided struct (must be a pointer).
// Supported tags:
//   - env: environment variable name
//   - envDefault: default value if env var is not set
//   - required: if "true", returns error when env var is missing and no default
//   - envSeparator: separator for slice values (default: ",")
func Parse(v any) error {
	ptrVal := reflect.ValueOf(v)
	if ptrVal.Kind() != reflect.Pointer || ptrVal.Elem().Kind() != reflect.Struct {
		return errors.New("env: argument must be a pointer to a struct")
	}

	return parseStruct(ptrVal.Elem())
}

// fieldConfig holds the parsed tag configuration for a struct field.
type fieldConfig struct {
	envKey       string
	defaultValue string
	required     bool
	separator    string
}

func parseStruct(val reflect.Value) error {
	typ := val.Type()

	for i := range val.NumField() {
		fieldVal := val.Field(i)
		structField := typ.Field(i)

		if err := parseField(fieldVal, structField); err != nil {
			return err
		}
	}
	return nil
}

func parseField(fieldVal reflect.Value, structField reflect.StructField) error {
	cfg := readFieldConfig(structField)

	// Check if it's a nested struct (even if it's a nil pointer).
	// We need to initialize it to traverse its fields recursively.
	// Note: time.Time is treated as a scalar value, not a struct to traverse.
	if isPotentialStruct(fieldVal) {
		fieldVal = ensurePointerInit(fieldVal)
		return parseStruct(fieldVal)
	}

	// If no "env" tag is present, skip the field
	if cfg.envKey == "" {
		return nil
	}

	// Resolve the value (Environment Variable > Default Value)
	valueStr, err := resolveValue(cfg)
	if err != nil {
		return err
	}

	// If no value found, leave the field as is (e.g., nil for pointers).
	if valueStr == "" {
		return nil
	}

	// Initialize pointer if necessary now that we have a value to set
	fieldVal = ensurePointerInit(fieldVal)

	// Set the value according to its type
	if err := setField(fieldVal, valueStr, cfg.separator); err != nil {
		return fmt.Errorf("env: error parsing field '%s' (%s): %w", structField.Name, cfg.envKey, err)
	}
	return nil
}

// ensurePointerInit initializes the pointer if it is nil and returns the underlying value (Elem).
func ensurePointerInit(fieldVal reflect.Value) reflect.Value {
	if fieldVal.Kind() == reflect.Pointer {
		if fieldVal.IsNil() {
			fieldVal.Set(reflect.New(fieldVal.Type().Elem()))
		}
		return fieldVal.Elem()
	}
	return fieldVal
}

// isPotentialStruct checks if the field is a struct (or pointer to struct) that should be traversed.
// It excludes time.Time as it's handled as a value type.
func isPotentialStruct(fieldVal reflect.Value) bool {
	t := fieldVal.Type()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]()
}

func readFieldConfig(structField reflect.StructField) fieldConfig {
	separator := structField.Tag.Get("envSeparator")
	if separator == "" {
		separator = ","
	}
	return fieldConfig{
		envKey:       structField.Tag.Get("env"),
		defaultValue: structField.Tag.Get("envDefault"),
		required:     structField.Tag.Get("required") == "true",
		separator:    separator,
	}
}

func resolveValue(cfg fieldConfig) (string, error) {
	valueStr := os.Getenv(cfg.envKey)
	if valueStr == "" && cfg.defaultValue != "" {
		valueStr = cfg.defaultValue
	}
	if valueStr == "" && cfg.required {
		return "", fmt.Errorf("env: required environment variable '%s' is not set", cfg.envKey)
	}
	return valueStr, nil
}

func setField(field reflect.Value, value, separator string) error {
	if !field.CanSet() {
		return nil // Private or non-settable field
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(value)

	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool: %w", err)
		}
		field.SetBool(b)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Special case for time.Duration
		if field.Type() == reflect.TypeFor[time.Duration]() {
			d, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid duration: %w", err)
			}
			field.SetInt(int64(d))
			return nil
		}

		i, err := strconv.ParseInt(value, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid int: %w", err)
		}
		field.SetInt(i)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(value, 10, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid uint: %w", err)
		}
		field.SetUint(u)

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(value, field.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid float: %w", err)
		}
		field.SetFloat(f)

	case reflect.Slice:
		return setSliceField(field, value, separator)

	default:
		return fmt.Errorf("unsupported type: %s", field.Kind())
	}
	return nil
}

func setSliceField(field reflect.Value, value, separator string) error {
	elemType := field.Type().Elem()
	elemKind := elemType.Kind()

	// Handle []byte separately (optimization to avoid string splitting)
	if elemKind == reflect.Uint8 {
		field.SetBytes([]byte(value))
		return nil
	}

	parts := splitAndTrim(value, separator)

	// Create a new slice of the correct type and size
	newSlice := reflect.MakeSlice(field.Type(), len(parts), len(parts))

	for i, part := range parts {
		// Get the element at index i
		item := newSlice.Index(i)

		// Recursively call setField for each element.
		// This enables automatic support for []time.Duration, []int16, []bool, etc.
		if err := setField(item, part, ""); err != nil {
			return fmt.Errorf("error parsing slice element %d: %w", i, err)
		}
	}

	field.Set(newSlice)
	return nil
}

func splitAndTrim(value, separator string) []string {
	parts := strings.Split(value, separator)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
