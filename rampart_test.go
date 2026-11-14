package k9o

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var rampartNow = time.Unix(1_800_000_000, 0)

const rampartProject = "proj_0123456789ab"

type signer struct {
	priv ed25519.PrivateKey
	kid  string
}

func newSigner(t *testing.T, kid string) *signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{priv: priv, kid: kid}
}

func (s *signer) jwk() JWK {
	return JWK{Kty: "OKP", Crv: "Ed25519", Kid: s.kid, Alg: "EdDSA", Use: "sig",
		X: base64.RawURLEncoding.EncodeToString(s.priv.Public().(ed25519.PublicKey))}
}

const rampartIssuerURL = "https://api.k9o.io/rampart"

func (s *signer) keys() *JWKS { return &JWKS{Keys: []JWK{s.jwk()}, Issuer: rampartIssuerURL} }

func b64(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func inputHash(t *testing.T, input any) string {
	t.Helper()
	c, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

type receiptSpec struct {
	header map[string]any
	claims map[string]any
}

func goodSpec(t *testing.T, s *signer, input any) receiptSpec {
	return receiptSpec{
		header: map[string]any{"alg": "EdDSA", "kid": s.kid, "typ": ReceiptType},
		claims: map[string]any{
			"iss": rampartIssuerURL, "iat": rampartNow.Unix() - 10, "exp": rampartNow.Unix() + 3590,
			"input_sha256": inputHash(t, input), "classification": "suspicious", "risk_bp": 6200, "confidence_bp": 8100,
			"categories": []string{"prompt_injection"}, "project_id": rampartProject, "calibration_version": "cal-1",
		},
	}
}

func (s *signer) sign(spec receiptSpec) string {
	signing := b64(spec.header) + "." + b64(spec.claims)
	return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(signing)))
}

func rampartTestClient(t *testing.T, rt roundTripFunc) *Client {
	t.Helper()
	c, kerr := NewClient(ClientOpts{
		BaseURL:     "https://acme.api.k9o.io",
		ProjectSlug: "portal",
		APIKey:      "tok",
		HTTPClient:  &http.Client{Transport: noProbe(rt)},
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	return c
}

func verifyOpts(s *signer) VerifyReceiptOptions {
	return VerifyReceiptOptions{Keys: s.keys(), ProjectID: rampartProject, Now: func() time.Time { return rampartNow }}
}

func wantReason(t *testing.T, claims *ReceiptClaims, err *ReceiptError, reason string) {
	t.Helper()
	if claims != nil {
		t.Fatalf("a refusal must carry no claims, got %+v", claims)
	}
	if err == nil || err.Reason != reason {
		t.Fatalf("want reason %q, got %v", reason, err)
	}
}

func TestRampartClassifyWire(t *testing.T) {
	var method, path string
	var body map[string]any
	var auth string
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		method, path, auth = req.Method, req.URL.Path, req.Header.Get("Authorization")
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &body)
		return jsonResponse(200, map[string]any{
			"data": map[string]any{
				"classification": "suspicious", "riskScore": 6200, "confidence": 8100,
				"categories": []string{"prompt_injection"}, "calibrationVersion": "cal-1",
				"classificationReceipt": "a.b.c", "projectId": rampartProject,
			},
			"meta": map[string]any{"context": map[string]any{"requestId": "req_1"}},
		})
	})
	res, meta, kerr := c.Rampart.Classify(context.Background(), map[string]any{"text": "hello"}, ClassifyOptions{Label: "chat"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if method != "POST" || path != "/@portal/rampart/classify" || auth != "Bearer tok" {
		t.Fatalf("request: %s %s %s", method, path, auth)
	}
	in, _ := body["input"].(map[string]any)
	if in["text"] != "hello" || body["label"] != "chat" || len(body) != 2 {
		t.Fatalf("body: %v", body)
	}
	if res.Classification != "suspicious" || res.RiskScore != 6200 || res.Confidence != 8100 ||
		len(res.Categories) != 1 || res.Categories[0] != "prompt_injection" || res.CalibrationVersion != "cal-1" ||
		res.ClassificationReceipt != "a.b.c" || res.ProjectID != rampartProject {
		t.Fatalf("result: %+v", res)
	}
	if meta.Context == nil || meta.Context.RequestID != "req_1" {
		t.Fatalf("meta: %+v", meta)
	}
}

func TestRampartClassifyNoLabelAndValidation(t *testing.T) {
	var body map[string]any
	calls := 0
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		calls++
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &body)
		return jsonResponse(200, map[string]any{"data": map[string]any{"classification": "clear"}, "meta": map[string]any{}})
	})
	ctx := context.Background()
	if _, _, kerr := c.Rampart.Classify(ctx, map[string]any{"a": 1}, ClassifyOptions{}); kerr != nil {
		t.Fatal(kerr)
	}
	if _, has := body["label"]; has {
		t.Fatalf("label must be omitted: %v", body)
	}
	for _, bad := range []any{"text", []any{1}, nil, 5} {
		if _, _, kerr := c.Rampart.Classify(ctx, bad, ClassifyOptions{}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("input %v: got %v", bad, kerr)
		}
	}
	if _, _, kerr := c.Rampart.Classify(ctx, map[string]any{}, ClassifyOptions{Label: strings.Repeat("x", 129)}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Fatalf("label: got %v", kerr)
	}
	if calls != 1 {
		t.Fatalf("invalid arguments must send nothing; %d calls", calls)
	}
}

func TestRampartClassifyNeedsProject(t *testing.T) {
	c, kerr := NewClient(ClientOpts{BaseURL: "https://x.api.k9o.io", APIKey: "k"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if _, _, kerr := c.Rampart.Classify(context.Background(), map[string]any{}, ClassifyOptions{}); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("got %v", kerr)
	}
}

func TestRampartClassifier503(t *testing.T) {
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(503, map[string]any{
			"code": "K9O_RAMPART_CLASSIFIER_UNAVAILABLE", "message": "Rampart classifier unavailable: no_endpoint_configured",
			"context": map[string]any{"cause": "no_endpoint_configured"}, "recommendation": nil,
		})
	})
	res, _, kerr := c.Rampart.Classify(context.Background(), map[string]any{"text": "hello"}, ClassifyOptions{})
	if res != nil || kerr == nil || kerr.Code != "K9O_RAMPART_CLASSIFIER_UNAVAILABLE" || kerr.Status != 503 {
		t.Fatalf("res=%v kerr=%v", res, kerr)
	}
}

func TestRampartJWKS(t *testing.T) {
	var method, path, auth string
	s := newSigner(t, "rampart-classify-1")
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		method, path, auth = req.Method, req.URL.Path, req.Header.Get("Authorization")
		return jsonResponse(200, map[string]any{"keys": []JWK{s.jwk()}, "issuer": rampartIssuerURL, "meta": map[string]any{}})
	})
	set, kerr := c.Rampart.JWKS(context.Background())
	if kerr != nil {
		t.Fatal(kerr)
	}
	if method != "GET" || path != "/rampart/jwks" || auth != "" {
		t.Fatalf("request: %s %s auth=%q", method, path, auth)
	}
	if set.Issuer != rampartIssuerURL {
		t.Fatalf("issuer: %q", set.Issuer)
	}
	if len(set.Keys) != 1 || set.Keys[0].Kid != "rampart-classify-1" || set.Keys[0].Kty != "OKP" || set.Keys[0].Crv != "Ed25519" {
		t.Fatalf("keys: %+v", set)
	}
}

func TestRampartJWKSErrors(t *testing.T) {
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(200, map[string]any{"data": []any{}})
	})
	if _, kerr := c.Rampart.JWKS(context.Background()); kerr == nil {
		t.Fatal("a body without keys must be an error")
	}
	c = rampartTestClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(500, map[string]any{"code": "K9O_INTERNAL_ERROR", "message": "boom"})
	})
	if _, kerr := c.Rampart.JWKS(context.Background()); kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" {
		t.Fatalf("got %v", kerr)
	}
}

func TestVerifyReceiptHappyPath(t *testing.T) {
	s := newSigner(t, "k1")
	input := map[string]any{"text": "hello", "n": 15, "nested": map[string]any{"b": 1, "a": []any{true, nil}}}
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request expected"); return nil })
	claims, err := c.Rampart.VerifyReceipt(context.Background(), s.sign(goodSpec(t, s, input)), input, verifyOpts(s))
	if err != nil {
		t.Fatal(err)
	}
	if claims.Classification != "suspicious" || claims.RiskBP != 6200 || claims.ConfidenceBP != 8100 ||
		len(claims.Categories) != 1 || claims.CalibrationVersion != "cal-1" || claims.ProjectID != rampartProject ||
		claims.KeyID != "k1" || claims.Issuer != rampartIssuerURL || claims.InputSHA256 != inputHash(t, input) ||
		claims.ExpiresAt.Unix() != rampartNow.Unix()+3590 {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestVerifyReceiptRefusals(t *testing.T) {
	s := newSigner(t, "k1")
	other := newSigner(t, "k1")
	input := map[string]any{"text": "hello"}
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request expected"); return nil })
	ctx := context.Background()

	mutate := func(f func(*receiptSpec)) string {
		spec := goodSpec(t, s, input)
		f(&spec)
		return s.sign(spec)
	}
	cases := []struct {
		name    string
		receipt string
		input   any
		opts    VerifyReceiptOptions
		reason  string
	}{
		{"wrong kid", mutate(func(r *receiptSpec) { r.header["kid"] = "nope" }), input, verifyOpts(s), ReceiptKeyUnknown},
		{"no kid", mutate(func(r *receiptSpec) { delete(r.header, "kid") }), input, verifyOpts(s), ReceiptKeyUnknown},
		{"alg none", mutate(func(r *receiptSpec) { r.header["alg"] = "none" }), input, verifyOpts(s), ReceiptAlg},
		{"alg HS256", mutate(func(r *receiptSpec) { r.header["alg"] = "HS256" }), input, verifyOpts(s), ReceiptAlg},
		{"wrong typ", mutate(func(r *receiptSpec) { r.header["typ"] = "JWT" }), input, verifyOpts(s), ReceiptTyp},
		{"wrong iss", mutate(func(r *receiptSpec) { r.claims["iss"] = "https://evil.example/rampart" }), input, verifyOpts(s), ReceiptIssuer},
		{"wrong project", mutate(func(r *receiptSpec) { r.claims["project_id"] = "proj_other" }), input, verifyOpts(s), ReceiptProject},
		{"fractional input", s.sign(goodSpec(t, s, input)), map[string]any{"n": 0.5}, verifyOpts(s), ReceiptInputHash},
		{"expired", mutate(func(r *receiptSpec) { r.claims["exp"] = rampartNow.Unix() - 61 }), input, verifyOpts(s), ReceiptExpired},
		{"not yet valid", mutate(func(r *receiptSpec) { r.claims["iat"] = rampartNow.Unix() + 61 }), input, verifyOpts(s), ReceiptNotYetValid},
		{"input mismatch", s.sign(goodSpec(t, s, input)), map[string]any{"text": "other"}, verifyOpts(s), ReceiptInputHash},
		{"signed by another key", other.sign(goodSpec(t, other, input)), input, verifyOpts(s), ReceiptSignature},
		{"empty", "", input, verifyOpts(s), ReceiptMalformed},
		{"two parts", "a.b", input, verifyOpts(s), ReceiptMalformed},
		{"not base64", "!!.!!.!!", input, verifyOpts(s), ReceiptMalformed},
		{"payload not an object", func() string {
			signing := b64(map[string]any{"alg": "EdDSA", "kid": "k1", "typ": ReceiptType}) + "." + b64([]int{1})
			return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(signing)))
		}(), input, verifyOpts(s), ReceiptMalformed},
		{"unknown classification", mutate(func(r *receiptSpec) { r.claims["classification"] = "fine" }), input, verifyOpts(s), ReceiptMalformed},
		{"missing input_sha256", mutate(func(r *receiptSpec) { delete(r.claims, "input_sha256") }), input, verifyOpts(s), ReceiptMalformed},
		{"missing exp", mutate(func(r *receiptSpec) { delete(r.claims, "exp") }), input, verifyOpts(s), ReceiptMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := c.Rampart.VerifyReceipt(ctx, tc.receipt, tc.input, tc.opts)
			wantReason(t, claims, err, tc.reason)
		})
	}

	t.Run("tampered signature", func(t *testing.T) {
		r := s.sign(goodSpec(t, s, input))
		i := strings.LastIndex(r, ".") + 1
		flipped := []byte(r)
		if flipped[i] == 'A' {
			flipped[i] = 'B'
		} else {
			flipped[i] = 'A'
		}
		claims, err := c.Rampart.VerifyReceipt(ctx, string(flipped), input, verifyOpts(s))
		wantReason(t, claims, err, ReceiptSignature)
	})
	t.Run("tampered claims", func(t *testing.T) {
		spec := goodSpec(t, s, input)
		r := s.sign(spec)
		parts := strings.Split(r, ".")
		spec.claims["classification"] = "clear"
		parts[1] = b64(spec.claims)
		claims, err := c.Rampart.VerifyReceipt(ctx, strings.Join(parts, "."), input, verifyOpts(s))
		wantReason(t, claims, err, ReceiptSignature)
	})
	t.Run("tampered header", func(t *testing.T) {
		spec := goodSpec(t, s, input)
		parts := strings.Split(s.sign(spec), ".")
		spec.header["extra"] = 1
		parts[0] = b64(spec.header)
		claims, err := c.Rampart.VerifyReceipt(ctx, strings.Join(parts, "."), input, verifyOpts(s))
		wantReason(t, claims, err, ReceiptSignature)
	})
}

func TestVerifyReceiptKeyAndOptionRules(t *testing.T) {
	s := newSigner(t, "k1")
	input := map[string]any{"text": "hello"}
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request expected"); return nil })
	ctx := context.Background()
	r := s.sign(goodSpec(t, s, input))

	// A key that is not Ed25519 is not a usable key.
	rsa := s.jwk()
	rsa.Kty = "RSA"
	o := verifyOpts(s)
	o.Keys = &JWKS{Keys: []JWK{rsa}}
	claims, err := c.Rampart.VerifyReceipt(ctx, r, input, o)
	wantReason(t, claims, err, ReceiptKeyUnknown)

	// Skew: just inside passes, a negative skew means none.
	spec := goodSpec(t, s, input)
	spec.claims["exp"] = rampartNow.Unix() - 30
	if _, err := c.Rampart.VerifyReceipt(ctx, s.sign(spec), input, verifyOpts(s)); err != nil {
		t.Fatalf("within the default skew: %v", err)
	}
	o = verifyOpts(s)
	o.ClockSkew = -1
	claims, err = c.Rampart.VerifyReceipt(ctx, s.sign(spec), input, o)
	wantReason(t, claims, err, ReceiptExpired)
	o.ClockSkew = 31 * time.Second
	if _, err := c.Rampart.VerifyReceipt(ctx, s.sign(spec), input, o); err != nil {
		t.Fatalf("custom skew: %v", err)
	}

	// Issuer option overrides the feed.
	o = verifyOpts(s)
	o.Issuer = "https://gw.internal.example/rampart"
	claims, err = c.Rampart.VerifyReceipt(ctx, r, input, o)
	wantReason(t, claims, err, ReceiptIssuer)
	spec = goodSpec(t, s, input)
	spec.claims["iss"] = o.Issuer
	if _, err := c.Rampart.VerifyReceipt(ctx, s.sign(spec), input, o); err != nil {
		t.Fatal(err)
	}

	// A feed without an issuer and no option: refused, not passed.
	o = verifyOpts(s)
	o.Keys = &JWKS{Keys: []JWK{s.jwk()}}
	claims, err = c.Rampart.VerifyReceipt(ctx, r, input, o)
	wantReason(t, claims, err, ReceiptIssuer)
	if !strings.Contains(err.Detail, "no issuer is advertised") {
		t.Fatalf("detail: %q", err.Detail)
	}
	// ...but the option alone is enough.
	o.Issuer = rampartIssuerURL
	if _, err := c.Rampart.VerifyReceipt(ctx, r, input, o); err != nil {
		t.Fatal(err)
	}

	// A bare receipt string with no project: project_unknown, not project.
	o = verifyOpts(s)
	o.ProjectID = ""
	claims, err = c.Rampart.VerifyReceipt(ctx, r, input, o)
	wantReason(t, claims, err, ReceiptProjectUnknown)

	// The classify result carries the project; a mismatch is project.
	res := &RampartClassification{ClassificationReceipt: r, ProjectID: rampartProject}
	if _, err := c.Rampart.VerifyReceipt(ctx, res, input, o); err != nil {
		t.Fatalf("pointer result: %v", err)
	}
	if _, err := c.Rampart.VerifyReceipt(ctx, *res, input, o); err != nil {
		t.Fatalf("value result: %v", err)
	}
	other := &RampartClassification{ClassificationReceipt: r, ProjectID: "proj_other"}
	claims, err = c.Rampart.VerifyReceipt(ctx, other, input, o)
	wantReason(t, claims, err, ReceiptProject)
	// The option wins over the result's project.
	o.ProjectID = rampartProject
	if _, err := c.Rampart.VerifyReceipt(ctx, other, input, o); err != nil {
		t.Fatalf("option must override: %v", err)
	}
	// Anything else is not a receipt.
	claims, err = c.Rampart.VerifyReceipt(ctx, 5, input, o)
	wantReason(t, claims, err, ReceiptMalformed)
	claims, err = c.Rampart.VerifyReceipt(ctx, (*RampartClassification)(nil), input, o)
	wantReason(t, claims, err, ReceiptMalformed)
}

func TestVerifyReceiptUncanonicalisableInput(t *testing.T) {
	s := newSigner(t, "k1")
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request expected"); return nil })
	good := map[string]any{"text": "hello"}
	r := s.sign(goodSpec(t, s, good))
	for name, in := range map[string]any{
		"fraction": map[string]any{"n": 1.5},
		"2^53":     map[string]any{"n": int64(9007199254740992)},
	} {
		claims, err := c.Rampart.VerifyReceipt(context.Background(), r, in, verifyOpts(s))
		wantReason(t, claims, err, ReceiptInputHash)
		if !strings.Contains(err.Detail, "cannot be canonicalised") {
			t.Errorf("%s: detail %q", name, err.Detail)
		}
	}
}

func TestClassifyDoesNotPrevalidateNumbers(t *testing.T) {
	var body string
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
		return jsonResponse(400, map[string]any{"code": "K9O_INVALID_FIELD_VALUE", "message": "input has a fractional number"})
	})
	_, _, kerr := c.Rampart.Classify(context.Background(), map[string]any{"n": 1.5}, ClassifyOptions{})
	if kerr == nil || kerr.Status != 400 || !strings.Contains(body, "1.5") {
		t.Fatalf("the server answers, the SDK sends: kerr=%v body=%s", kerr, body)
	}
}

func TestVerifyReceiptDefaultsFromClassify(t *testing.T) {
	s := newSigner(t, "k1")
	input := map[string]any{"text": "hello"}
	spec := goodSpec(t, s, input)
	spec.claims["iat"], spec.claims["exp"] = time.Now().Unix(), time.Now().Unix()+3600
	receipt := s.sign(spec)
	feeds := 0
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		if req.URL.Path == "/rampart/jwks" {
			feeds++
			return jsonResponse(200, map[string]any{"keys": []JWK{s.jwk()}, "issuer": rampartIssuerURL})
		}
		return jsonResponse(200, map[string]any{
			"data": map[string]any{"classification": "suspicious", "riskScore": 6200, "confidence": 8100,
				"categories": []string{"prompt_injection"}, "calibrationVersion": "cal-1", "classificationReceipt": receipt, "projectId": rampartProject},
			"meta": map[string]any{"context": map[string]any{"requestId": "r"}},
		})
	})
	ctx := context.Background()
	res, _, kerr := c.Rampart.Classify(ctx, input, ClassifyOptions{})
	if kerr != nil {
		t.Fatal(kerr)
	}
	// Issuer from the key feed, project from the classify result, keys fetched.
	claims, err := c.Rampart.VerifyReceipt(ctx, res, input, VerifyReceiptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claims.Classification != "suspicious" || feeds != 1 {
		t.Fatalf("claims=%+v feeds=%d", claims, feeds)
	}
	if _, err := c.Rampart.VerifyReceipt(ctx, res, input, VerifyReceiptOptions{}); err != nil || feeds != 1 {
		t.Fatalf("second verify must use the cache: err=%v feeds=%d", err, feeds)
	}
}

func TestVerifyReceiptRefetchesOnceOnUnknownKid(t *testing.T) {
	old := newSigner(t, "k-old")
	rolled := newSigner(t, "k-new")
	input := map[string]any{"text": "hello"}
	feed := []JWK{old.jwk()}
	feeds := 0
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		feeds++
		return jsonResponse(200, map[string]any{"keys": feed, "issuer": rampartIssuerURL})
	})
	ctx := context.Background()
	mk := func(sg *signer) string {
		spec := goodSpec(t, sg, input)
		spec.claims["iat"], spec.claims["exp"] = time.Now().Unix(), time.Now().Unix()+3600
		return sg.sign(spec)
	}
	o := VerifyReceiptOptions{ProjectID: rampartProject}
	if _, err := c.Rampart.VerifyReceipt(ctx, mk(old), input, o); err != nil || feeds != 1 {
		t.Fatalf("first: err=%v feeds=%d", err, feeds)
	}
	if _, err := c.Rampart.VerifyReceipt(ctx, mk(old), input, o); err != nil || feeds != 1 {
		t.Fatalf("cached kid must not refetch: err=%v feeds=%d", err, feeds)
	}
	// Unknown kid, still not in the feed: exactly one refetch, then a refusal.
	claims, err := c.Rampart.VerifyReceipt(ctx, mk(rolled), input, o)
	wantReason(t, claims, err, ReceiptKeyUnknown)
	if feeds != 2 {
		t.Fatalf("one refetch expected, feeds=%d", feeds)
	}
	// The feed rolls over: the next unknown-kid receipt refetches once and verifies.
	feed = []JWK{rolled.jwk(), old.jwk()}
	if _, err := c.Rampart.VerifyReceipt(ctx, mk(rolled), input, o); err != nil || feeds != 3 {
		t.Fatalf("rollover: err=%v feeds=%d", err, feeds)
	}
	if _, err := c.Rampart.VerifyReceipt(ctx, mk(rolled), input, o); err != nil || feeds != 3 {
		t.Fatalf("rolled key cached: err=%v feeds=%d", err, feeds)
	}
}

func TestVerifyReceiptFeedUnavailableFailsClosed(t *testing.T) {
	s := newSigner(t, "k1")
	input := map[string]any{"text": "hello"}
	c := rampartTestClient(t, func(*http.Request) *http.Response {
		return jsonResponse(500, map[string]any{"code": "K9O_INTERNAL_ERROR", "message": "boom"})
	})
	claims, err := c.Rampart.VerifyReceipt(context.Background(), s.sign(goodSpec(t, s, input)), input,
		VerifyReceiptOptions{ProjectID: rampartProject, Now: func() time.Time { return rampartNow }})
	wantReason(t, claims, err, ReceiptKeyUnavailable)
	if err.Cause == nil || err.Cause.Code != "K9O_INTERNAL_ERROR" {
		t.Fatalf("cause: %+v", err.Cause)
	}
}
