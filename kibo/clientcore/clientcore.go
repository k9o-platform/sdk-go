// Package clientcore is the client core for Crypto on a BYOK or HYOK project:
// it opens and seals data on this machine, against your own KMS or HSM, so the
// platform never sees plaintext or the key. ZERO crypto here: sealing, opening,
// the key pin and the token exchange all run in the audited Rust core
// (kibo-kms-rest-ffi, zk-bundle-v2.16.3 or later). This package only sequences
// them:
//
//	federated token (platform) -> AcquireKmsCredential (your cloud) -> AppCryptoSeal / AppCryptoOpen (your KMS)
//
// Everything that picks a key, a provider, a role, a region or an endpoint
// comes from the configuration you pass to New. Nothing comes from the
// platform's answer: the token response's Provider and ProjectID are checked
// against your configuration, and a mismatch is refused, never followed.
//
// The bound core is passed in as an AppCore. A small adapter over the generated
// kibo_kms_rest_ffi package (AppCryptoSealBlocking, AppCryptoOpenBlocking,
// AcquireKmsCredentialBlocking, CoreCapabilities) satisfies it; see the Kibo
// README. The package itself is pure Go, so it builds and tests without the
// native library.
package clientcore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	k9o "k9o.dev/sdk-go"
)

// Codes raised by this package.
const (
	// CodeExchangeTimeout is raised when the token exchange runs past its deadline.
	CodeExchangeTimeout = "KIBO_EXCHANGE_TIMEOUT"
	// CodeExchangeCanceled is raised when the caller's context ends during the exchange.
	CodeExchangeCanceled = "KIBO_EXCHANGE_CANCELED"
	// CodeCoreError is raised when the bound core is missing or fails without a code.
	CodeCoreError = "KIBO_WASM_ERROR"
	// CodeCoreDecodeError is raised when the core returns something unusable.
	CodeCoreDecodeError = "KIBO_WASM_DECODE_ERROR"
)

// DefaultExchangeDeadline is how long the token exchange may take.
const DefaultExchangeDeadline = 30 * time.Second

// skewSecs: refresh a credential this long before it expires.
const skewSecs = 60

// Credential is a credential on your cloud. Secret is never printed.
type Credential struct {
	Secret            string
	ExpiresAtUnixSecs uint64
}

func (Credential) String() string   { return "Credential{<redacted>}" }
func (Credential) GoString() string { return "Credential{<redacted>}" }

// KeyRef names the key this application uses.
type KeyRef struct {
	Version     uint32
	ExternalRef string
}

// ProviderConfig is provider-specific initialization. Nil means not set.
type ProviderConfig struct {
	AwsRegion    *string
	VaultAddress *string
}

// FederationSpec says how to exchange the platform's identity token for a
// credential on your cloud. Provider is "aws", "gcp", "azure" or "vault"; set
// only that provider's fields. All of it is yours: the platform supplies none.
type FederationSpec struct {
	Provider string
	// aws
	RoleArn      string
	SessionName  string
	Region       string
	KeyArn       string
	DurationSecs uint32
	// gcp
	Audience string
	// azure
	TenantID string
	ClientID string
	Scope    string
	// vault
	VaultAddr string
	// JwtAuthMount is the JWT auth mount: the name Vault serves the JWT login under, "jwt" for
	// auth/jwt (not the transit mount, which comes from the key reference).
	JwtAuthMount string
	Role         string
}

// FederationInput is the input of AcquireKmsCredentialBlocking. IdentityToken is secret.
type FederationInput struct {
	Spec          FederationSpec
	IdentityToken string
	NowUnixSecs   uint64
}

func (FederationInput) String() string   { return "FederationInput{<redacted>}" }
func (FederationInput) GoString() string { return "FederationInput{<redacted>}" }

// AppSealInput is the input of AppCryptoSealBlocking. Wire names are snake_case.
type AppSealInput struct {
	Provider        string // "aws-kms" | "gcp-kms" | "azure-kv" | "hashicorp-vault"
	Posture         string // "byok" | "hyok"
	KmsCred         Credential
	KeyRef          KeyRef
	ProviderConfig  ProviderConfig
	ProjectID       string
	Crypt           string
	Context         *string
	Plaintext       []byte
	ExpectedKeyRefs []string
}

// AppSealOutput holds the container bytes.
type AppSealOutput struct{ Container []byte }

// AppOpenInput is the input of AppCryptoOpenBlocking.
type AppOpenInput struct {
	KmsCred         Credential
	ProviderConfig  ProviderConfig
	ProjectID       string
	Crypt           string
	Context         *string
	Container       []byte
	ExpectedKeyRefs []string
}

// AppOpenOutput holds the plaintext.
type AppOpenOutput struct{ Plaintext []byte }

// AppCore is what the bound core exports beyond ZkEncrypt and ZkDecrypt. A
// small adapter over the generated kibo_kms_rest_ffi package satisfies it,
// mapping these structs to the generated ones field for field. A core error
// should carry the text "<CODE>: <message>" (UPSTREAM_KMS_ERROR is your own
// KMS), or be a *k9o.KError.
type AppCore interface {
	AppCryptoSealBlocking(AppSealInput) (AppSealOutput, error)
	AppCryptoOpenBlocking(AppOpenInput) (AppOpenOutput, error)
	AcquireKmsCredentialBlocking(FederationInput) (Credential, error)
	CoreCapabilities() []string
}

// FederatedTokenFunc fetches a token from the platform. Use ViaCrypto for client.Crypto.
type FederatedTokenFunc func(ctx context.Context, crypt, op string) (*k9o.CryptoFederatedToken, *k9o.KError)

// ViaCrypto adapts client.Crypto.FederatedToken.
func ViaCrypto(n *k9o.CryptoNamespace) FederatedTokenFunc {
	return func(ctx context.Context, crypt, op string) (*k9o.CryptoFederatedToken, *k9o.KError) {
		t, _, kerr := n.FederatedToken(ctx, crypt, op)
		return t, kerr
	}
}

// Config configures the client core.
type Config struct {
	// Core is the loaded core. Required.
	Core AppCore
	// Provider is your cloud. Must equal Federation.Provider.
	Provider k9o.KeyRefProvider
	// Posture is "byok" or "hyok".
	Posture string
	// ProjectID (proj_...) is bound into what is sealed. Leave it empty only if
	// you want the token response's value; a value given here is never overridden.
	ProjectID string
	// KeyRef is the exact key this application uses.
	KeyRef KeyRef
	// ProviderConfig is the region for AWS, the address for Vault, the host
	// suffix for an Azure cloud outside the built-in list.
	ProviderConfig ProviderConfig
	// Federation says how to exchange the platform's token for a credential on your cloud.
	Federation FederationSpec
	// ExpectedKeyRefs are the keys this application allows. Required and
	// non-empty: a core with no pin opens whatever key a stored container names,
	// so this client never calls without one.
	ExpectedKeyRefs []string
	// FederatedToken is ViaCrypto(client.Crypto).
	FederatedToken FederatedTokenFunc
	// ExchangeDeadline bounds the token exchange. Default 30 s.
	ExchangeDeadline time.Duration
	// Now is the host clock, Unix seconds. Default time.Now().Unix().
	Now func() int64
}

var providerWire = map[k9o.KeyRefProvider]string{
	k9o.KeyRefAWS:   "aws-kms",
	k9o.KeyRefGCP:   "gcp-kms",
	k9o.KeyRefAzure: "azure-kv",
	k9o.KeyRefVault: "hashicorp-vault",
}

// ClientCore implements k9o.CryptoClientCore and k9o.CryptoCoreEnforcer.
type ClientCore struct {
	cfg      Config
	pin      []string
	deadline time.Duration
	now      func() int64

	mu       sync.Mutex
	creds    map[string]Credential
	capsDone bool
}

var (
	_ k9o.CryptoClientCore   = (*ClientCore)(nil)
	_ k9o.CryptoCoreEnforcer = (*ClientCore)(nil)
)

func bad(msg, field string) *k9o.KError {
	return &k9o.KError{Code: "K9O_INVALID_FIELD_VALUE", Message: msg, Context: map[string]any{"field": field}}
}

// New builds the client core. Register it with client.Crypto.UseClientCore(core).
func New(cfg Config) (*ClientCore, *k9o.KError) {
	if _, ok := providerWire[cfg.Provider]; !ok {
		return nil, bad(`provider must be "aws", "gcp", "azure" or "vault"`, "provider")
	}
	if string(cfg.Provider) != cfg.Federation.Provider {
		return nil, bad("provider and federation.provider must be the same cloud", "provider")
	}
	if cfg.Posture != "byok" && cfg.Posture != "hyok" {
		return nil, bad(`posture must be "byok" or "hyok"`, "posture")
	}
	if cfg.FederatedToken == nil {
		return nil, bad("federatedToken is required", "federatedToken")
	}
	pin, kerr := checkPin(cfg.ExpectedKeyRefs)
	if kerr != nil {
		return nil, kerr
	}
	if !k9o.KeyRefMatches(cfg.KeyRef.ExternalRef, pin, cfg.Provider) {
		return nil, bad("keyRef is not in expectedKeyRefs", "keyRef")
	}
	c := &ClientCore{cfg: cfg, pin: pin, deadline: cfg.ExchangeDeadline, now: cfg.Now, creds: map[string]Credential{}}
	if c.deadline <= 0 {
		c.deadline = DefaultExchangeDeadline
	}
	if c.now == nil {
		c.now = func() int64 { return time.Now().Unix() }
	}
	return c, nil
}

func checkPin(refs []string) ([]string, *k9o.KError) {
	const field = "expectedKeyRefs"
	if len(refs) == 0 {
		return nil, bad("expectedKeyRefs is required: this client never seals or opens without a key pin", field)
	}
	for _, r := range refs {
		if strings.TrimSpace(r) == "" || strings.IndexFunc(r, unicode.IsSpace) >= 0 {
			return nil, bad("expectedKeyRefs: each entry must be a key identifier without spaces", field)
		}
		if strings.Contains(r, "*") {
			return nil, bad(`expectedKeyRefs: an entry cannot contain "*"; name the exact key`, field)
		}
	}
	return append([]string(nil), refs...), nil
}

// EnforcesExpectedKeyRefs declares that this core enforces the key pin.
func (c *ClientCore) EnforcesExpectedKeyRefs() bool { return true }

var coreErrRe = regexp.MustCompile(`^([A-Z][A-Z0-9_]*):\s*([\s\S]*)$`)

// mapCoreError reads the core's "<CODE>: <message>"; UPSTREAM_KMS_ERROR is your own KMS failing.
func mapCoreError(err error) *k9o.KError {
	var ke *k9o.KError
	if errors.As(err, &ke) && ke != nil {
		return ke
	}
	raw := err.Error()
	if m := coreErrRe.FindStringSubmatch(raw); m != nil {
		return &k9o.KError{Code: m[1], Message: m[2]}
	}
	return &k9o.KError{Code: CodeCoreError, Message: raw}
}

func failure(err error) k9o.CryptoItemResult {
	k := mapCoreError(err)
	return k9o.CryptoItemResult{Code: k.Code, Message: k.Message}
}

func (c *ClientCore) effectivePin(opts k9o.CryptoCoreOptions) ([]string, *k9o.KError) {
	p := c.pin
	if opts.ExpectedKeyRefs != nil {
		p = opts.ExpectedKeyRefs
	}
	if len(p) == 0 {
		return nil, bad("expectedKeyRefs is empty: refusing to call without a key pin", "expectedKeyRefs")
	}
	return p, nil
}

func (c *ClientCore) core() (AppCore, *k9o.KError) {
	w := c.cfg.Core
	if w == nil {
		return nil, &k9o.KError{
			Code:           CodeCoreError,
			Message:        "the Kibo core is not loaded; it must export app_crypto_seal, app_crypto_open, acquire_kms_credential and core_capabilities (zk-bundle-v2.16.3 or later)",
			Context:        map[string]any{"missing": "core"},
			Recommendation: "Use a build of the Kibo module that ships zk-bundle-v2.16.3 or later.",
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.capsDone {
		has := false
		for _, cap := range w.CoreCapabilities() {
			if cap == "expected_key_refs" {
				has = true
			}
		}
		if !has {
			return nil, &k9o.KError{
				Code:           k9o.CryptoCodeKeyPinUnenforced,
				Message:        "the Kibo core does not declare that it enforces expected_key_refs; nothing was sent",
				Context:        map[string]any{},
				Recommendation: "Use a build of the Kibo module that ships zk-bundle-v2.16.3 or later.",
			}
		}
		c.capsDone = true
	}
	return w, nil
}

func (c *ClientCore) credential(ctx context.Context, w AppCore, crypt, op string) (Credential, string, *k9o.KError) {
	t, kerr := c.cfg.FederatedToken(ctx, crypt, op)
	if kerr != nil {
		return Credential{}, "", kerr
	}
	if t == nil {
		return Credential{}, "", &k9o.KError{Code: CodeCoreDecodeError, Message: "the platform returned no token"}
	}
	if t.Provider != "" && t.Provider != string(c.cfg.Provider) {
		return Credential{}, "", bad(fmt.Sprintf("the platform named provider %q; this client is configured for %q", t.Provider, c.cfg.Provider), "provider")
	}
	if t.ProjectID != "" && c.cfg.ProjectID != "" && t.ProjectID != c.cfg.ProjectID {
		return Credential{}, "", bad("the platform named a different project than the one configured", "projectId")
	}
	projectID := c.cfg.ProjectID
	if projectID == "" {
		projectID = t.ProjectID
	}
	if projectID == "" {
		return Credential{}, "", bad("projectId is not configured and the platform did not name one", "projectId")
	}

	key := crypt + "\x00" + op + "\x00" + t.Subject
	c.mu.Lock()
	cached, ok := c.creds[key]
	c.mu.Unlock()
	// A token is fetched on every call (it is bound to the call's crypt and op);
	// the credential from the exchange is reused until it is close to expiring.
	if ok && int64(cached.ExpiresAtUnixSecs)-skewSecs > c.now() {
		return cached, projectID, nil
	}
	cred, kerr := c.exchange(ctx, w, FederationInput{Spec: c.cfg.Federation, IdentityToken: t.Token, NowUnixSecs: uint64(max(c.now(), 0))})
	if kerr != nil {
		return Credential{}, "", kerr
	}
	c.mu.Lock()
	c.creds[key] = cred
	c.mu.Unlock()
	return cred, projectID, nil
}

type exchangeOut struct {
	cred Credential
	err  error
}

func (c *ClientCore) exchange(ctx context.Context, w AppCore, in FederationInput) (Credential, *k9o.KError) {
	dctx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	ch := make(chan exchangeOut, 1)
	go func() {
		cred, err := w.AcquireKmsCredentialBlocking(in)
		ch <- exchangeOut{cred, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return Credential{}, mapCoreError(r.err)
		}
		if r.cred.Secret == "" || r.cred.ExpiresAtUnixSecs == 0 {
			return Credential{}, &k9o.KError{Code: CodeCoreDecodeError, Message: "the core returned a credential without secret or expiry"}
		}
		return r.cred, nil
	case <-dctx.Done():
		if errors.Is(dctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return Credential{}, &k9o.KError{
				Code:           CodeExchangeTimeout,
				Message:        fmt.Sprintf("the token exchange with your cloud did not finish within %d ms", c.deadline.Milliseconds()),
				Context:        map[string]any{"deadlineMs": c.deadline.Milliseconds()},
				Recommendation: "Check that your cloud's token endpoint is reachable from this host.",
			}
		}
		return Credential{}, &k9o.KError{Code: CodeExchangeCanceled, Message: "the token exchange with your cloud was canceled"}
	}
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (c *ClientCore) providerConfig(opts k9o.CryptoCoreOptions) ProviderConfig {
	pc := c.cfg.ProviderConfig
	return pc
}

func (c *ClientCore) seal(w AppCore, cred Credential, projectID, crypt string, plaintext []byte, context string, pin []string, opts k9o.CryptoCoreOptions) (string, error) {
	out, err := w.AppCryptoSealBlocking(AppSealInput{
		Provider:        providerWire[c.cfg.Provider],
		Posture:         c.cfg.Posture,
		KmsCred:         cred,
		KeyRef:          c.cfg.KeyRef,
		ProviderConfig:  c.providerConfig(opts),
		ProjectID:       projectID,
		Crypt:           crypt,
		Context:         optStr(context),
		Plaintext:       plaintext,
		ExpectedKeyRefs: append([]string(nil), pin...),
	})
	if err != nil {
		return "", err
	}
	if len(out.Container) == 0 {
		return "", &k9o.KError{Code: CodeCoreDecodeError, Message: "the core returned an empty container"}
	}
	return base64.StdEncoding.EncodeToString(out.Container), nil
}

func (c *ClientCore) open(w AppCore, cred Credential, projectID, crypt, ciphertext, context string, pin []string, opts k9o.CryptoCoreOptions) ([]byte, error) {
	container, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, &k9o.KError{Code: "K9O_INVALID_FIELD_VALUE", Message: "ciphertext is not base64", Context: map[string]any{"field": "ciphertext"}}
	}
	out, err := w.AppCryptoOpenBlocking(AppOpenInput{
		KmsCred:         cred,
		ProviderConfig:  c.providerConfig(opts),
		ProjectID:       projectID,
		Crypt:           crypt,
		Context:         optStr(context),
		Container:       container,
		ExpectedKeyRefs: append([]string(nil), pin...),
	})
	if err != nil {
		return nil, err
	}
	return out.Plaintext, nil
}

// errKeyNotInPin marks a call whose pin excludes the configured key; each op turns it into per-item refusals.
var errKeyNotInPin = &k9o.KError{Code: "K9O_CRYPTO_UNEXPECTED_KEY", Message: "the configured key is not in this call's expectedKeyRefs; nothing was sent"}

func refuseAll(n int) *k9o.CryptoResult {
	out := make([]k9o.CryptoItemResult, n)
	for i := range out {
		out[i] = k9o.CryptoItemResult{Code: errKeyNotInPin.Code, Message: errKeyNotInPin.Message}
	}
	return &k9o.CryptoResult{Items: out}
}

// prepare runs the call-level steps in order: pin, core and capabilities, token, exchange.
func (c *ClientCore) prepare(ctx context.Context, crypt, op string, opts k9o.CryptoCoreOptions) (AppCore, Credential, string, []string, *k9o.KError) {
	pin, kerr := c.effectivePin(opts)
	if kerr != nil {
		return nil, Credential{}, "", nil, kerr
	}
	// The pin is matched before any token is requested or any exchange is made.
	if !k9o.KeyRefMatches(c.cfg.KeyRef.ExternalRef, pin, c.cfg.Provider) {
		return nil, Credential{}, "", nil, errKeyNotInPin
	}
	w, kerr := c.core()
	if kerr != nil {
		return nil, Credential{}, "", nil, kerr
	}
	cred, projectID, kerr := c.credential(ctx, w, crypt, op)
	if kerr != nil {
		return nil, Credential{}, "", nil, kerr
	}
	return w, cred, projectID, pin, nil
}

// Encrypt seals each item under the configured key, on this machine.
func (c *ClientCore) Encrypt(ctx context.Context, crypt string, items []k9o.CryptoEncryptItem, opts k9o.CryptoCoreOptions) (*k9o.CryptoResult, *k9o.KError) {
	w, cred, projectID, pin, kerr := c.prepare(ctx, crypt, "encrypt", opts)
	if kerr == errKeyNotInPin {
		return refuseAll(len(items)), nil
	}
	if kerr != nil {
		return nil, kerr
	}
	out := make([]k9o.CryptoItemResult, 0, len(items))
	for _, it := range items {
		ct, err := c.seal(w, cred, projectID, crypt, it.Plaintext, it.Context, pin, opts)
		if err != nil {
			out = append(out, failure(err))
			continue
		}
		out = append(out, k9o.CryptoItemResult{Ciphertext: ct})
	}
	return &k9o.CryptoResult{Items: out}, nil
}

// Decrypt opens each item on this machine.
func (c *ClientCore) Decrypt(ctx context.Context, crypt string, items []k9o.CryptoCiphertextItem, opts k9o.CryptoCoreOptions) (*k9o.CryptoResult, *k9o.KError) {
	w, cred, projectID, pin, kerr := c.prepare(ctx, crypt, "decrypt", opts)
	if kerr == errKeyNotInPin {
		return refuseAll(len(items)), nil
	}
	if kerr != nil {
		return nil, kerr
	}
	out := make([]k9o.CryptoItemResult, 0, len(items))
	for _, it := range items {
		pt, err := c.open(w, cred, projectID, crypt, it.Ciphertext, it.Context, pin, opts)
		if err != nil {
			out = append(out, failure(err))
			continue
		}
		if pt == nil {
			pt = []byte{}
		}
		out = append(out, k9o.CryptoItemResult{Plaintext: pt})
	}
	return &k9o.CryptoResult{Items: out}, nil
}

// Rewrap opens each item and seals it again under the configured key, with a "rewrap" token.
func (c *ClientCore) Rewrap(ctx context.Context, crypt string, items []k9o.CryptoCiphertextItem, opts k9o.CryptoCoreOptions) (*k9o.CryptoResult, *k9o.KError) {
	w, cred, projectID, pin, kerr := c.prepare(ctx, crypt, "rewrap", opts)
	if kerr == errKeyNotInPin {
		return refuseAll(len(items)), nil
	}
	if kerr != nil {
		return nil, kerr
	}
	out := make([]k9o.CryptoItemResult, 0, len(items))
	for _, it := range items {
		pt, err := c.open(w, cred, projectID, crypt, it.Ciphertext, it.Context, pin, opts)
		if err == nil {
			var ct string
			ct, err = c.seal(w, cred, projectID, crypt, pt, it.Context, pin, opts)
			if err == nil {
				out = append(out, k9o.CryptoItemResult{Ciphertext: ct})
				continue
			}
		}
		out = append(out, failure(err))
	}
	return &k9o.CryptoResult{Items: out}, nil
}
