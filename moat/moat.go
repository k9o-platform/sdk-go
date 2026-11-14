// Package moat pins the system prompt of a model call and checks what goes in
// and what comes out, in your own process. It makes no network call, needs no
// account and imports nothing outside the standard library.
//
// A Moat does four things:
//
//  1. It builds the message list itself. Your pinned prompt is the only
//     "system" message; untrusted input is always a "user" message, and input
//     that carries a role or system marker is refused.
//  2. It validates the untrusted input against a declared schema.
//  3. It validates the model output against a declared schema.
//  4. It runs an optional filter over the model output for personal data.
//
// The pin is structural. It stops injected text from becoming the system
// prompt; it does not stop that text from reaching the model, and the model
// may still follow it. Moat refuses what it can recognise and checks the shape
// of what comes back. It is one layer, not a guarantee.
//
// A refusal is a *Refusal value (it also implements error), not a panic. Its
// Reason is one of RoleMarker, InputSchema, OutputSchema and PII.
package moat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// Refusal reasons: the closed set of Refusal.Reason.
const (
	// RoleMarker: the input carries a role or system marker.
	RoleMarker = "role_marker"
	// InputSchema: the input does not meet the input schema.
	InputSchema = "input_schema"
	// OutputSchema: the output is not valid JSON where the schema needs it, or does not meet the output schema.
	OutputSchema = "output_schema"
	// PII: the PII filter reported personal data in the output.
	PII = "pii"
)

// Refusal is returned when Messages or CheckOutput refuses. Detail is short
// and never repeats the input or the output.
type Refusal struct {
	Reason string
	Detail string
}

func (r *Refusal) Error() string { return "moat refused (" + r.Reason + "): " + r.Detail }

// Message is one entry of the message list.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// PIIFilter inspects model output text and reports whether it holds personal
// data. found true refuses the output with reason "pii"; detail is a short
// label for the refusal (do not put the data in it).
//
// A Barbican instance fits:
//
//	func(text string) (string, bool) {
//		v := b.Inspect(text, nil)
//		return v.Reason, v.Decision != "allow"
//	}
type PIIFilter func(text string) (detail string, found bool)

// Filters are the optional output filters.
type Filters struct {
	// PII is run over the model output. Nil means no PII step.
	PII PIIFilter
}

// Options configure New.
type Options struct {
	// Prompt is the pinned system prompt. Required and non-empty.
	Prompt string
	// InputSchema is a schema in the supported subset (see the package README
	// and ConfigError). Nil means no input check beyond the markers.
	InputSchema map[string]any
	// OutputSchema is a schema in the supported subset. Nil means any output.
	OutputSchema map[string]any
	// Filters are the optional output filters.
	Filters Filters
}

// ConfigError is returned by New for an unusable prompt or schema, so that a
// typo never silently disables a check.
type ConfigError struct {
	// Field is "prompt", "inputSchema" or "outputSchema".
	Field string
	// Path locates the problem inside the schema, for example "properties.name".
	Path string
	// Detail says what is wrong.
	Detail string
}

func (e *ConfigError) Error() string {
	if e.Path != "" {
		return "moat: " + e.Field + " at " + e.Path + ": " + e.Detail
	}
	return "moat: " + e.Field + ": " + e.Detail
}

// Moat is a configured guard. It is safe for concurrent use.
type Moat struct {
	prompt string
	in     *schema
	out    *schema
	pii    PIIFilter
}

// New builds a Moat. A schema keyword outside the supported subset, or a
// malformed one, is a *ConfigError.
func New(opts Options) (*Moat, error) {
	if strings.TrimSpace(opts.Prompt) == "" {
		return nil, &ConfigError{Field: "prompt", Detail: "must be a non-empty string"}
	}
	m := &Moat{prompt: opts.Prompt, pii: opts.Filters.PII}
	var err error
	if opts.InputSchema != nil {
		if m.in, err = compileSchema(opts.InputSchema, "inputSchema"); err != nil {
			return nil, err
		}
	}
	if opts.OutputSchema != nil {
		if m.out, err = compileSchema(opts.OutputSchema, "outputSchema"); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// PromptSHA256 returns the lowercase hex SHA-256 of the pinned prompt, for
// example to pass along with a call so it can be matched to this prompt later.
func (m *Moat) PromptSHA256() string {
	sum := sha256.Sum256([]byte(m.prompt))
	return hex.EncodeToString(sum[:])
}

// Messages returns the message list for one call: the pinned prompt as the
// only "system" message, then the untrusted input as one "user" message. A
// string input is sent as it is; any other value is sent as compact JSON.
//
// The input is refused first for a role or system marker, then for the input
// schema (a string input is validated as a string).
func (m *Moat) Messages(untrusted any) ([]Message, *Refusal) {
	value, text, err := normalizeInput(untrusted)
	if err != nil {
		return nil, &Refusal{Reason: InputSchema, Detail: "the input cannot be represented as JSON"}
	}
	if marker := findMarker(value, text); marker != "" {
		return nil, &Refusal{Reason: RoleMarker, Detail: "the input carries " + marker}
	}
	if m.in != nil {
		if detail := m.in.validate(value, ""); detail != "" {
			return nil, &Refusal{Reason: InputSchema, Detail: detail}
		}
	}
	return []Message{
		{Role: "system", Content: m.prompt},
		{Role: "user", Content: text},
	}, nil
}

// CheckOutput validates the model's output and returns it: the parsed JSON
// value, or the text itself when the output schema is for a string (or there is
// no output schema). A schema whose type is not string needs the output to be
// JSON; text that does not parse is refused with reason "output_schema". The
// PII filter, when set, then runs over the raw text and, for parsed output,
// over the parsed value written back as compact JSON.
func (m *Moat) CheckOutput(modelOutput string) (any, *Refusal) {
	var value any = modelOutput
	if m.out != nil && !m.out.stringOnly() {
		parsed, err := parseJSON([]byte(modelOutput))
		if err != nil {
			return nil, &Refusal{Reason: OutputSchema, Detail: "the output is not valid JSON"}
		}
		value = parsed
	}
	if m.out != nil {
		if detail := m.out.validate(value, ""); detail != "" {
			return nil, &Refusal{Reason: OutputSchema, Detail: detail}
		}
	}
	if m.pii != nil {
		// The filter must see what the caller receives. A parsed value is written back as compact JSON, so a JSON
		// escape in the raw text cannot hide what the filter looks for; the raw text is checked as well.
		texts := []string{modelOutput}
		if m.out != nil && !m.out.stringOnly() {
			compact, err := marshalNoEscape(value)
			if err != nil {
				return nil, &Refusal{Reason: PII, Detail: "the output cannot be written back for the PII filter"}
			}
			texts = append(texts, string(compact))
		}
		for _, text := range texts {
			if detail, found := m.pii(text); found {
				if detail == "" {
					detail = "personal data found"
				}
				return nil, &Refusal{Reason: PII, Detail: detail}
			}
		}
	}
	return value, nil
}

// normalizeInput returns the input as a generic JSON value and as the text
// that goes into the user message.
func normalizeInput(v any) (any, string, error) {
	if s, ok := v.(string); ok {
		return s, s, nil
	}
	raw, err := marshalNoEscape(v)
	if err != nil {
		return nil, "", err
	}
	parsed, err := parseJSON(raw)
	if err != nil {
		return nil, "", err
	}
	return parsed, string(raw), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(sb.String(), "\n")), nil
}

func parseJSON(b []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errTrailing
	}
	return v, nil
}
