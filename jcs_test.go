package k9o

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"
)

type jcsCase struct {
	Name            string          `json:"name"`
	Input           json.RawMessage `json:"input"`
	ExpectCanonical *string         `json:"expectCanonical"`
	ExpectError     string          `json:"expectError"`
}

func TestJCSSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcs_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []jcsCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) != 25 {
		t.Fatalf("want 25 cases, got %d", len(file.Cases))
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := CanonicalJSON(c.Input)
			switch {
			case c.ExpectCanonical != nil:
				if err != nil || got != *c.ExpectCanonical {
					t.Fatalf("got %q (%v), want %q", got, err, *c.ExpectCanonical)
				}
			case c.ExpectError != "":
				if err == nil {
					t.Fatalf("want an error (%s), got %q", c.ExpectError, got)
				}
			default:
				t.Fatal("case has neither expectCanonical nor expectError")
			}
		})
	}
}

func TestJCSGoValues(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"b": []any{1, 2.0, "x<y"}, "a": nil, "c": true})
	if err != nil || got != `{"a":null,"b":[1,2,"x<y"],"c":true}` {
		t.Fatalf("got %q %v", got, err)
	}
	for name, v := range map[string]any{
		"NaN":        math.NaN(),
		"Infinity":   map[string]any{"x": math.Inf(1)},
		"fraction":   map[string]any{"n": 1.5},
		"2^53":       map[string]any{"n": int64(9007199254740992)},
		"float 2^60": 1.152921504606847e18,
		"exponent":   json.RawMessage(`1e400`),
	} {
		if _, err := CanonicalJSON(v); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	for in, want := range map[string]string{"-0": "0", "-0.0": "0", "1.0": "1", "1e2": "100", "9007199254740991": "9007199254740991"} {
		if got, err := CanonicalJSON(json.RawMessage(in)); err != nil || got != want {
			t.Errorf("%s: got %q (%v), want %q", in, got, err, want)
		}
	}
}

func TestJCSKeyOrderIsUTF8NotUTF16(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"\U0001F600": 1, "ﬀ": 2})
	want := "{\"ﬀ\":2,\"\U0001F600\":1}"
	if err != nil || got != want {
		t.Fatalf("got %q (%v), want %q", got, err, want)
	}
	if bytes.Contains([]byte(got), []byte(`\u`)) {
		t.Fatal("non-ASCII must be written literally")
	}
}
