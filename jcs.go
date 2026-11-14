package k9o

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// jcsMaxSafeInteger is 2^53-1, the largest absolute value the profile accepts.
const jcsMaxSafeInteger = 9007199254740991

// CanonicalJSON returns the canonical form of v under the platform's code-point
// profile: object keys sorted by UTF-8 byte order (the same as Unicode code
// point order), no whitespace, strings escaped minimally (a backslash before
// '"' and '\', the short escapes \b \t \n \f \r, every other control
// character below U+0020 as a lowercase \u00xx, '/' and every other code point
// written as is), and numbers as base-10 integers only.
//
// v is anything encoding/json can marshal, or a json.RawMessage. A number that
// is fractional, or whose absolute value is above 9007199254740991 (2^53-1), is
// an error, as is NaN or an infinity: the profile fails closed rather than
// round. Send such values as strings.
func CanonicalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := jcsWrite(&sb, parsed); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func jcsWrite(sb *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case string:
		jcsString(sb, x)
	case json.Number:
		s, err := jcsInteger(x)
		if err != nil {
			return err
		}
		sb.WriteString(s)
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := jcsWrite(sb, e); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Go compares strings bytewise: UTF-8 byte order
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			jcsString(sb, k)
			sb.WriteByte(':')
			if err := jcsWrite(sb, x[k]); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON: unsupported value of type %T", v)
	}
	return nil
}

func jcsString(sb *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteByte(hex[r>>4])
			sb.WriteByte(hex[r&0xf])
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

// jcsInteger writes a JSON number as a base-10 integer, or fails closed.
func jcsInteger(n json.Number) (string, error) {
	lit := string(n)
	if !strings.ContainsAny(lit, ".eE") {
		v, err := strconv.ParseInt(lit, 10, 64)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				return "", fmt.Errorf("canonical JSON: integer %s is above the 2^53-1 ceiling", lit)
			}
			return "", err
		}
		return jcsFormatInt(v, lit)
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return "", err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("canonical JSON: NaN and infinity cannot be represented")
	}
	if f != math.Trunc(f) {
		return "", fmt.Errorf("canonical JSON: %s is not an integer", lit)
	}
	if math.Abs(f) > jcsMaxSafeInteger {
		return "", fmt.Errorf("canonical JSON: %s is above the 2^53-1 ceiling", lit)
	}
	return jcsFormatInt(int64(f), lit)
}

func jcsFormatInt(v int64, lit string) (string, error) {
	if v > jcsMaxSafeInteger || v < -jcsMaxSafeInteger {
		return "", fmt.Errorf("canonical JSON: integer %s is above the 2^53-1 ceiling", lit)
	}
	return strconv.FormatInt(v, 10), nil // -0 is 0
}
