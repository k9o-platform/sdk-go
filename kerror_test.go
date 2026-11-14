package k9o

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKErrorError(t *testing.T) {
	e := &KError{Code: "K9O_TIER_CAP_HIT", Message: "cap exceeded", Status: 429}
	msg := e.Error()
	if !strings.Contains(msg, "K9O_TIER_CAP_HIT") {
		t.Errorf("Error() missing code: %q", msg)
	}
	if !strings.Contains(msg, "cap exceeded") {
		t.Errorf("Error() missing message: %q", msg)
	}
	if !strings.Contains(msg, "429") {
		t.Errorf("Error() missing status: %q", msg)
	}
}

func TestKErrorErrorNoStatus(t *testing.T) {
	e := &KError{Code: "K9O_INVALID_BASE_URL", Message: "bad url"}
	msg := e.Error()
	if strings.Contains(msg, "status=") {
		t.Errorf("Error() should not include status=0: %q", msg)
	}
}

func TestKErrorFromWire(t *testing.T) {
	wire := &KErrorWire{
		Code:           "K9O_PROJECT_NOT_FOUND",
		Message:        "no such project",
		Context:        map[string]any{"projectSlug": "ghost"},
		Recommendation: "create the project first",
		RequestID:      "req_abc",
	}
	e := kerrorFromWire(wire, 404)
	if e.Code != "K9O_PROJECT_NOT_FOUND" {
		t.Errorf("Code: got %q", e.Code)
	}
	if e.Status != 404 {
		t.Errorf("Status: got %d", e.Status)
	}
	if e.RequestID != "req_abc" {
		t.Errorf("RequestID: got %q", e.RequestID)
	}
	if e.Context["projectSlug"] != "ghost" {
		t.Errorf("Context: got %v", e.Context)
	}
}

func TestKErrorFromWireUnknownCodePassthrough(t *testing.T) {
	wire := &KErrorWire{Code: "K9O_NOVEL_CODE", Message: "y"}
	e := kerrorFromWire(wire, 422)
	if e.Code != "K9O_NOVEL_CODE" {
		t.Errorf("Code: got %q", e.Code)
	}
	if e.Status != 422 {
		t.Errorf("Status: got %d", e.Status)
	}
}

func TestKErrorRecommendationShapes(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name   string
		status int
		rec    string
		want   string
		retry  *float64
	}{
		{"object action", 403, `{"action":"Open Billing and settle the balance."}`, "Open Billing and settle the balance.", nil},
		{"object retry note", 429, `{"retryAfterSec":30,"note":"retry"}`, "retry", f(30)},
		{"fractional retry", 429, `{"retryAfterSec":1.5}`, "", f(1.5)},
		{"negative retry", 429, `{"retryAfterSec":-1,"note":"n"}`, "n", nil},
		{"action not string", 429, `{"action":5,"note":"n"}`, "n", nil},
		{"null", 403, `null`, "", nil},
		{"string", 403, `"upgrade"`, "upgrade", nil},
		{"number", 403, `42`, "", nil},
		{"array", 403, `["a"]`, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"code":"K9O_READ_ONLY_SUSPENDED","message":"suspended","context":{"x":1},"recommendation":` + tc.rec + `}`
			var wire KErrorWire
			if err := json.Unmarshal([]byte(body), &wire); err != nil {
				t.Fatalf("decode: %v", err)
			}
			e := kerrorFromWire(&wire, tc.status)
			if e.Code != "K9O_READ_ONLY_SUSPENDED" || e.Message != "suspended" || e.Status != tc.status {
				t.Fatalf("lost fields: %+v", e)
			}
			if v, ok := e.Context["x"].(float64); !ok || v != 1 {
				t.Errorf("context: %v", e.Context)
			}
			if e.Recommendation != tc.want {
				t.Errorf("Recommendation: got %q want %q", e.Recommendation, tc.want)
			}
			if (e.RetryAfterSec == nil) != (tc.retry == nil) || (tc.retry != nil && *e.RetryAfterSec != *tc.retry) {
				t.Errorf("RetryAfterSec: got %v want %v", e.RetryAfterSec, tc.retry)
			}
		})
	}
}

func TestKErrorRecommendationObjectOverHTTP(t *testing.T) {
	body := `{"code":"K9O_RATE_LIMITED","message":"slow down","context":{"x":1},"recommendation":{"retryAfterSec":30,"note":"retry"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c, _ := NewClient(ClientOpts{BaseURL: srv.URL, OrgSlug: "acme"})
	_, _, kerr := c.Projects.List(context.Background())
	if kerr == nil || kerr.Code != "K9O_RATE_LIMITED" || kerr.Status != 429 || kerr.Recommendation != "retry" ||
		kerr.RetryAfterSec == nil || *kerr.RetryAfterSec != 30 {
		t.Fatalf("http path: %+v", kerr)
	}
}

func requestIDStatus(t *testing.T, header, headerVal, body string) *KError {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if header != "" {
			w.Header()[header] = []string{headerVal} // set verbatim, no canonicalization
		}
		w.WriteHeader(404)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c, _ := NewClient(ClientOpts{BaseURL: srv.URL, OrgSlug: "acme"})
	_, _, kerr := c.Projects.List(context.Background())
	if kerr == nil {
		t.Fatal("expected an error")
	}
	return kerr
}

func TestKErrorRequestIDFromHeader(t *testing.T) {
	body := `{"code":"K9O_NOT_FOUND","message":"gone"}`
	if got := requestIDStatus(t, "x-k9o-request-id", "req_abc", body).RequestID; got != "req_abc" {
		t.Errorf("header only: got %q", got)
	}
	if got := requestIDStatus(t, "x-k9o-request-id", "req_h", `{"code":"K9O_NOT_FOUND","message":"gone","requestId":"req_body"}`).RequestID; got != "req_body" {
		t.Errorf("body wins: got %q", got)
	}
	if got := requestIDStatus(t, "", "", body).RequestID; got != "" {
		t.Errorf("neither: got %q", got)
	}
	if got := requestIDStatus(t, "X-K9O-Request-Id", "req_abc", body).RequestID; got != "req_abc" {
		t.Errorf("mixed case: got %q", got)
	}
	// a non-KError body still gets the id
	if got := requestIDStatus(t, "x-k9o-request-id", "req_abc", `oops`).RequestID; got != "req_abc" {
		t.Errorf("non-KError body: got %q", got)
	}
}
