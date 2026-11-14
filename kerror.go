// Package k9o is the Kilimanjaro platform SDK for Go.
//
// Idiomatic shape: PascalCase exported methods +
// tuple-return (data, meta, err *KError) error delivery. KError is
// a concrete pointer type (nil on success) — caller pattern:
//
//	data, meta, kerr := client.From("posts").Select().Execute(ctx)
//	if kerr != nil {
//	    if kerr.Code == "K9O_TIER_CAP_HIT" { ... }
//	    return kerr
//	}
package k9o

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
)

// KError is the closed-taxonomy error type returned by every k9o
// surface on non-2xx gateway responses or SDK-side validation failures.
//
// Implements the standard library `error` interface; safe to wrap
// with errors.Is / errors.As. Caller code narrows on `.Code` field
// per the closed taxonomy.
type KError struct {
	// Code is the closed-taxonomy code (e.g. "K9O_TIER_CAP_HIT").
	Code string
	// Message is the human-readable explanation.
	Message string
	// Context carries structured per-code payload.
	Context map[string]any
	// Recommendation is the actionable next step (may be empty string).
	Recommendation string
	// RetryAfterSec is the wait, in seconds, the gateway asked for. It is
	// nil when the gateway did not send one. The SDK does not act on it.
	RetryAfterSec *float64
	// Status is the HTTP status the gateway returned (0 for SDK-side errors).
	Status int
	// RequestID is the gateway-emitted trace id.
	RequestID string
	// StepUp is the step-up challenge on a 401 K9O_STEP_UP_REQUIRED (read from
	// Context["stepUp"]); nil on every other error.
	StepUp *StepUpChallenge
	// Cause is the error that stopped the SDK from handling this one, for
	// example the error a StepUp hook returned. Nil otherwise.
	Cause error
}

// Unwrap returns Cause, so errors.Is and errors.As see through a KError.
func (e *KError) Unwrap() error { return e.Cause }

// Error satisfies the error interface.
func (e *KError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("[%s] %s (status=%d)", e.Code, e.Message, e.Status)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// KErrorWire is the wire-shape sentinel matching the gateway's
// KError JSON payload.
type KErrorWire struct {
	Code           string         `json:"code"`
	Message        string         `json:"message"`
	Context        map[string]any `json:"context,omitempty"`
	Recommendation string         `json:"recommendation,omitempty"`
	RetryAfterSec  *float64       `json:"-"`
	RequestID      string         `json:"requestId,omitempty"`
}

// UnmarshalJSON decodes the gateway error body. The `recommendation`
// field is null, a string, or an object; whatever its JSON type, it
// never fails the decode. A string is kept. An object yields its string
// `action`, else its string `note`, else "". `retryAfterSec` is read
// from the object when it is a finite number >= 0.
func (w *KErrorWire) UnmarshalJSON(data []byte) error {
	var raw struct {
		Code           string          `json:"code"`
		Message        string          `json:"message"`
		Context        json.RawMessage `json:"context"`
		Recommendation json.RawMessage `json:"recommendation"`
		RequestID      string          `json:"requestId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	w.Code = raw.Code
	w.Message = raw.Message
	w.RequestID = raw.RequestID
	w.Context = nil
	if len(raw.Context) > 0 {
		var ctx map[string]any
		if json.Unmarshal(raw.Context, &ctx) == nil {
			w.Context = ctx
		}
	}
	w.Recommendation, w.RetryAfterSec = decodeRecommendation(raw.Recommendation)
	return nil
}

func decodeRecommendation(raw json.RawMessage) (string, *float64) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return "", nil
	}
	text := ""
	for _, key := range []string{"action", "note"} {
		var v string
		if r, ok := obj[key]; ok && string(r) != "null" && json.Unmarshal(r, &v) == nil {
			text = v
			break
		}
	}
	var retry *float64
	if r, ok := obj["retryAfterSec"]; ok && string(r) != "null" {
		var n float64
		if json.Unmarshal(r, &n) == nil && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) {
			retry = &n
		}
	}
	return text, retry
}

// kerrorFromWire rehydrates a wire KError payload into a typed *KError.
// No allow-list — gateway code-space is open-ended; preserves whatever
// code the gateway sends. JSON tag handles wire camelCase → Go
// PascalCase translation at the boundary.
func kerrorFromWire(wire *KErrorWire, status int) *KError {
	ke := &KError{
		Code:           wire.Code,
		Message:        wire.Message,
		Context:        wire.Context,
		Recommendation: wire.Recommendation,
		RetryAfterSec:  wire.RetryAfterSec,
		Status:         status,
		RequestID:      wire.RequestID,
	}
	if wire.Code == "K9O_STEP_UP_REQUIRED" {
		ke.StepUp = stepUpFromContext(wire.Context)
	}
	return ke
}

// requestIDHeader is the response header that carries the gateway's id for the request.
const requestIDHeader = "x-k9o-request-id"

// withRequestID sets ke.RequestID from the response header when the error
// body did not already provide one. A body value wins; with neither the
// field stays empty. It returns ke.
func withRequestID(ke *KError, h http.Header) *KError {
	if ke != nil && ke.RequestID == "" {
		ke.RequestID = strings.TrimSpace(h.Get(requestIDHeader))
	}
	return ke
}
