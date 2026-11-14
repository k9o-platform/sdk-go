package k9o

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---- keys.rotate step-up ----

func stepUp401() map[string]any {
	return map[string]any{"code": "K9O_STEP_UP_REQUIRED", "message": "step-up required", "context": map[string]any{"stepUp": stepUpCtx}}
}

type stepUpStub struct {
	calls   int
	headers []string
	bodies  []string
	answers []func() *http.Response
}

func (s *stepUpStub) client(t *testing.T) *Client {
	return rampartTestClient(t, func(req *http.Request) *http.Response {
		s.headers = append(s.headers, req.Header.Get("x-k9o-step-up-proof"))
		b, _ := io.ReadAll(req.Body)
		s.bodies = append(s.bodies, string(b))
		i := s.calls
		s.calls++
		if i >= len(s.answers) {
			i = len(s.answers) - 1
		}
		return s.answers[i]()
	})
}

func decodeProofHeader(t *testing.T, h string) map[string]string {
	t.Helper()
	if strings.ContainsAny(h, "=+/") {
		t.Fatalf("header must be base64url without padding: %q", h)
	}
	raw, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var rotated = func() *http.Response {
	return jsonResponse(200, dkOK(map[string]any{"keyId": "vkey_1", "previousVersion": 1, "newVersion": 2, "status": "rotated"}))
}

func TestKeysRotateStepUpHookRetriesOnce(t *testing.T) {
	stub := &stepUpStub{answers: []func() *http.Response{func() *http.Response { return jsonResponse(401, stepUp401()) }, rotated}}
	var seen StepUpRequest
	rot, _, kerr := stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{
		StepUp: func(_ context.Context, req StepUpRequest) (StepUpProof, error) {
			seen = req
			return StepUpProof{Method: "dpop", Proof: "eyJ.proof+/=="}, nil
		},
	})
	if kerr != nil || *rot.NewVersion != 2 {
		t.Fatalf("%+v %v", rot, kerr)
	}
	if stub.calls != 2 {
		t.Fatalf("calls: %d", stub.calls)
	}
	ch := seen.Challenge
	if ch.ScopeSetRoot != "ssr_1" || ch.Nonce != "n_1" || ch.MinTier != "T3_KEY_POSSESSION" || len(ch.Methods) != 3 || ch.MaxAgeSec != 120 || !ch.SingleUse || ch.RpID != "k9o.example" {
		t.Fatalf("challenge: %+v", ch)
	}
	if seen.HTTPMethod != "POST" || !strings.HasSuffix(seen.URL, "/@portal/vault/keys/vkey_1/rotate") || !strings.HasPrefix(seen.URL, "https://") {
		t.Fatalf("request: %+v", seen)
	}
	if stub.headers[0] != "" {
		t.Fatal("the first request must carry no proof")
	}
	h := decodeProofHeader(t, stub.headers[1])
	if h["scopeSetRoot"] != "ssr_1" || h["nonce"] != "n_1" || h["method"] != "dpop" || h["proof"] != "eyJ.proof+/==" || len(h) != 4 {
		t.Fatalf("header: %v", h)
	}
	if stub.bodies[0] != stub.bodies[1] {
		t.Fatalf("the retry must be the same request: %q vs %q", stub.bodies[0], stub.bodies[1])
	}
}

func TestKeysRotateStepUpWithoutHook(t *testing.T) {
	stub := &stepUpStub{answers: []func() *http.Response{func() *http.Response { return jsonResponse(401, stepUp401()) }}}
	_, _, kerr := stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{})
	if kerr == nil || kerr.Code != "K9O_STEP_UP_REQUIRED" || kerr.Status != 401 || stub.calls != 1 {
		t.Fatalf("%v calls=%d", kerr, stub.calls)
	}
	if kerr.StepUp == nil || kerr.StepUp.Nonce != "n_1" || kerr.StepUp.ScopeSetRoot != "ssr_1" || kerr.StepUp.Methods[1] != "dpop" || kerr.StepUp.MaxAgeSec != 120 {
		t.Fatalf("challenge must be reachable: %+v", kerr.StepUp)
	}
	if kerr.Context["stepUp"] == nil {
		t.Fatal("context keeps the raw challenge")
	}
}

func TestKeysRotateStepUpHookFails(t *testing.T) {
	stub := &stepUpStub{answers: []func() *http.Response{func() *http.Response { return jsonResponse(401, stepUp401()) }}}
	boom := errors.New("key is locked")
	_, _, kerr := stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{
		StepUp: func(context.Context, StepUpRequest) (StepUpProof, error) { return StepUpProof{}, boom },
	})
	if kerr == nil || kerr.Code != "K9O_STEP_UP_REQUIRED" || stub.calls != 1 || !errors.Is(kerr, boom) || kerr.StepUp == nil {
		t.Fatalf("%v calls=%d", kerr, stub.calls)
	}
	// an unusable answer is also not sent
	for _, p := range []StepUpProof{{}, {Method: "dpop"}, {Proof: "x"}, {Method: "sms", Proof: "x"}} {
		stub = &stepUpStub{answers: []func() *http.Response{func() *http.Response { return jsonResponse(401, stepUp401()) }}}
		_, _, kerr = stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{
			StepUp: func(context.Context, StepUpRequest) (StepUpProof, error) { return p, nil },
		})
		if kerr == nil || kerr.Code != "K9O_STEP_UP_REQUIRED" || kerr.Cause == nil || stub.calls != 1 {
			t.Fatalf("%+v: %v calls=%d", p, kerr, stub.calls)
		}
	}
}

func TestKeysRotateStepUpSecond401NotRetried(t *testing.T) {
	for _, second := range []map[string]any{
		{"code": "K9O_STEP_UP_PROOF_MALFORMED", "message": "proof malformed"},
		stepUp401(),
	} {
		second := second
		stub := &stepUpStub{answers: []func() *http.Response{
			func() *http.Response { return jsonResponse(401, stepUp401()) },
			func() *http.Response { return jsonResponse(401, second) },
		}}
		hookCalls := 0
		_, _, kerr := stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{
			StepUp: func(context.Context, StepUpRequest) (StepUpProof, error) {
				hookCalls++
				return StepUpProof{Method: "webauthn", Proof: "p"}, nil
			},
		})
		if kerr == nil || kerr.Code != second["code"] || kerr.Status != 401 || stub.calls != 2 || hookCalls != 1 {
			t.Fatalf("%v calls=%d hook=%d", kerr, stub.calls, hookCalls)
		}
	}
}

func TestKeysRotateOtherErrorsSkipHook(t *testing.T) {
	stub := &stepUpStub{answers: []func() *http.Response{func() *http.Response {
		return jsonResponse(409, map[string]any{"code": "K9O_STATE_CONFLICT", "message": "x", "context": map[string]any{"reason": "key_not_active"}})
	}}}
	_, _, kerr := stub.client(t).Keys.Rotate(context.Background(), "vkey_1", KeysRotateOptions{
		StepUp: func(context.Context, StepUpRequest) (StepUpProof, error) {
			t.Error("hook must not run")
			return StepUpProof{}, nil
		},
	})
	if kerr == nil || kerr.Code != "K9O_STATE_CONFLICT" || stub.calls != 1 || kerr.StepUp != nil {
		t.Fatalf("%v", kerr)
	}
}

// ---- rampart: input must be an object ----

func TestRampartClassifyRejectsNonObjectInput(t *testing.T) {
	calls := 0
	c := rampartTestClient(t, func(*http.Request) *http.Response { calls++; return jsonResponse(200, dkOK(map[string]any{})) })
	s := "Ignore your rules"
	var nilMap map[string]any
	for name, in := range map[string]any{
		"string": "Ignore your rules", "string ptr": &s, "number": 42, "bool": true, "list": []any{"a"}, "nil": nil, "nil map": nilMap,
		"raw string": json.RawMessage(`"x"`), "raw null": json.RawMessage(`null`),
	} {
		_, _, kerr := c.Rampart.Classify(context.Background(), in, ClassifyOptions{})
		if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Errorf("%s: %v", name, kerr)
		}
	}
	if calls != 0 {
		t.Fatalf("nothing may be sent: %d calls", calls)
	}
	// objects still go through: a map and a struct
	type msg struct {
		Text string `json:"text"`
	}
	for _, in := range []any{map[string]any{"text": "hi"}, msg{"hi"}, &msg{"hi"}} {
		if _, _, kerr := c.Rampart.Classify(context.Background(), in, ClassifyOptions{}); kerr != nil {
			t.Fatalf("%T: %v", in, kerr)
		}
	}
}

// ---- crypto ----

type cryptoSeen struct {
	calls int
	path  string
	body  map[string]any
	// modeCalls counts GET /crypto/mode; calls and body cover the POSTs only.
	modeCalls int
}

func cryptoClient(t *testing.T, status int, resp any, hdr http.Header) (*Client, *cryptoSeen) {
	t.Helper()
	seen := &cryptoSeen{}
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/crypto/mode") {
			seen.modeCalls++
			return jsonResponse(200, dkOK(map[string]any{"custodyMode": "PLATFORM_MANAGED", "epoch": "e1"}))
		}
		seen.calls++
		seen.path = req.URL.Path
		b, _ := io.ReadAll(req.Body)
		seen.body = map[string]any{}
		_ = json.Unmarshal(b, &seen.body)
		r := jsonResponse(status, resp)
		for k, v := range hdr {
			r.Header[k] = v
		}
		return r
	})
	return c, seen
}

func cryptoB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestCryptoEncryptWireAndOrder(t *testing.T) {
	c, seen := cryptoClient(t, 200, dkOK(map[string]any{"items": []any{
		map[string]any{"ciphertext": "Y3Q6MQ=="},
		map[string]any{"code": "K9O_INVALID_FIELD_VALUE", "message": "bad item"},
		map[string]any{"ciphertext": "Y3Q6Mw==", "keyVersion": 7},
	}}), nil)
	res, _, kerr := c.Crypto.Encrypt(context.Background(), "orders-2026", []CryptoEncryptItem{
		{Plaintext: []byte{0, 1, 2, 255}, Context: "tenant:42"},
		CryptoText("héllo", ""),
		{Plaintext: []byte("c")},
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if seen.path != "/@portal/crypto/encrypt" || seen.body["crypt"] != "orders-2026" || seen.body["custodyEpoch"] != "e1" {
		t.Fatalf("request: %s %v", seen.path, seen.body)
	}
	items := seen.body["items"].([]any)
	i0, i1 := items[0].(map[string]any), items[1].(map[string]any)
	if i0["plaintext"] != base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 255}) || i0["context"] != "tenant:42" || i1["plaintext"] != cryptoB64("héllo") {
		t.Fatalf("items: %v", items)
	}
	if _, has := i1["context"]; has {
		t.Fatal("empty context must be omitted")
	}
	if len(res.Items) != 3 || res.Items[0].Ciphertext != "Y3Q6MQ==" || !res.Items[0].OK() || res.Items[0].Err() != nil {
		t.Fatalf("results: %+v", res.Items)
	}
	if bad := res.Items[1]; bad.OK() || bad.Code != "K9O_INVALID_FIELD_VALUE" || bad.Message != "bad item" || bad.Err().Code != "K9O_INVALID_FIELD_VALUE" || bad.Ciphertext != "" {
		t.Fatalf("failed item: %+v", bad)
	}
	if string(res.Items[2].Extra["keyVersion"]) != "7" || res.Items[2].Ciphertext != "Y3Q6Mw==" {
		t.Fatalf("unknown fields must be kept: %+v", res.Items[2])
	}
}

func TestCryptoDecryptAndRewrap(t *testing.T) {
	c, seen := cryptoClient(t, 200, dkOK(map[string]any{"items": []any{
		map[string]any{"plaintext": cryptoB64("one")}, map[string]any{"plaintext": cryptoB64("two")},
	}}), nil)
	in := []CryptoCiphertextItem{{Ciphertext: "AAEC", Context: "tenant:42"}, {Ciphertext: "AwQF"}}
	res, _, kerr := c.Crypto.Decrypt(context.Background(), "orders-2026", in)
	if kerr != nil || res.Items[0].Text() != "one" || string(res.Items[1].Plaintext) != "two" {
		t.Fatalf("%+v %v", res, kerr)
	}
	items := seen.body["items"].([]any)
	if seen.path != "/@portal/crypto/decrypt" || items[0].(map[string]any)["ciphertext"] != "AAEC" || items[0].(map[string]any)["context"] != "tenant:42" || len(items[1].(map[string]any)) != 1 {
		t.Fatalf("%s %v", seen.path, seen.body)
	}

	c, seen = cryptoClient(t, 200, dkOK(map[string]any{"items": []any{map[string]any{"ciphertext": "bmV3"}, map[string]any{"code": "K9O_ENVELOPE_INTEGRITY_FAILURE", "message": "cannot open"}}}), nil)
	res, _, kerr = c.Crypto.Rewrap(context.Background(), "orders-2026", in)
	if kerr != nil || seen.path != "/@portal/crypto/rewrap" || res.Items[0].Ciphertext != "bmV3" || res.Items[1].Err().Code != "K9O_ENVELOPE_INTEGRITY_FAILURE" {
		t.Fatalf("%s %+v %v", seen.path, res, kerr)
	}

	// a result count that differs from the request is an error, not a guess
	c, _ = cryptoClient(t, 200, dkOK(map[string]any{"items": []any{}}), nil)
	if _, _, kerr = c.Crypto.Decrypt(context.Background(), "k", in); kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" {
		t.Fatalf("count: %v", kerr)
	}
	// a plaintext that is not base64 is an error
	c, _ = cryptoClient(t, 200, dkOK(map[string]any{"items": []any{map[string]any{"plaintext": "***"}, map[string]any{"plaintext": ""}}}), nil)
	if _, _, kerr = c.Crypto.Decrypt(context.Background(), "k", in); kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" {
		t.Fatalf("base64: %v", kerr)
	}
}

func TestCryptoValidationSendsNothing(t *testing.T) {
	c, seen := cryptoClient(t, 200, dkOK(map[string]any{"items": []any{}}), nil)
	ctx := context.Background()
	ok := []CryptoEncryptItem{{Plaintext: []byte("x")}}
	over := func(n int) []CryptoEncryptItem {
		out := make([]CryptoEncryptItem, n)
		for i := range out {
			out[i].Plaintext = []byte("x")
		}
		return out
	}
	tooBig := func(n, each int) []CryptoEncryptItem {
		out := make([]CryptoEncryptItem, n)
		for i := range out {
			out[i].Plaintext = make([]byte, each)
		}
		return out
	}
	cases := []struct {
		name  string
		crypt string
		items []CryptoEncryptItem
		code  string
	}{
		{"empty crypt", "", ok, "K9O_INVALID_FIELD_VALUE"},
		{"uppercase crypt", "Orders", ok, "K9O_INVALID_FIELD_VALUE"},
		{"underscore crypt", "or_ders", ok, "K9O_INVALID_FIELD_VALUE"},
		{"long crypt", strings.Repeat("a", 65), ok, "K9O_INVALID_FIELD_VALUE"},
		{"crypt named actions", "actions", ok, "K9O_INVALID_FIELD_VALUE"},
		{"no items", "k", nil, "K9O_INVALID_FIELD_VALUE"},
		{"101 items", "k", over(101), "K9O_INVALID_FIELD_VALUE"},
		{"context 257 bytes", "k", []CryptoEncryptItem{{Plaintext: []byte("x"), Context: strings.Repeat("c", 257)}}, "K9O_INVALID_FIELD_VALUE"},
		{"item 64 KiB + 1", "k", tooBig(1, 64*1024+1), "K9O_PAYLOAD_TOO_LARGE"},
		{"call 1 MiB + 1", "k", append(tooBig(16, 64*1024), CryptoEncryptItem{Plaintext: []byte("x")}), "K9O_PAYLOAD_TOO_LARGE"},
	}
	for _, tc := range cases {
		if _, _, kerr := c.Crypto.Encrypt(ctx, tc.crypt, tc.items); kerr == nil || kerr.Code != tc.code {
			t.Errorf("%s: %v", tc.name, kerr)
		}
	}
	if _, _, kerr := c.Crypto.Decrypt(ctx, "k", []CryptoCiphertextItem{{Ciphertext: ""}}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("empty ciphertext: %v", kerr)
	}
	if _, _, kerr := c.Crypto.Rewrap(ctx, "K", []CryptoCiphertextItem{{Ciphertext: "a"}}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("rewrap crypt: %v", kerr)
	}
	if _, _, kerr := c.Crypto.Decrypt(ctx, "k", make([]CryptoCiphertextItem, 101)); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("decrypt 101: %v", kerr)
	}
	if seen.calls != 0 {
		t.Fatalf("nothing may be sent, got %d calls", seen.calls)
	}
	// the limits themselves pass: 100 items, 64 KiB each is over 1 MiB, so use 16 x 64 KiB = 1 MiB
	if _, _, kerr := c.Crypto.Encrypt(ctx, strings.Repeat("a", 64), tooBig(16, 64*1024)); kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" {
		t.Errorf("limit call must be sent (and the stub's empty answer rejected): %v", kerr)
	}
	if seen.calls != 1 {
		t.Fatalf("calls: %d", seen.calls)
	}
}

func TestCryptoActionsNameRefusedAndActions2Accepted(t *testing.T) {
	c, seen := cryptoClient(t, 200, dkOK(map[string]any{"items": []any{}}), nil)
	ctx := context.Background()
	ok := []CryptoEncryptItem{{Plaintext: []byte("x")}}
	want := `crypt must be 1 to 64 characters of a-z, 0-9 and -, and not "actions"`
	_, _, kerr := c.Crypto.Encrypt(ctx, "actions", ok)
	if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" || kerr.Message != want {
		t.Fatalf("encrypt actions: %v", kerr)
	}
	if _, _, kerr := c.Crypto.Decrypt(ctx, "actions", []CryptoCiphertextItem{{Ciphertext: "AA"}}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("decrypt actions: %v", kerr)
	}
	if _, _, kerr := c.Crypto.Rewrap(ctx, "actions", []CryptoCiphertextItem{{Ciphertext: "AA"}}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("rewrap actions: %v", kerr)
	}
	if _, _, kerr := c.Crypto.FederatedToken(ctx, "actions", "encrypt"); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Errorf("federated actions: %v", kerr)
	}
	if seen.calls != 0 {
		t.Fatalf("nothing may be sent, got %d calls", seen.calls)
	}
	// actions-2 passes validation and is sent (the stub's empty answer is rejected afterwards).
	_, _, kerr = c.Crypto.Encrypt(ctx, "actions-2", ok)
	if kerr != nil && kerr.Code == "K9O_INVALID_FIELD_VALUE" {
		t.Fatalf("actions-2 must be accepted: %v", kerr)
	}
	if seen.calls != 1 || seen.body["crypt"] != "actions-2" {
		t.Fatalf("calls=%d body=%v", seen.calls, seen.body)
	}
}

func TestCryptoNotFoundAndRetiredPassThroughOnEncrypt(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{404, CryptoCodeCryptNotFound},
		{409, CryptoCodeCryptRetired},
	} {
		c, seen := cryptoClient(t, tc.status, map[string]any{"code": tc.code, "message": "m"}, nil)
		_, _, kerr := c.Crypto.Encrypt(context.Background(), "orders", []CryptoEncryptItem{{Plaintext: []byte("x")}})
		if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status {
			t.Errorf("%d %s: %v", tc.status, tc.code, kerr)
		}
		if seen.calls != 1 {
			t.Errorf("%s: %d calls, never retried", tc.code, seen.calls)
		}
	}
}

func TestCryptoErrorsAreTypedAndNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   map[string]any
		hdr    http.Header
		check  func(*KError) bool
	}{
		{400, map[string]any{"code": "K9O_ENVELOPE_INTEGRITY_FAILURE", "message": "cannot open", "context": map[string]any{}}, nil, func(k *KError) bool { return len(k.Context) == 0 }},
		{409, map[string]any{"code": "K9O_SEALED_UNDER_RETIRED_KEY", "message": "retired", "context": map[string]any{"resealableUntil": "2026-11-03T00:00:00Z"}}, nil,
			func(k *KError) bool { return k.Context["resealableUntil"] == "2026-11-03T00:00:00Z" }},
		{413, map[string]any{"code": "K9O_PAYLOAD_TOO_LARGE", "message": "too large"}, nil, func(*KError) bool { return true }},
		{429, map[string]any{"code": "K9O_RATE_LIMITED", "message": "slow down"}, http.Header{"Retry-After": {"12"}}, func(k *KError) bool { return k.RetryAfterSec != nil && *k.RetryAfterSec == 12 }},
		{503, map[string]any{"code": "K9O_KEY_SERVICE_UNAVAILABLE", "message": "down"}, nil, func(*KError) bool { return true }},
		{503, map[string]any{"code": "K9O_BYOK_KEY_UNREACHABLE", "message": "kms"}, nil, func(*KError) bool { return true }},
		{503, map[string]any{"code": "K9O_HYOK_CLUSTER_UNREACHABLE", "message": "hsm"}, nil, func(*KError) bool { return true }},
		{404, map[string]any{"code": "K9O_NOT_FOUND", "message": "no such route yet"}, nil, func(*KError) bool { return true }},
	} {
		for op, call := range map[string]func(*Client) *KError{
			"encrypt": func(c *Client) *KError {
				_, _, k := c.Crypto.Encrypt(context.Background(), "k", []CryptoEncryptItem{{Plaintext: []byte("x")}})
				return k
			},
			"decrypt": func(c *Client) *KError {
				_, _, k := c.Crypto.Decrypt(context.Background(), "k", []CryptoCiphertextItem{{Ciphertext: "AA"}})
				return k
			},
			"rewrap": func(c *Client) *KError {
				_, _, k := c.Crypto.Rewrap(context.Background(), "k", []CryptoCiphertextItem{{Ciphertext: "AA"}})
				return k
			},
		} {
			c, seen := cryptoClient(t, tc.status, tc.body, tc.hdr)
			kerr := call(c)
			if kerr == nil || kerr.Code != tc.body["code"] || kerr.Status != tc.status || !tc.check(kerr) {
				t.Errorf("%s %d: %v", op, tc.status, kerr)
			}
			if seen.calls != 1 {
				t.Errorf("%s %d: %d calls, a crypto call is never retried", op, tc.status, seen.calls)
			}
		}
	}
}

func TestCryptoNeedsProject(t *testing.T) {
	nc, _ := NewClient(ClientOpts{BaseURL: "https://acme.api.k9o.io"})
	if _, _, kerr := nc.Crypto.Encrypt(context.Background(), "k", []CryptoEncryptItem{{Plaintext: []byte("x")}}); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("%v", kerr)
	}
}
