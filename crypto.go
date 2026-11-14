package k9o

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Crypto error codes the router raises or reacts to.
const (
	// CryptoCodeCryptNotFound is the 404 the gateway can answer on encrypt
	// when the crypt name is not registered. It passes through unchanged.
	CryptoCodeCryptNotFound = "K9O_CRYPT_NOT_FOUND"
	// CryptoCodeCryptRetired is the 409 the gateway can answer on encrypt when
	// the crypt name is retired. Decrypt and rewrap are not refused for it.
	CryptoCodeCryptRetired = "K9O_CRYPT_RETIRED"
	// CryptoCodeZKStoreFlowRequired is the 409 the gateway answers when the
	// call belongs on the client-side route.
	CryptoCodeZKStoreFlowRequired = "K9O_ZK_STORE_FLOW_REQUIRED"
	// CryptoCodeClientCoreUnavailable is raised (status 501) when a call needs
	// the client route and no client core is registered.
	CryptoCodeClientCoreUnavailable = "K9O_ZK_CLIENT_CORE_UNAVAILABLE"
	// CryptoCodeCustodyUnknown is raised by the SDK (no HTTP status) when the
	// custody mode answer is unknown, missing or malformed; nothing was sent.
	CryptoCodeCustodyUnknown = "K9O_CRYPTO_CUSTODY_UNKNOWN"
	// CryptoCodeKeyPinUnenforced is raised by the SDK (no HTTP status) before
	// the call reaches the client core: ExpectedKeyRefs is set and the core
	// does not declare that it enforces it. Nothing was sent.
	CryptoCodeKeyPinUnenforced = "K9O_CRYPTO_KEY_PIN_UNENFORCED"
)

// Custody modes `GET /crypto/mode` can answer.
const (
	CustodyPlatformManaged      = "PLATFORM_MANAGED"
	CustodyConfidentialWorkload = "CONFIDENTIAL_WORKLOAD"
	CustodySKR                  = "SKR"
	CustodyBYOK                 = "BYOK"
	CustodyHYOK                 = "HYOK"
)

// Limits of the crypto resource.
const (
	// CryptoMaxItems is the most items in one call.
	CryptoMaxItems = 100
	// CryptoMaxItemBytes is the most plaintext bytes in one item (64 KiB).
	CryptoMaxItemBytes = 64 * 1024
	// CryptoMaxCallBytes is the most plaintext bytes in one call (1 MiB).
	CryptoMaxCallBytes = 1024 * 1024
	// CryptoMaxContextBytes is the most bytes in an item's context.
	CryptoMaxContextBytes = 256
)

// The name pattern is ^(?!actions$)[a-z0-9-]{1,64}$; Go's regexp has no
// lookahead, so the "actions" exclusion is checked beside this expression.
var cryptoCryptPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// CryptoEncryptItem is one value to encrypt.
type CryptoEncryptItem struct {
	// Plaintext is the bytes to encrypt, at most CryptoMaxItemBytes.
	Plaintext []byte
	// Context is optional text of at most CryptoMaxContextBytes bytes that
	// binds the ciphertext: decrypt must give the same crypt and context.
	// Do not put secrets in it; it is not encrypted.
	Context string
}

// CryptoText is a CryptoEncryptItem for a UTF-8 string.
func CryptoText(text, context string) CryptoEncryptItem {
	return CryptoEncryptItem{Plaintext: []byte(text), Context: context}
}

// CryptoCiphertextItem is one ciphertext to decrypt or rewrap.
type CryptoCiphertextItem struct {
	// Ciphertext is exactly the string encrypt returned. The SDK never reads it.
	Ciphertext string
	// Context must be the one given on encrypt (empty if none was).
	Context string
}

// CryptoItemResult is the result of one item of a batch: a Ciphertext
// (encrypt, rewrap), a Plaintext (decrypt), or a Code and Message when that
// item failed. The item shape follows the gateway; fields the SDK does not
// know are kept in Extra.
type CryptoItemResult struct {
	// Ciphertext is set on a successful encrypt or rewrap item.
	Ciphertext string
	// Plaintext is set on a successful decrypt item (base64 already decoded).
	Plaintext []byte
	// Code and Message are set when this item failed, for example
	// K9O_ENVELOPE_INTEGRITY_FAILURE.
	Code    string
	Message string
	// Extra holds any other field of the item, as received.
	Extra map[string]json.RawMessage
}

// OK reports whether the item succeeded (it carries no Code).
func (r CryptoItemResult) OK() bool { return r.Code == "" }

// Text returns Plaintext as a string.
func (r CryptoItemResult) Text() string { return string(r.Plaintext) }

// Err returns the item's failure as a *KError (Status 0: it is not an HTTP
// status), or nil when the item succeeded.
func (r CryptoItemResult) Err() *KError {
	if r.Code == "" {
		return nil
	}
	return &KError{Code: r.Code, Message: r.Message}
}

// CryptoResult is the answer to Encrypt, Decrypt and Rewrap: one result per
// item, in the order of the request.
type CryptoResult struct {
	Items []CryptoItemResult
}

// CryptoNamespace exposes client.Crypto.*: server-side encryption of
// application data under the project's key. The caller never names a key.
type CryptoNamespace struct {
	config *httpClientConfig

	mu   sync.RWMutex
	core CryptoClientCore
}

// CryptoClientCore is the hook the Kibo client binding for Go fills. When a
// project's key is held in your own KMS or HSM (BYOK or HYOK), the router
// hands the call to the registered core, which encrypts and decrypts in your
// process so the platform never sees plaintext. A core knows nothing about
// custody epochs. Register one with Crypto.UseClientCore.
//
// A core that enforces ExpectedKeyRefs declares it by also implementing
// CryptoCoreEnforcer and returning true; see there.
type CryptoClientCore interface {
	Encrypt(ctx context.Context, crypt string, items []CryptoEncryptItem, opts CryptoCoreOptions) (*CryptoResult, *KError)
	Decrypt(ctx context.Context, crypt string, items []CryptoCiphertextItem, opts CryptoCoreOptions) (*CryptoResult, *KError)
	Rewrap(ctx context.Context, crypt string, items []CryptoCiphertextItem, opts CryptoCoreOptions) (*CryptoResult, *KError)
}

// CryptoCoreEnforcer is the optional interface through which a client core
// declares that it enforces ClientOpts.ExpectedKeyRefs: it refuses a key that
// is not in CryptoCoreOptions.ExpectedKeyRefs before it uses any token or
// makes any network call. A core declares it by implementing the interface
// and returning true. When ExpectedKeyRefs is set and the core does not
// declare it, the router refuses the call with K9O_CRYPTO_KEY_PIN_UNENFORCED
// before the core is called, because a pin the core ignores would look like a
// pin.
type CryptoCoreEnforcer interface {
	EnforcesExpectedKeyRefs() bool
}

// CryptoCoreOptions is what the router passes to the client core on every
// call. ExpectedKeyRefs is the list set in ClientOpts. When it is non-nil the
// core must refuse a key whose reference (version ignored, see KeyRefMatches)
// is not in it, before it uses any token or makes any network call. It is nil
// when the option was not set. The slice is a copy; the core may keep it.
type CryptoCoreOptions struct {
	ExpectedKeyRefs []string
}

func (n *CryptoNamespace) coreOptions() CryptoCoreOptions {
	o := CryptoCoreOptions{}
	if n.config.ExpectedKeyRefs != nil {
		o.ExpectedKeyRefs = append([]string(nil), n.config.ExpectedKeyRefs...)
	}
	return o
}

// checkExpectedKeyRefs validates ClientOpts.ExpectedKeyRefs and returns a copy
// (nil when not set). It must be a non-empty list of non-blank entries without
// whitespace, and none may contain a wildcard, since a wildcard pins nothing.
func checkExpectedKeyRefs(refs []string) ([]string, *KError) {
	if refs == nil {
		return nil, nil
	}
	bad := func(msg string) *KError {
		return invalidField("expectedKeyRefs: "+msg, map[string]any{"field": "expectedKeyRefs"})
	}
	if len(refs) == 0 {
		return nil, bad("pass a non-empty list of key identifiers, or leave it out")
	}
	for _, r := range refs {
		if strings.TrimSpace(r) == "" || strings.IndexFunc(r, unicode.IsSpace) >= 0 {
			return nil, bad("each entry must be a key identifier without spaces")
		}
		if strings.Contains(r, "*") {
			return nil, bad(`an entry cannot contain "*"; name the exact key`)
		}
	}
	return append([]string(nil), refs...), nil
}

// KeyRefProvider names the provider whose key reference rule KeyRefMatches
// applies.
type KeyRefProvider string

// Key reference providers.
const (
	KeyRefAWS   KeyRefProvider = "aws"
	KeyRefGCP   KeyRefProvider = "gcp"
	KeyRefAzure KeyRefProvider = "azure"
	KeyRefVault KeyRefProvider = "vault"
)

func keyIdentity(ref string, provider KeyRefProvider) string {
	switch provider {
	case KeyRefAWS:
		return ref
	case KeyRefGCP:
		return strings.SplitN(ref, "/cryptoKeyVersions/", 2)[0]
	case KeyRefAzure:
		parts := strings.Split(ref, "/")
		if len(parts) > 5 {
			parts = parts[:5]
		}
		return strings.ToLower(strings.Join(parts, "/"))
	default: // KeyRefVault
		path, key := "", ref
		if cut := strings.LastIndex(ref, "/"); cut >= 0 {
			path, key = ref[:cut], ref[cut+1:]
		}
		if colon := strings.Index(key, ":"); colon >= 0 {
			key = key[:colon]
		}
		return path + "/" + key
	}
}

// KeyRefMatches reports whether keyRef is one of expected, ignoring the key
// version, by the rule of the given provider: AWS compares the whole
// reference; GCP drops "/cryptoKeyVersions/<n>" and what follows; Azure
// compares the first five "/"-separated parts ("https://<host>/keys/<key>")
// in lower case, so a version and the case of the host and key name are
// ignored; Vault compares "<path>/<key>" without a ":<version>" suffix. The
// provider is required and is never inferred from the reference, because a
// Vault reference can look like a URL. A value other than KeyRefAWS,
// KeyRefGCP, KeyRefAzure or KeyRefVault matches nothing. It is for authors of
// a client core; the SDK does not call it.
func KeyRefMatches(keyRef string, expected []string, provider KeyRefProvider) bool {
	switch provider {
	case KeyRefAWS, KeyRefGCP, KeyRefAzure, KeyRefVault:
	default:
		return false
	}
	k := keyIdentity(keyRef, provider)
	for _, e := range expected {
		if keyIdentity(e, provider) == k {
			return true
		}
	}
	return false
}

// UseClientCore registers the client core used when a call needs the
// client-side route. Pass nil to remove it. With none registered, such a call
// raises K9O_ZK_CLIENT_CORE_UNAVAILABLE.
func (n *CryptoNamespace) UseClientCore(core CryptoClientCore) {
	n.mu.Lock()
	n.core = core
	n.mu.Unlock()
}

func (n *CryptoNamespace) clientCore() CryptoClientCore {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.core
}

// checkCorePin refuses the call when ExpectedKeyRefs is set and the core does
// not declare that it enforces it.
func (n *CryptoNamespace) checkCorePin(core CryptoClientCore, custodyMode string) *KError {
	if n.config.ExpectedKeyRefs == nil {
		return nil
	}
	if e, ok := core.(CryptoCoreEnforcer); ok && e.EnforcesExpectedKeyRefs() {
		return nil
	}
	return &KError{
		Code:           CryptoCodeKeyPinUnenforced,
		Message:        "expectedKeyRefs is set but the client core does not declare that it enforces it; nothing was sent",
		Context:        map[string]any{"custodyMode": custodyMode, "requirement": "client-core-enforces-expected-key-refs"},
		Recommendation: "Use a client core that enforces the pin (it implements EnforcesExpectedKeyRefs and returns true), or leave ExpectedKeyRefs out.",
	}
}

func clientCoreUnavailable(custodyMode string) *KError {
	return &KError{
		Code:    CryptoCodeClientCoreUnavailable,
		Message: "this project's key is held in your own KMS; install and register the Kibo client core for this language, or call kibo.encrypt/decrypt directly",
		Context: map[string]any{"custodyMode": custodyMode, "requirement": "kibo-client-core"},
		Status:  501,
	}
}

// CryptoMode is the answer of Mode.
type CryptoMode struct {
	// CustodyMode is who holds the project's key, for example PLATFORM_MANAGED
	// or BYOK.
	CustodyMode string `json:"custodyMode"`
	// Epoch is opaque and changes with every custody transition.
	Epoch string `json:"epoch"`
	// Route is the raw route the gateway named, "server" or "client"; empty
	// when the answer has none. Encrypt reads it on every call.
	Route json.RawMessage `json:"route,omitempty"`
}

// Mode reads the project's custody mode (GET /@<slug>/crypto/mode). Any caller
// with a crypto grant may read it. Encrypt calls it on every call; it is never
// cached.
func (n *CryptoNamespace) Mode(ctx context.Context) (*CryptoMode, *Meta, *KError) {
	return n.readMode(ctx, "")
}

// ModeFor is Mode about one crypt (GET /@<slug>/crypto/mode?crypt=<crypt>): a
// personal access token or workload policy scoped to named crypts is refused
// by Mode and needs this. A bad crypt name is refused with
// K9O_INVALID_FIELD_VALUE before anything is sent.
func (n *CryptoNamespace) ModeFor(ctx context.Context, crypt string) (*CryptoMode, *Meta, *KError) {
	if kerr := checkCrypt(crypt); kerr != nil {
		return nil, nil, kerr
	}
	return n.readMode(ctx, crypt)
}

// readMode is Mode with an optional crypt: Encrypt names the crypt it is about
// to use, so a caller whose grant names only specific crypts is authorized on
// that one. An empty crypt sends no query.
func (n *CryptoNamespace) readMode(ctx context.Context, crypt string) (*CryptoMode, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "crypto")
	if kerr != nil {
		return nil, nil, kerr
	}
	var q map[string]string
	if crypt != "" {
		q = map[string]string{"crypt": crypt}
	}
	env, kerr := doRequest[CryptoMode](ctx, n.config, &requestInit{
		Method: "GET",
		Path:   ProjectPath(slug, "crypto/mode"),
		Query:  q,
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// CryptoFederatedToken is the answer of FederatedToken. Token is a credential:
// keep it out of logs and error messages.
type CryptoFederatedToken struct {
	// Token is a signed RS256 JWT, valid for at most 15 minutes.
	Token string `json:"token"`
	// ExpiresAt is when the token stops being valid (date-time string).
	ExpiresAt string `json:"expiresAt"`
	Issuer    string `json:"issuer"`
	Subject   string `json:"subject"`
	Audience  string `json:"audience"`
	// Kid is the id of the signing key.
	Kid string `json:"kid"`
	// ProjectID and Provider are present when the gateway names them. They are
	// optional. A client core checks them against its own configuration and
	// never follows them.
	ProjectID string `json:"projectId,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

// FederatedToken asks for a short-lived token that your own cloud KMS or HSM
// can trust (POST /@<slug>/crypto/federated-token). Crypt is 1 to 64
// characters of a-z, 0-9 and -, and not "actions"; op is "encrypt", "decrypt" or "rewrap". The SDK
// checks both before sending and refuses a bad value with
// K9O_INVALID_FIELD_VALUE. A call failure is a *KError: 409
// K9O_CUSTODY_SERVER_ROUTE (Context["route"] is "server" or "client-idp"), 429 K9O_RATE_LIMITED, 503
// K9O_KEY_SERVICE_UNAVAILABLE. The SDK never retries it, and an answer without
// the token or any of its fields is K9O_INTERNAL_ERROR. The token is never put
// in an error.
func (n *CryptoNamespace) FederatedToken(ctx context.Context, crypt, op string) (*CryptoFederatedToken, *Meta, *KError) {
	if kerr := checkCrypt(crypt); kerr != nil {
		return nil, nil, kerr
	}
	if op != "encrypt" && op != "decrypt" && op != "rewrap" {
		return nil, nil, invalidField("op must be encrypt, decrypt or rewrap", map[string]any{"field": "op"})
	}
	slug, kerr := requireProjectSlug(n.config, "crypto")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[CryptoFederatedToken](ctx, n.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "crypto/federated-token"),
		Body:   map[string]any{"crypt": crypt, "op": op},
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	d := env.Data
	if d.Token == "" || d.ExpiresAt == "" || d.Issuer == "" || d.Subject == "" || d.Audience == "" || d.Kid == "" {
		return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: "malformed federated-token answer: token, expiresAt, issuer, subject, audience and kid are all required", Status: 200}
	}
	return &d, &env.Meta, nil
}

// parseRoute reads the route of a mode answer: "" when missing, otherwise
// "server" or "client". Anything else (including null and non-strings) is
// unknown.
func parseRoute(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || (s != "server" && s != "client") {
		return "", false
	}
	return s, true
}

func custodyUnknown(msg, seen string) *KError {
	ctx := map[string]any{}
	if seen != "" {
		ctx["custodyMode"] = seen
	}
	return &KError{Code: CryptoCodeCustodyUnknown, Message: msg, Context: ctx}
}

func isZKFlow(kerr *KError) bool {
	return kerr != nil && kerr.Status == 409 && kerr.Code == CryptoCodeZKStoreFlowRequired
}

func zkRoute(kerr *KError) string {
	r, _ := kerr.Context["route"].(string)
	return r
}

func checkCrypt(crypt string) *KError {
	if crypt == "actions" || !cryptoCryptPattern.MatchString(crypt) {
		return invalidField("crypt must be 1 to 64 characters of a-z, 0-9 and -, and not \"actions\"", map[string]any{"field": "crypt"})
	}
	return nil
}

func checkItemCount(n int) *KError {
	if n < 1 || n > CryptoMaxItems {
		return invalidField(fmt.Sprintf("items must hold 1 to %d entries", CryptoMaxItems), map[string]any{"field": "items", "count": n})
	}
	return nil
}

func checkContext(i int, c string) *KError {
	if len(c) > CryptoMaxContextBytes {
		return invalidField(fmt.Sprintf("items[%d].context is over %d bytes", i, CryptoMaxContextBytes), map[string]any{"field": "context", "index": i})
	}
	return nil
}

func tooLarge(message string, ctx map[string]any) *KError {
	return &KError{Code: "K9O_PAYLOAD_TOO_LARGE", Message: message, Context: ctx, Status: 413}
}

// Encrypt encrypts up to 100 values under the project's key (POST
// /@<slug>/crypto/encrypt). Crypt is a registered name and is required (1 to
// 64 characters of a-z, 0-9 and -, and not "actions"); it is also the unit IAM
// grants on. The SDK checks the crypt,
// the item count and the sizes before sending: an item over 64 KiB or a call
// over 1 MiB of plaintext is refused with K9O_PAYLOAD_TOO_LARGE, a bad crypt,
// count or context with K9O_INVALID_FIELD_VALUE.
//
// Each item has its own result (CryptoItemResult.Ciphertext or Code). A call
// failure is a *KError: 429 K9O_RATE_LIMITED (RetryAfterSec), 503
// K9O_KEY_SERVICE_UNAVAILABLE (fail-closed), 404 K9O_CRYPT_NOT_FOUND (the name
// is not registered), 409 K9O_CRYPT_RETIRED (the name is retired; decrypt and
// rewrap are not refused for either), 409 K9O_ZK_STORE_FLOW_REQUIRED
// (the project's key is in the customer's own KMS or HSM; use client-side
// kibo.encrypt). The SDK never retries a crypto call.
//
// Encrypt checks the project's custody mode on every call and never sends
// plaintext to the platform when the key is in your own KMS or HSM. For BYOK
// and HYOK it uses the registered client core (UseClientCore), or raises 501
// K9O_ZK_CLIENT_CORE_UNAVAILABLE without sending anything. An unknown mode or
// a failed mode check fails closed.
func (n *CryptoNamespace) Encrypt(ctx context.Context, crypt string, items []CryptoEncryptItem) (*CryptoResult, *Meta, *KError) {
	if kerr := checkCrypt(crypt); kerr != nil {
		return nil, nil, kerr
	}
	if kerr := checkItemCount(len(items)); kerr != nil {
		return nil, nil, kerr
	}
	total := 0
	wire := make([]map[string]any, len(items))
	for i, it := range items {
		if kerr := checkContext(i, it.Context); kerr != nil {
			return nil, nil, kerr
		}
		if len(it.Plaintext) > CryptoMaxItemBytes {
			return nil, nil, tooLarge(fmt.Sprintf("items[%d].plaintext is over %d bytes", i, CryptoMaxItemBytes), map[string]any{"index": i, "maxItemBytes": CryptoMaxItemBytes})
		}
		total += len(it.Plaintext)
		w := map[string]any{"plaintext": base64.StdEncoding.EncodeToString(it.Plaintext)}
		if it.Context != "" {
			w["context"] = it.Context
		}
		wire[i] = w
	}
	if total > CryptoMaxCallBytes {
		return nil, nil, tooLarge(fmt.Sprintf("the call holds more than %d bytes of plaintext", CryptoMaxCallBytes), map[string]any{"maxCallBytes": CryptoMaxCallBytes})
	}
	// A fresh custody check on every call, never cached: a stale answer could
	// send plaintext to the platform for a key that has moved to your own KMS.
	// One re-route at most, when custody moves between the check and the send.
	var lastFlow *KError
	for attempt := 0; attempt < 2; attempt++ {
		mode, _, kerr := n.readMode(ctx, crypt)
		if kerr != nil {
			return nil, nil, kerr
		}
		route, routeKnown := parseRoute(mode.Route)
		if !routeKnown {
			ectx := map[string]any{"custodyMode": mode.CustodyMode}
			var rs string
			if json.Unmarshal(mode.Route, &rs) == nil {
				ectx["route"] = rs
			}
			return nil, nil, &KError{Code: CryptoCodeCustodyUnknown, Message: fmt.Sprintf("unknown route in the custody answer for mode %q: nothing was sent", mode.CustodyMode), Context: ectx}
		}
		switch mode.CustodyMode {
		case CustodyPlatformManaged, CustodyConfidentialWorkload, CustodySKR:
			if mode.Epoch == "" {
				return nil, nil, custodyUnknown(fmt.Sprintf("custody mode %q answered without an epoch: nothing was sent", mode.CustodyMode), mode.CustodyMode)
			}
			res, meta, kerr := n.call(ctx, "encrypt", crypt, wire, mode.Epoch)
			if isZKFlow(kerr) && attempt == 0 {
				lastFlow = kerr
				continue
			}
			return res, meta, kerr
		case CustodyBYOK, CustodyHYOK:
			if route == "server" {
				if mode.Epoch == "" {
					return nil, nil, custodyUnknown(fmt.Sprintf("custody mode %q answered without an epoch: nothing was sent", mode.CustodyMode), mode.CustodyMode)
				}
				res, meta, kerr := n.call(ctx, "encrypt", crypt, wire, mode.Epoch)
				if isZKFlow(kerr) && attempt == 0 {
					lastFlow = kerr
					continue
				}
				return res, meta, kerr
			}
			core := n.clientCore()
			if core == nil {
				return nil, nil, clientCoreUnavailable(mode.CustodyMode)
			}
			if pinErr := n.checkCorePin(core, mode.CustodyMode); pinErr != nil {
				return nil, nil, pinErr
			}
			res, kerr := core.Encrypt(ctx, crypt, items, n.coreOptions())
			return res, nil, kerr
		default:
			return nil, nil, custodyUnknown(fmt.Sprintf("unknown custody mode %q: nothing was sent", mode.CustodyMode), mode.CustodyMode)
		}
	}
	return nil, nil, lastFlow
}

// Decrypt decrypts up to 100 ciphertexts (POST /@<slug>/crypto/decrypt). The
// crypt and each context must be the ones given on encrypt. A ciphertext
// the gateway cannot open is 400 K9O_ENVELOPE_INTEGRITY_FAILURE for every
// reason (wrong crypt or context, tampered, another project's), so the
// error cannot tell you why. 409 K9O_SEALED_UNDER_RETIRED_KEY means the key
// version has retired: Context["resealableUntil"] is set while Rewrap can
// still fix it. Other errors as for Encrypt.
func (n *CryptoNamespace) Decrypt(ctx context.Context, crypt string, items []CryptoCiphertextItem) (*CryptoResult, *Meta, *KError) {
	return n.ciphertextCall(ctx, "decrypt", crypt, items)
}

// Rewrap opens up to 100 ciphertexts and seals them again under the current
// version of the project's key (POST /@<slug>/crypto/rewrap); the plaintext
// never leaves the platform. Errors as for Decrypt.
func (n *CryptoNamespace) Rewrap(ctx context.Context, crypt string, items []CryptoCiphertextItem) (*CryptoResult, *Meta, *KError) {
	return n.ciphertextCall(ctx, "rewrap", crypt, items)
}

func (n *CryptoNamespace) ciphertextCall(ctx context.Context, op, crypt string, items []CryptoCiphertextItem) (*CryptoResult, *Meta, *KError) {
	if kerr := checkCrypt(crypt); kerr != nil {
		return nil, nil, kerr
	}
	if kerr := checkItemCount(len(items)); kerr != nil {
		return nil, nil, kerr
	}
	wire := make([]map[string]any, len(items))
	for i, it := range items {
		if it.Ciphertext == "" {
			return nil, nil, invalidField(fmt.Sprintf("items[%d].ciphertext must be a non-empty string", i), map[string]any{"field": "ciphertext", "index": i})
		}
		if kerr := checkContext(i, it.Context); kerr != nil {
			return nil, nil, kerr
		}
		w := map[string]any{"ciphertext": it.Ciphertext}
		if it.Context != "" {
			w["context"] = it.Context
		}
		wire[i] = w
	}
	// Route by the ciphertext: the platform first; when it says the
	// ciphertext belongs to your own key, open it with the client core.
	res, meta, kerr := n.call(ctx, op, crypt, wire, "")
	if isZKFlow(kerr) && zkRoute(kerr) == "client" {
		core := n.clientCore()
		mode, _ := kerr.Context["custodyMode"].(string)
		if core == nil {
			return nil, nil, clientCoreUnavailable(mode)
		}
		if pinErr := n.checkCorePin(core, mode); pinErr != nil {
			return nil, nil, pinErr
		}
		var cres *CryptoResult
		var ckerr *KError
		if op == "rewrap" {
			cres, ckerr = core.Rewrap(ctx, crypt, items, n.coreOptions())
		} else {
			cres, ckerr = core.Decrypt(ctx, crypt, items, n.coreOptions())
		}
		return cres, nil, ckerr
	}
	return res, meta, kerr
}

func (n *CryptoNamespace) call(ctx context.Context, op, crypt string, items []map[string]any, epoch string) (*CryptoResult, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "crypto")
	if kerr != nil {
		return nil, nil, kerr
	}
	body := map[string]any{"crypt": crypt, "items": items}
	if epoch != "" {
		body["custodyEpoch"] = epoch
	}
	env, kerr := doRequest[struct {
		Items []map[string]json.RawMessage `json:"items"`
	}](ctx, n.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "crypto/"+op),
		Body:   body,
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	if len(env.Data.Items) != len(items) {
		return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("malformed %s answer: %d results for %d items", op, len(env.Data.Items), len(items)), Status: 200}
	}
	out := &CryptoResult{Items: make([]CryptoItemResult, len(items))}
	for i, raw := range env.Data.Items {
		var r CryptoItemResult
		take := func(k string) (string, bool) {
			v, ok := raw[k]
			if !ok {
				return "", false
			}
			delete(raw, k)
			var s string
			if json.Unmarshal(v, &s) != nil {
				return "", false
			}
			return s, true
		}
		r.Ciphertext, _ = take("ciphertext")
		r.Code, _ = take("code")
		r.Message, _ = take("message")
		if pt, ok := take("plaintext"); ok {
			b, err := base64.StdEncoding.DecodeString(pt)
			if err != nil {
				return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("malformed %s answer: items[%d].plaintext is not base64", op, i), Status: 200}
			}
			r.Plaintext = b
		}
		if len(raw) > 0 {
			r.Extra = raw
		}
		out.Items[i] = r
	}
	return out, &env.Meta, nil
}

// Crypt is one registered crypt name. Status is "active" or "retired";
// RetiredAt is nil while the crypt is active. A crypt is a name the gateway
// checks on encrypt and the unit IAM grants on. It is not isolated, enforced
// or destroyed by this registry.
type Crypt struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"createdAt"`
	RetiredAt *string `json:"retiredAt"`
}

type cryptsList struct {
	Crypts []json.RawMessage `json:"crypts"`
}

func cryptsBad(op, why string) *KError {
	return &KError{Code: "K9O_INTERNAL_ERROR", Message: "crypto.crypts." + op + ": the gateway answered " + why}
}

// parseCrypt checks one crypt of a gateway answer and fails closed.
func parseCrypt(raw json.RawMessage, op string) (*Crypt, *KError) {
	var o map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &o) != nil || o == nil {
		return nil, cryptsBad(op, "without a crypt")
	}
	str := func(k string) (string, bool) {
		var s string
		if v, ok := o[k]; !ok || json.Unmarshal(v, &s) != nil {
			return "", false
		}
		return s, true
	}
	c := &Crypt{}
	var ok bool
	if c.Name, ok = str("name"); !ok || c.Name == "" {
		return nil, cryptsBad(op, "without a name")
	}
	if c.Status, ok = str("status"); !ok || (c.Status != "active" && c.Status != "retired") {
		return nil, cryptsBad(op, "with an unknown status")
	}
	if c.CreatedAt, ok = str("createdAt"); !ok || c.CreatedAt == "" {
		return nil, cryptsBad(op, "without createdAt")
	}
	rv, present := o["retiredAt"]
	if !present {
		return nil, cryptsBad(op, "with a retiredAt that is neither a date nor null")
	}
	if string(rv) != "null" {
		var s string
		if json.Unmarshal(rv, &s) != nil {
			return nil, cryptsBad(op, "with a retiredAt that is neither a date nor null")
		}
		c.RetiredAt = &s
	}
	return c, nil
}

func checkCryptName(name, op string) *KError {
	if name == "actions" || !cryptoCryptPattern.MatchString(name) {
		return invalidField("crypto.crypts."+op+": name must be 1 to 64 characters of a-z, 0-9 and -, and not \"actions\"", map[string]any{"field": "name"})
	}
	return nil
}

// CryptsList returns the crypts you hold (GET /@<slug>/crypto/crypts): every
// one if you have a `*` grant on crypts, none if you are a member with no
// grant. Errors: 403 K9O_ACCESS_DENIED, 503 if the registry cannot be read. An
// answer without the crypts list, or with a malformed crypt, is
// K9O_INTERNAL_ERROR.
func (n *CryptoNamespace) CryptsList(ctx context.Context) ([]Crypt, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "crypto")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[cryptsList](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "crypto/crypts")})
	if kerr != nil {
		return nil, nil, kerr
	}
	if env.Data.Crypts == nil {
		return nil, nil, cryptsBad("list", "without crypts")
	}
	out := make([]Crypt, 0, len(env.Data.Crypts))
	for _, raw := range env.Data.Crypts {
		c, kerr := parseCrypt(raw, "list")
		if kerr != nil {
			return nil, nil, kerr
		}
		out = append(out, *c)
	}
	return out, &env.Meta, nil
}

// CryptsCreate registers a crypt so Encrypt accepts it (POST
// /@<slug>/crypto/crypts, body {name}). Name is 1 to 64 characters of a-z, 0-9
// and -, and not "actions"; a bad name is refused with K9O_INVALID_FIELD_VALUE
// before anything is sent. Needs crypto:manage_crypts (project admins by
// default). Errors: 409 K9O_CRYPT_ALREADY_EXISTS, 409
// K9O_CRYPT_LIMIT_EXCEEDED (256 per project, retired ones counted), 409
// K9O_CRYPT_RETIRED (a retired name cannot be used again).
func (n *CryptoNamespace) CryptsCreate(ctx context.Context, name string) (*Crypt, *Meta, *KError) {
	return n.cryptsWrite(ctx, "create", name, "crypto/crypts", map[string]any{"name": name})
}

// CryptsRetire retires a crypt (POST /@<slug>/crypto/crypts/<name>/retire):
// Encrypt then refuses the name. Decrypt and Rewrap never read the registry,
// so data stays readable. This does not isolate, enforce or destroy anything.
// Needs crypto:manage_crypts; a personal access token is refused with
// K9O_RBAC_DENY. Errors: 404 K9O_CRYPT_NOT_FOUND, 409 K9O_CRYPT_RETIRED.
func (n *CryptoNamespace) CryptsRetire(ctx context.Context, name string) (*Crypt, *Meta, *KError) {
	return n.cryptsWrite(ctx, "retire", name, "crypto/crypts/"+name+"/retire", nil)
}

func (n *CryptoNamespace) cryptsWrite(ctx context.Context, op, name, path string, body any) (*Crypt, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "crypto")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := checkCryptName(name, op); kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[json.RawMessage](ctx, n.config, &requestInit{Method: "POST", Path: ProjectPath(slug, path), Body: body})
	if kerr != nil {
		return nil, nil, kerr
	}
	c, kerr := parseCrypt(env.Data, op)
	if kerr != nil {
		return nil, nil, kerr
	}
	return c, &env.Meta, nil
}
