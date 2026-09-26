package scripts

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// A small JSON Schema validator for function inputs (§4.15.2): the subset
// tool schemas use in practice — type, properties, required,
// additionalProperties, items, enum, pattern, min/maxLength, minimum/maximum.
// Anything else in a schema (description, default, examples, …) is accepted
// and ignored. No dependency: a full JSON Schema library would dwarf the rest
// of the package for keywords no tool uses.

type schema struct {
	Type                 any                `json:"type"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Items                *schema            `json:"items"`
	Enum                 []any              `json:"enum"`
	Pattern              string             `json:"pattern"`
	MinLength            *int               `json:"minLength"`
	MaxLength            *int               `json:"maxLength"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`

	re *regexp.Regexp
}

// parseSchema compiles a function's input schema. The top level must describe
// an object: tool arguments are always a JSON object.
func parseSchema(raw json.RawMessage) (*schema, error) {
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if !s.allows("object") || len(s.types()) != 1 {
		return nil, errors.New(`the top level must be {"type": "object", …}`)
	}
	return &s, s.compile("")
}

func (s *schema) compile(path string) error {
	for _, t := range s.types() {
		switch t {
		case "object", "array", "string", "number", "integer", "boolean", "null":
		default:
			return fmt.Errorf("%s: unknown type %q", where(path), t)
		}
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return fmt.Errorf("%s: pattern: %w", where(path), err)
		}
		s.re = re
	}
	for name, p := range s.Properties {
		if p == nil {
			return fmt.Errorf("%s: property %s has no schema", where(path), name)
		}
		if err := p.compile(path + "." + name); err != nil {
			return err
		}
	}
	if s.Items != nil {
		if err := s.Items.compile(path + "[]"); err != nil {
			return err
		}
	}
	return nil
}

func where(path string) string {
	if path == "" {
		return "input"
	}
	return "input" + path
}

func (s *schema) types() []string {
	switch t := s.Type.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func (s *schema) allows(t string) bool {
	ts := s.types()
	if len(ts) == 0 {
		return true
	}
	for _, x := range ts {
		if x == t || (x == "number" && t == "integer") {
			return true
		}
	}
	return false
}

// validate checks v (as decoded by encoding/json) against s.
func (s *schema) validate(v any, path string) error {
	switch x := v.(type) {
	case map[string]any:
		if !s.allows("object") {
			return fmt.Errorf("%s must be %s", where(path), strings.Join(s.types(), " or "))
		}
		for _, r := range s.Required {
			if _, ok := x[r]; !ok {
				return fmt.Errorf("%s.%s is required", where(path), r)
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, ok := s.Properties[k]
			if !ok {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("%s.%s is not an accepted field", where(path), k)
				}
				continue
			}
			if err := p.validate(x[k], path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		if !s.allows("array") {
			return fmt.Errorf("%s must be %s", where(path), strings.Join(s.types(), " or "))
		}
		if s.Items != nil {
			for i, item := range x {
				if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		if !s.allows("string") {
			return fmt.Errorf("%s must be %s", where(path), strings.Join(s.types(), " or "))
		}
		n := len([]rune(x))
		if s.MinLength != nil && n < *s.MinLength {
			return fmt.Errorf("%s is shorter than %d", where(path), *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return fmt.Errorf("%s is longer than %d", where(path), *s.MaxLength)
		}
		if s.re != nil && !s.re.MatchString(x) {
			return fmt.Errorf("%s doesn't match %s", where(path), s.Pattern)
		}
	case float64:
		isInt := x == math.Trunc(x)
		if !(s.allows("number") || (isInt && s.allows("integer"))) {
			return fmt.Errorf("%s must be %s", where(path), strings.Join(s.types(), " or "))
		}
		if s.Minimum != nil && x < *s.Minimum {
			return fmt.Errorf("%s is below %v", where(path), *s.Minimum)
		}
		if s.Maximum != nil && x > *s.Maximum {
			return fmt.Errorf("%s is above %v", where(path), *s.Maximum)
		}
	case bool:
		if !s.allows("boolean") {
			return fmt.Errorf("%s must be %s", where(path), strings.Join(s.types(), " or "))
		}
	case nil:
		if !s.allows("null") {
			return fmt.Errorf("%s must not be null", where(path))
		}
	}
	if len(s.Enum) > 0 {
		for _, e := range s.Enum {
			if equalJSON(e, v) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of the allowed values", where(path))
	}
	return nil
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ValidateInput checks a function call's input against its schema. A
// function without a schema takes any object.
func ValidateInput(f Function, input json.RawMessage) error {
	var v any
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if err := json.Unmarshal(input, &v); err != nil {
		return fmt.Errorf("input is not JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return errors.New("input must be a JSON object")
	}
	if len(f.Input) == 0 {
		return nil
	}
	s, err := parseSchema(f.Input)
	if err != nil {
		return err
	}
	return s.validate(v, "")
}
