package moat

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errTrailing = errors.New("trailing data after the JSON value")

// The schema language is a subset of JSON Schema. A schema is a map with these
// keywords and no others:
//
//	type                  "object", "array", "string", "number", "integer", "boolean", "null", or a list of them
//	properties            map of property name to schema
//	required              list of property names
//	additionalProperties  true or false (default true)
//	items                 one schema for every element
//	enum                  list of allowed values
//	const                 the one allowed value
//	minLength, maxLength  string length in characters
//	pattern               regular expression (Go syntax), searched, not anchored
//	minimum, maximum      numbers, inclusive
//	minItems, maxItems    array length
//
// Any other keyword is a *ConfigError from New.
type schema struct {
	types    []string
	props    map[string]*schema
	required []string
	addl     *bool
	items    *schema
	enum     []any
	hasEnum  bool
	konst    any
	hasConst bool
	minLen   *int
	maxLen   *int
	pattern  *regexp.Regexp
	min, max *float64
	minItems *int
	maxItems *int
}

var knownTypes = map[string]bool{"object": true, "array": true, "string": true, "number": true, "integer": true, "boolean": true, "null": true}

func cfgErr(field, path, format string, a ...any) error {
	return &ConfigError{Field: field, Path: path, Detail: fmt.Sprintf(format, a...)}
}

func join(path, seg string) string {
	if path == "" {
		return seg
	}
	return path + "." + seg
}

func compileSchema(m map[string]any, field string) (*schema, error) {
	return compile(m, field, "")
}

func compile(m map[string]any, field, path string) (*schema, error) {
	s := &schema{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		here := join(path, k)
		switch k {
		case "type":
			switch t := v.(type) {
			case string:
				s.types = []string{t}
			case []string:
				s.types = t
			case []any:
				for _, e := range t {
					es, ok := e.(string)
					if !ok {
						return nil, cfgErr(field, here, "type must be a string or a list of strings")
					}
					s.types = append(s.types, es)
				}
			default:
				return nil, cfgErr(field, here, "type must be a string or a list of strings")
			}
			if len(s.types) == 0 {
				return nil, cfgErr(field, here, "type must not be empty")
			}
			for _, t := range s.types {
				if !knownTypes[t] {
					return nil, cfgErr(field, here, "unknown type %q", t)
				}
			}
		case "properties":
			pm, ok := asSchemaMap(v)
			if !ok {
				return nil, cfgErr(field, here, "properties must be a map of schemas")
			}
			s.props = map[string]*schema{}
			names := make([]string, 0, len(pm))
			for name := range pm {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				sub, ok := pm[name].(map[string]any)
				if !ok {
					return nil, cfgErr(field, join(here, name), "a schema must be a map")
				}
				c, err := compile(sub, field, join(here, name))
				if err != nil {
					return nil, err
				}
				s.props[name] = c
			}
		case "required":
			switch r := v.(type) {
			case []string:
				s.required = r
			case []any:
				for _, e := range r {
					es, ok := e.(string)
					if !ok {
						return nil, cfgErr(field, here, "required must be a list of strings")
					}
					s.required = append(s.required, es)
				}
			default:
				return nil, cfgErr(field, here, "required must be a list of strings")
			}
		case "additionalProperties":
			b, ok := v.(bool)
			if !ok {
				return nil, cfgErr(field, here, "additionalProperties must be true or false")
			}
			s.addl = &b
		case "items":
			sub, ok := v.(map[string]any)
			if !ok {
				return nil, cfgErr(field, here, "items must be one schema")
			}
			c, err := compile(sub, field, here)
			if err != nil {
				return nil, err
			}
			s.items = c
		case "enum":
			l, ok := toList(v)
			if !ok || len(l) == 0 {
				return nil, cfgErr(field, here, "enum must be a non-empty list")
			}
			for _, e := range l {
				n, err := normalizeValue(e)
				if err != nil {
					return nil, cfgErr(field, here, "enum holds a value that is not JSON")
				}
				s.enum = append(s.enum, n)
			}
			s.hasEnum = true
		case "const":
			n, err := normalizeValue(v)
			if err != nil {
				return nil, cfgErr(field, here, "const is not a JSON value")
			}
			s.konst, s.hasConst = n, true
		case "minLength", "maxLength", "minItems", "maxItems":
			f, ok := toFloat(v)
			if !ok || f < 0 || f != math.Trunc(f) {
				return nil, cfgErr(field, here, "%s must be a whole number, zero or more", k)
			}
			n := int(f)
			switch k {
			case "minLength":
				s.minLen = &n
			case "maxLength":
				s.maxLen = &n
			case "minItems":
				s.minItems = &n
			default:
				s.maxItems = &n
			}
		case "pattern":
			p, ok := v.(string)
			if !ok {
				return nil, cfgErr(field, here, "pattern must be a string")
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, cfgErr(field, here, "pattern does not compile: %v", err)
			}
			s.pattern = re
		case "minimum", "maximum":
			f, ok := toFloat(v)
			if !ok {
				return nil, cfgErr(field, here, "%s must be a number", k)
			}
			if k == "minimum" {
				s.min = &f
			} else {
				s.max = &f
			}
		default:
			return nil, cfgErr(field, here, "the keyword %q is not supported", k)
		}
	}
	return s, nil
}

func asSchemaMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func toList(v any) ([]any, bool) {
	if l, ok := v.([]any); ok {
		return l, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

func toFloat(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// normalizeValue turns a Go value into the generic JSON form validation sees.
func normalizeValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return parseJSON(raw)
}

// stringOnly reports whether the schema asks for a string and nothing else.
func (s *schema) stringOnly() bool {
	return len(s.types) == 1 && s.types[0] == "string"
}

func typeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func at(path string) string {
	if path == "" {
		return "the value"
	}
	return path
}

func equalJSON(a, b any) bool { return reflect.DeepEqual(a, b) }

// validate returns "" when v meets the schema, else a short description of the
// first problem. It never repeats the value.
func (s *schema) validate(v any, path string) string {
	if len(s.types) > 0 {
		tn := typeName(v)
		ok := false
		for _, t := range s.types {
			if t == tn || (t == "number" && tn == "integer") {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf("%s must be of type %s", at(path), strings.Join(s.types, " or "))
		}
	}
	if s.hasConst && !equalJSON(v, s.konst) {
		return at(path) + " must equal the constant"
	}
	if s.hasEnum {
		found := false
		for _, e := range s.enum {
			if equalJSON(v, e) {
				found = true
				break
			}
		}
		if !found {
			return at(path) + " must be one of the allowed values"
		}
	}
	switch x := v.(type) {
	case string:
		n := utf8.RuneCountInString(x)
		if s.minLen != nil && n < *s.minLen {
			return fmt.Sprintf("%s must be at least %d characters", at(path), *s.minLen)
		}
		if s.maxLen != nil && n > *s.maxLen {
			return fmt.Sprintf("%s must be at most %d characters", at(path), *s.maxLen)
		}
		if s.pattern != nil && !s.pattern.MatchString(x) {
			return at(path) + " does not match the pattern"
		}
	case float64:
		if s.min != nil && x < *s.min {
			return fmt.Sprintf("%s must be at least %s", at(path), strconv.FormatFloat(*s.min, 'g', -1, 64))
		}
		if s.max != nil && x > *s.max {
			return fmt.Sprintf("%s must be at most %s", at(path), strconv.FormatFloat(*s.max, 'g', -1, 64))
		}
	case []any:
		if s.minItems != nil && len(x) < *s.minItems {
			return fmt.Sprintf("%s must have at least %d items", at(path), *s.minItems)
		}
		if s.maxItems != nil && len(x) > *s.maxItems {
			return fmt.Sprintf("%s must have at most %d items", at(path), *s.maxItems)
		}
		if s.items != nil {
			for i, e := range x {
				if d := s.items.validate(e, path+"["+strconv.Itoa(i)+"]"); d != "" {
					return d
				}
			}
		}
	case map[string]any:
		for _, r := range s.required {
			if _, ok := x[r]; !ok {
				return fmt.Sprintf("%s is missing required property %q", at(path), r)
			}
		}
		names := make([]string, 0, len(x))
		for k := range x {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if sub, ok := s.props[k]; ok {
				if d := sub.validate(x[k], join(path, k)); d != "" {
					return d
				}
			} else if s.addl != nil && !*s.addl {
				return at(path) + " has a property the schema does not allow"
			}
		}
	}
	return ""
}
