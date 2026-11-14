package k9o

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// ReceiptType is the `typ` of a classification receipt.
const ReceiptType = "k9o.rampart.classify.v1"

const (
	rampartDefaultSkew = 60 * time.Second
	rampartMaxBody     = 1 << 20
)

// RampartClassification is the answer to Rampart.Classify.
//
// Every field is read from the unsigned response body. To rely on a verdict,
// pass ClassificationReceipt and the same input to Rampart.VerifyReceipt and
// use the claims it returns. Rampart is advisory: nothing here blocks anything,
// the caller decides what to do with the verdict.
type RampartClassification struct {
	// Classification is "clear", "suspicious" or "malicious". Uncertain input
	// comes back "suspicious".
	Classification string `json:"classification"`
	// RiskScore is the risk in basis points, 0 to 10000.
	RiskScore int `json:"riskScore"`
	// Confidence is the classifier's confidence in basis points, 0 to 10000.
	Confidence int `json:"confidence"`
	// Categories names what the classifier found; it can be empty.
	Categories []string `json:"categories"`
	// CalibrationVersion names the calibration that produced the verdict.
	CalibrationVersion string `json:"calibrationVersion"`
	// ClassificationReceipt is the signed receipt (a compact EdDSA JWS), exactly as received.
	ClassificationReceipt string `json:"classificationReceipt"`
	// ProjectID is the project the receipt was issued for (proj_...). Pass the
	// whole result to VerifyReceipt and the receipt's project_id is checked
	// against it. Empty when the server does not report it.
	ProjectID string `json:"projectId"`
}

// ClassifyOptions configures Rampart.Classify.
type ClassifyOptions struct {
	// Label is an optional context label for the classifier, 1 to 128 characters.
	Label string
}

// JWK is one public key of a key set. Receipts are signed with Ed25519, so a
// usable key has Kty "OKP", Crv "Ed25519" and X, the base64url public key.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
}

// JWKS is a JSON Web Key Set: the answer of Rampart.JWKS.
type JWKS struct {
	Keys []JWK `json:"keys"`
	// Issuer is the deployment's receipt issuer (its "<issuer>/rampart"), the
	// value a receipt's iss must equal. Empty when the feed does not advertise one.
	Issuer string `json:"issuer,omitempty"`
}

// Receipt refusal reasons: the closed set of ReceiptError.Reason.
const (
	// ReceiptMalformed: the receipt is not a compact JWS, or a field has the wrong shape.
	ReceiptMalformed = "malformed"
	// ReceiptKeyUnknown: no key in the key feed has the receipt's kid (or the key is not an Ed25519 key).
	ReceiptKeyUnknown = "key_unknown"
	// ReceiptKeyUnavailable: the key feed could not be read, so nothing was verified.
	ReceiptKeyUnavailable = "key_unavailable"
	// ReceiptAlg: the header alg is not EdDSA.
	ReceiptAlg = "alg"
	// ReceiptTyp: the header typ is not k9o.rampart.classify.v1.
	ReceiptTyp = "typ"
	// ReceiptSignature: the signature does not verify.
	ReceiptSignature = "signature"
	// ReceiptIssuer: iss is not the expected issuer, or no issuer is advertised and none was given.
	ReceiptIssuer = "issuer"
	// ReceiptProject: project_id is not the expected project.
	ReceiptProject = "project"
	// ReceiptProjectUnknown: the expected project is not known (a bare receipt string and no ProjectID option).
	ReceiptProjectUnknown = "project_unknown"
	// ReceiptExpired: the receipt is past its exp (beyond the clock skew).
	ReceiptExpired = "expired"
	// ReceiptNotYetValid: the receipt's iat is in the future (beyond the clock skew).
	ReceiptNotYetValid = "not_yet_valid"
	// ReceiptInputHash: input_sha256 is not the hash of the input you passed.
	ReceiptInputHash = "input_hash"
)

// ReceiptError is the refusal of Rampart.VerifyReceipt. Reason is one of the
// Receipt* constants; Detail is a short explanation. No verdict accompanies it.
type ReceiptError struct {
	Reason string
	Detail string
	// Cause is set for ReceiptKeyUnavailable: why the key feed could not be read.
	Cause *KError
}

func (e *ReceiptError) Error() string {
	return "rampart receipt refused (" + e.Reason + "): " + e.Detail
}

// ReceiptClaims are the verified claims of a receipt. They are returned only
// after every check passed.
type ReceiptClaims struct {
	Classification     string
	RiskBP             int
	ConfidenceBP       int
	Categories         []string
	CalibrationVersion string
	ProjectID          string
	Issuer             string
	IssuedAt           time.Time
	ExpiresAt          time.Time
	// InputSHA256 is the lowercase hex SHA-256 of the canonical input.
	InputSHA256 string
	// KeyID is the kid of the key that verified the receipt.
	KeyID string
	// Windows is the number of windows the input was scored in (1 when it fit
	// one window). A receipt without a windows claim reads as 1 too: an input
	// that fits one window scores one.
	Windows int
}

// Error codes of the classifier worth narrowing on.
const (
	// CodeRampartInputTooLong (HTTP 413, Context["reason"] "rampart_input_too_long"):
	// the input needs more than 16 windows (about 6,000 tokens). There is no
	// verdict; send a shorter input.
	CodeRampartInputTooLong = "K9O_RAMPART_INPUT_TOO_LONG"
	// CodeRampartClassifierUnavailable (HTTP 503): the classifier could not
	// answer. There is no verdict; never treat it as clear.
	CodeRampartClassifierUnavailable = "K9O_RAMPART_CLASSIFIER_UNAVAILABLE"
)

// VerifyReceiptOptions configures Rampart.VerifyReceipt. Every field is optional.
type VerifyReceiptOptions struct {
	// Keys is a key set you already fetched. When set, no request is made and
	// an unknown kid is refused at once.
	Keys *JWKS
	// Issuer is the expected iss. Default: the issuer the key feed advertises.
	// With neither, the receipt is refused with ReceiptIssuer.
	Issuer string
	// ProjectID is the expected project_id claim (the project's id, not its
	// slug). Default: the ProjectID of the classify result passed to
	// VerifyReceipt. With neither (a bare receipt string), the receipt is
	// refused with ReceiptProjectUnknown.
	ProjectID string
	// ClockSkew is the tolerance for iat and exp. Zero means 60 seconds; a
	// negative value means none.
	ClockSkew time.Duration
	// Now replaces the clock (for tests).
	Now func() time.Time
}

// RampartNamespace exposes client.Rampart.*: hosted classification of content,
// with a signed receipt. Rampart is advisory. It never blocks: Classify returns
// a verdict or an error, and the caller decides what to do. It fails closed in
// the sense that an unavailable classifier is an error (503
// K9O_RAMPART_CLASSIFIER_UNAVAILABLE), never a pass.
type RampartNamespace struct {
	config *httpClientConfig

	mu     sync.Mutex
	keys   map[string]JWK
	issuer string
}

func newRampartNamespace(cfg *httpClientConfig) *RampartNamespace {
	return &RampartNamespace{config: cfg, keys: map[string]JWK{}}
}

// Classify sends input (a JSON object) to the classifier and returns its
// verdict, the signed receipt and the project id (the result's ProjectID). Any project member may call it; a call is
// metered when it answers 200. Errors arrive as *KError:
//   - 413 K9O_RAMPART_INPUT_TOO_LONG: the input needs more than 16 windows
//     (about 6,000 tokens). There is no verdict; send a shorter input.
//   - 503 K9O_RAMPART_CLASSIFIER_UNAVAILABLE: the classifier is down. There is
//     no verdict; treat it as "not classified", never as clear.
//   - 403 and 404 for a caller who is not a project member.
//
// A long input is scored in overlapping windows of up to 512 tokens and the
// highest risk decides; the receipt's windows claim (ReceiptClaims.Windows)
// says how many were scored.
//
// The verdict fields are unsigned. Verify the receipt before relying on them.
func (n *RampartNamespace) Classify(ctx context.Context, input any, opts ClassifyOptions) (*RampartClassification, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "rampart")
	if kerr != nil {
		return nil, nil, kerr
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return nil, nil, invalidField("input must be a JSON object (a map or a struct)", nil)
	}
	body := map[string]any{"input": json.RawMessage(raw)}
	if opts.Label != "" {
		if l := len(utf16.Encode([]rune(opts.Label))); l < 1 || l > 128 {
			return nil, nil, invalidField("label must be 1 to 128 characters", map[string]any{"field": "label"})
		}
		body["label"] = opts.Label
	}
	env, kerr := doRequest[RampartClassification](ctx, n.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "rampart/classify"),
		Body:   body,
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// JWKS reads the public keys that sign receipts (GET /rampart/jwks on the
// client's base URL; public, so no credential is sent). Keys stay listed for a
// while after a rollover, so a receipt signed just before one still resolves.
// Issuer is the receipt issuer the deployment advertises, when it does.
func (n *RampartNamespace) JWKS(ctx context.Context) (*JWKS, *KError) {
	urlStr := strings.TrimRight(n.config.resolveOrigin(ctx), "/") + "/rampart/jwks"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to build request: %v", err)}
	}
	req.Header = n.config.buildHeaders(&requestInit{NoCredential: true})
	hc := n.config.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &KError{
			Code:    "K9O_UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("network error reaching %s: %v", urlStr, err),
			Context: map[string]any{"url": urlStr},
		}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, rampartMaxBody))
	if err != nil {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to read response body from %s: %v", urlStr, err), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var wire KErrorWire
		if json.Unmarshal(b, &wire) == nil && wire.Code != "" && wire.Message != "" {
			return nil, withRequestID(kerrorFromWire(&wire, resp.StatusCode), resp.Header)
		}
		return nil, withRequestID(&KError{
			Code:    "K9O_INTERNAL_ERROR",
			Message: fmt.Sprintf("upstream returned %d with non-KError body", resp.StatusCode),
			Context: map[string]any{"url": urlStr, "status": resp.StatusCode},
			Status:  resp.StatusCode,
		}, resp.Header)
	}
	var set JWKS
	if err := json.Unmarshal(b, &set); err != nil || set.Keys == nil {
		return nil, &KError{
			Code:    "K9O_INTERNAL_ERROR",
			Message: fmt.Sprintf("malformed key set from %s", urlStr),
			Context: map[string]any{"url": urlStr, "status": resp.StatusCode},
			Status:  resp.StatusCode,
		}
	}
	return &set, nil
}

func receiptRefuse(reason, format string, a ...any) *ReceiptError {
	return &ReceiptError{Reason: reason, Detail: fmt.Sprintf(format, a...)}
}

func b64url(s string) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return b, err == nil
}

// VerifyReceipt checks receipt against input, the exact object you classified,
// and returns its verified claims. receipt is the *RampartClassification (or
// RampartClassification) that Classify returned, which carries the receipt and
// its project id, or a bare receipt string (then set opts.ProjectID). The checks run in this order and the first
// failure is returned as a *ReceiptError with its Reason; no claims come back
// then:
//
//  1. the key is the one whose kid the receipt's header names, taken from the
//     key feed (never from the receipt);
//  2. alg is EdDSA;
//  3. typ is k9o.rampart.classify.v1;
//  4. the signature verifies (Ed25519);
//  5. iss is the expected issuer (opts.Issuer, else the one the key feed
//     advertises), project_id is the expected project (opts.ProjectID, else
//     the classify result's ProjectID), and iat and exp are within the clock
//     skew;
//  6. input_sha256 equals the SHA-256 of the canonical form of input
//     (CanonicalJSON), recomputed here. An input that cannot be canonicalised,
//     for example one holding a fractional number, is refused as ReceiptInputHash.
//
// The key feed is read from the client's base URL when opts.Keys is nil. Keys
// and the advertised issuer are cached for the client's life; an unknown kid
// triggers one refetch per call. If the feed cannot be read the receipt is
// refused (ReceiptKeyUnavailable).
func (n *RampartNamespace) VerifyReceipt(ctx context.Context, receiptOrResult any, input any, opts VerifyReceiptOptions) (*ReceiptClaims, *ReceiptError) {
	var receipt, carriedProject string
	switch v := receiptOrResult.(type) {
	case string:
		receipt = v
	case *RampartClassification:
		if v == nil {
			return nil, receiptRefuse(ReceiptMalformed, "the classify result is nil")
		}
		receipt, carriedProject = v.ClassificationReceipt, v.ProjectID
	case RampartClassification:
		receipt, carriedProject = v.ClassificationReceipt, v.ProjectID
	default:
		return nil, receiptRefuse(ReceiptMalformed, "pass a receipt string or the result of Classify")
	}
	parts := strings.Split(receipt, ".")
	if len(parts) != 3 {
		return nil, receiptRefuse(ReceiptMalformed, "a receipt has three dot-separated parts")
	}
	hb, ok1 := b64url(parts[0])
	pb, ok2 := b64url(parts[1])
	sig, ok3 := b64url(parts[2])
	if !ok1 || !ok2 || !ok3 {
		return nil, receiptRefuse(ReceiptMalformed, "a part is not base64url")
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &header) != nil {
		return nil, receiptRefuse(ReceiptMalformed, "the header is not a JSON object")
	}
	var claims map[string]any
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	if dec.Decode(&claims) != nil || claims == nil {
		return nil, receiptRefuse(ReceiptMalformed, "the payload is not a JSON object")
	}
	if header.Kid == "" {
		return nil, receiptRefuse(ReceiptKeyUnknown, "the header has no kid")
	}

	// 1. key by kid
	jwk, feedIssuer, rerr := n.findKey(ctx, header.Kid, opts.Keys)
	if rerr != nil {
		return nil, rerr
	}
	if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
		return nil, receiptRefuse(ReceiptKeyUnknown, "key %q is not an Ed25519 key", header.Kid)
	}
	xb, ok := b64url(jwk.X)
	if !ok || len(xb) != ed25519.PublicKeySize {
		return nil, receiptRefuse(ReceiptKeyUnknown, "key %q has an unusable public key", header.Kid)
	}
	// 2. alg, 3. typ
	if header.Alg != "EdDSA" {
		return nil, receiptRefuse(ReceiptAlg, "alg must be EdDSA")
	}
	if header.Typ != ReceiptType {
		return nil, receiptRefuse(ReceiptTyp, "typ must be %s", ReceiptType)
	}
	// signature over the signing input as received
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(xb), []byte(parts[0]+"."+parts[1]), sig) {
		return nil, receiptRefuse(ReceiptSignature, "the signature does not verify")
	}

	// 4. iss, project, time
	iss, _ := claims["iss"].(string)
	wantIss := opts.Issuer
	if wantIss == "" {
		wantIss = feedIssuer
	}
	if wantIss == "" {
		return nil, receiptRefuse(ReceiptIssuer, "no issuer is advertised by the key feed and none was given: pass VerifyReceiptOptions.Issuer")
	}
	if iss != wantIss {
		return nil, receiptRefuse(ReceiptIssuer, "iss is not the expected issuer")
	}
	pid, _ := claims["project_id"].(string)
	wantPid := opts.ProjectID
	if wantPid == "" {
		wantPid = carriedProject
	}
	if wantPid == "" {
		return nil, receiptRefuse(ReceiptProjectUnknown, "the project id is not known: pass the result of Classify or VerifyReceiptOptions.ProjectID")
	}
	if pid != wantPid {
		return nil, receiptRefuse(ReceiptProject, "project_id is not the expected project")
	}
	iat, okI := claims["iat"].(float64)
	exp, okE := claims["exp"].(float64)
	if !okI || !okE {
		return nil, receiptRefuse(ReceiptMalformed, "iat and exp must be numbers")
	}
	skew := opts.ClockSkew
	switch {
	case skew == 0:
		skew = rampartDefaultSkew
	case skew < 0:
		skew = 0
	}
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	nowS := float64(now.UnixNano()) / 1e9
	if iat > nowS+skew.Seconds() {
		return nil, receiptRefuse(ReceiptNotYetValid, "the receipt was issued in the future")
	}
	if exp+skew.Seconds() < nowS {
		return nil, receiptRefuse(ReceiptExpired, "the receipt has expired")
	}

	// 5. input hash, recomputed
	canon, err := CanonicalJSON(input)
	if err != nil {
		return nil, receiptRefuse(ReceiptInputHash, "the input cannot be canonicalised: %v", err)
	}
	sum := sha256.Sum256([]byte(canon))
	want := hex.EncodeToString(sum[:])
	got, _ := claims["input_sha256"].(string)
	if got == "" {
		return nil, receiptRefuse(ReceiptMalformed, "input_sha256 is missing")
	}
	if got != want {
		return nil, receiptRefuse(ReceiptInputHash, "the receipt does not attest this input")
	}

	// 6. the verdict, from the verified claims
	out := &ReceiptClaims{
		Issuer:      iss,
		ProjectID:   pid,
		InputSHA256: got,
		KeyID:       header.Kid,
		IssuedAt:    time.Unix(0, int64(iat*1e9)).UTC(),
		ExpiresAt:   time.Unix(0, int64(exp*1e9)).UTC(),
	}
	cls, _ := claims["classification"].(string)
	if cls != "clear" && cls != "suspicious" && cls != "malicious" {
		return nil, receiptRefuse(ReceiptMalformed, "classification is not clear, suspicious or malicious")
	}
	out.Classification = cls
	risk, okR := claims["risk_bp"].(float64)
	conf, okC := claims["confidence_bp"].(float64)
	if !okR || !okC || risk != float64(int(risk)) || conf != float64(int(conf)) {
		return nil, receiptRefuse(ReceiptMalformed, "risk_bp and confidence_bp must be whole numbers")
	}
	out.RiskBP, out.ConfidenceBP = int(risk), int(conf)
	cats, okCats := claims["categories"].([]any)
	if !okCats {
		return nil, receiptRefuse(ReceiptMalformed, "categories must be a list")
	}
	out.Categories = make([]string, 0, len(cats))
	for _, c := range cats {
		s, ok := c.(string)
		if !ok {
			return nil, receiptRefuse(ReceiptMalformed, "categories must be strings")
		}
		out.Categories = append(out.Categories, s)
	}
	cv, okCV := claims["calibration_version"].(string)
	if !okCV {
		return nil, receiptRefuse(ReceiptMalformed, "calibration_version must be a string")
	}
	out.CalibrationVersion = cv
	// windows is optional and reads as 1 when absent; any other claim is ignored.
	out.Windows = 1
	if w, present := claims["windows"]; present && w != nil {
		wf, okW := w.(float64)
		if !okW || wf != float64(int(wf)) || wf < 1 {
			return nil, receiptRefuse(ReceiptMalformed, "windows must be a whole number, 1 or more")
		}
		out.Windows = int(wf)
	}
	return out, nil
}

// findKey resolves a kid, and returns the issuer that goes with the key set:
// from the supplied key set alone, else from the cache, refetching the feed
// once when the kid is not cached.
func (n *RampartNamespace) findKey(ctx context.Context, kid string, supplied *JWKS) (JWK, string, *ReceiptError) {
	if supplied != nil {
		for _, k := range supplied.Keys {
			if k.Kid == kid {
				return k, supplied.Issuer, nil
			}
		}
		return JWK{}, "", receiptRefuse(ReceiptKeyUnknown, "no key with kid %q in the key set", kid)
	}
	n.mu.Lock()
	k, ok := n.keys[kid]
	iss := n.issuer
	n.mu.Unlock()
	if ok {
		return k, iss, nil
	}
	set, kerr := n.JWKS(ctx)
	if kerr != nil {
		return JWK{}, "", &ReceiptError{Reason: ReceiptKeyUnavailable, Detail: "the key feed could not be read: " + kerr.Message, Cause: kerr}
	}
	n.mu.Lock()
	for _, key := range set.Keys {
		if key.Kid != "" {
			n.keys[key.Kid] = key
		}
	}
	n.issuer = set.Issuer
	k, ok = n.keys[kid]
	n.mu.Unlock()
	if !ok {
		return JWK{}, "", receiptRefuse(ReceiptKeyUnknown, "no key with kid %q in the key feed", kid)
	}
	return k, set.Issuer, nil
}
