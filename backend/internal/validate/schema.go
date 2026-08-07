// Package validate implements a minimal JSON Schema subset used to validate
// command arguments and API payloads. Supported keywords: type, required,
// enum, additionalProperties, properties, minimum, maximum, minLength,
// maxLength. No third-party schema library is used.
package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Schema is the minimal schema model understood by the validator.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Enum                 []json.RawMessage  `json:"enum,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
}

// Parse decodes a raw schema document.
func Parse(data []byte) (*Schema, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("schema is empty")
	}
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	return &s, nil
}

// Validate checks value against the schema. value must be a decoded JSON value
// (map[string]any, []any, string, float64, bool, nil).
func (s *Schema) Validate(value any) error {
	if s == nil {
		return nil
	}
	return s.validate("$", value)
}

func (s *Schema) validate(path string, value any) error {
	if s.Type != "" {
		if err := checkType(path, s.Type, value); err != nil {
			return err
		}
	}
	switch s.Type {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected object, got %T", path, value)
		}
		// required
		for _, key := range s.Required {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("%s: missing required property %q", path, key)
			}
		}
		// additionalProperties
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			for key := range obj {
				if _, known := s.Properties[key]; !known {
					return fmt.Errorf("%s: additional property %q not allowed", path, key)
				}
			}
		}
		for key, sub := range s.Properties {
			if sub == nil {
				continue
			}
			if val, ok := obj[key]; ok {
				if err := sub.validate(path+"."+key, val); err != nil {
					return err
				}
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected array, got %T", path, value)
		}
		for i, item := range arr {
			if err := s.validate(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
				return err
			}
		}
	case "string":
		str, ok := value.(string)
		if !ok {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected string, got %T", path, value)
		}
		if s.MinLength != nil && len(str) < *s.MinLength {
			return fmt.Errorf("%s: string shorter than minLength %d", path, *s.MinLength)
		}
		if s.MaxLength != nil && len(str) > *s.MaxLength {
			return fmt.Errorf("%s: string longer than maxLength %d", path, *s.MaxLength)
		}
	case "integer":
		f, ok := asFloat(value)
		if !ok || math.Trunc(f) != f {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected integer, got %v", path, value)
		}
		if err := checkBounds(path, f, s); err != nil {
			return err
		}
	case "number":
		f, ok := asFloat(value)
		if !ok {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected number, got %v", path, value)
		}
		if err := checkBounds(path, f, s); err != nil {
			return err
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			if value == nil {
				return nil
			}
			return fmt.Errorf("%s: expected boolean, got %T", path, value)
		}
	}
	if len(s.Enum) > 0 {
		for _, raw := range s.Enum {
			var e any
			if err := json.Unmarshal(raw, &e); err == nil {
				if jsonEqual(e, value) {
					return nil
				}
			}
		}
		return fmt.Errorf("%s: value not in enum", path)
	}
	return nil
}

func checkType(path, typ string, value any) error {
	switch typ {
	case "string":
		if _, ok := value.(string); ok {
			return nil
		}
	case "boolean":
		if _, ok := value.(bool); ok {
			return nil
		}
	case "integer", "number":
		if _, ok := asFloat(value); ok {
			return nil
		}
	case "object":
		if _, ok := value.(map[string]any); ok {
			return nil
		}
	case "array":
		if _, ok := value.([]any); ok {
			return nil
		}
	case "null":
		if value == nil {
			return nil
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, typ)
	}
	if value == nil {
		return nil
	}
	return fmt.Errorf("%s: expected %s, got %T", path, typ, value)
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

func checkBounds(path string, f float64, s *Schema) error {
	if s.Minimum != nil && f < *s.Minimum {
		return fmt.Errorf("%s: value %v below minimum %v", path, f, *s.Minimum)
	}
	if s.Maximum != nil && f > *s.Maximum {
		return fmt.Errorf("%s: value %v above maximum %v", path, f, *s.Maximum)
	}
	return nil
}

func jsonEqual(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ab) == string(bb)
}
