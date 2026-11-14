package clientcore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	k9o "k9o.dev/sdk-go"
)

const key = "arn:aws:kms:eu-central-1:111122223333:key/mine"

type logger struct {
	mu sync.Mutex
	l  []string
}

func (g *logger) add(s string) { g.mu.Lock(); g.l = append(g.l, s); g.mu.Unlock() }
func (g *logger) get() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.l...)
}
func (g *logger) count(prefix string) int {
	n := 0
	for _, s := range g.get() {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

type fake struct {
	log     *logger
	caps    []string
	acquire func(FederationInput) (Credential, error)
	sealErr error
}

func newFake(log *logger) *fake {
	return &fake{log: log, caps: []string{"expected_key_refs"}}
}

func (f *fake) CoreCapabilities() []string { return f.caps }
func (f *fake) AcquireKmsCredentialBlocking(in FederationInput) (Credential, error) {
	f.log.add("exchange:" + in.IdentityToken)
	if f.acquire != nil {
		return f.acquire(in)
	}
	return Credential{Secret: "cred-1", ExpiresAtUnixSecs: 10000}, nil
}
func (f *fake) AppCryptoSealBlocking(in AppSealInput) (AppSealOutput, error) {
	f.log.add(fmt.Sprintf("seal:%s:%s:%s", in.Crypt, in.ProjectID, strings.Join(in.ExpectedKeyRefs, ",")))
	if f.sealErr != nil {
		return AppSealOutput{}, f.sealErr
	}
	return AppSealOutput{Container: append([]byte{1, 2, 3}, in.Plaintext...)}, nil
}
func (f *fake) AppCryptoOpenBlocking(in AppOpenInput) (AppOpenOutput, error) {
	f.log.add(fmt.Sprintf("open:%s:%s", in.Crypt, strings.Join(in.ExpectedKeyRefs, ",")))
	if len(in.Container) == 0 || in.Container[0] != 1 {
		return AppOpenOutput{}, errors.New("K9O_CRYPTO_UNEXPECTED_KEY: no")
	}
	return AppOpenOutput{Plaintext: in.Container[3:]}, nil
}

func tok(mut func(*k9o.CryptoFederatedToken)) *k9o.CryptoFederatedToken {
	t := &k9o.CryptoFederatedToken{Token: "jwt-secret", ExpiresAt: "x", Issuer: "i", Subject: "proj_1:crypt:c:op:encrypt", Audience: "a", Kid: "k", ProjectID: "proj_1", Provider: "aws"}
	if mut != nil {
		mut(t)
	}
	return t
}

func cfg(log *logger, f *fake, mut func(*Config)) Config {
	c := Config{
		Core:            f,
		Provider:        k9o.KeyRefAWS,
		Posture:         "byok",
		ProjectID:       "proj_1",
		KeyRef:          KeyRef{Version: 1, ExternalRef: key},
		Federation:      FederationSpec{Provider: "aws", RoleArn: "arn:aws:iam::1:role/r", SessionName: "s", Region: "eu-central-1", KeyArn: key, DurationSecs: 900},
		ExpectedKeyRefs: []string{key},
		FederatedToken: func(_ context.Context, crypt, op string) (*k9o.CryptoFederatedToken, *k9o.KError) {
			log.add("token:" + crypt + ":" + op)
			return tok(nil), nil
		},
		Now: func() int64 { return 1000 },
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

func mk(t *testing.T, mut func(*Config)) (*ClientCore, *logger, *fake) {
	t.Helper()
	log := &logger{}
	f := newFake(log)
	c, kerr := New(cfg(log, f, mut))
	if kerr != nil {
		t.Fatal(kerr)
	}
	return c, log, f
}

func one(b byte) []k9o.CryptoEncryptItem { return []k9o.CryptoEncryptItem{{Plaintext: []byte{b}}} }

func wantCode(t *testing.T, kerr *k9o.KError, code string) {
	t.Helper()
	if kerr == nil || kerr.Code != code {
		t.Fatalf("want %s, got %v", code, kerr)
	}
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEncryptTokenExchangeSealRoundTrip(t *testing.T) {
	c, log, _ := mk(t, nil)
	enc, kerr := c.Encrypt(context.Background(), "c", []k9o.CryptoEncryptItem{k9o.CryptoText("hi", "")}, k9o.CryptoCoreOptions{})
	if kerr != nil {
		t.Fatal(kerr)
	}
	eq(t, log.get(), []string{"token:c:encrypt", "exchange:jwt-secret", "seal:c:proj_1:" + key})
	dec, kerr := c.Decrypt(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: enc.Items[0].Ciphertext}}, k9o.CryptoCoreOptions{})
	if kerr != nil || !dec.Items[0].OK() || dec.Items[0].Text() != "hi" {
		t.Fatalf("%v %+v", kerr, dec)
	}
	if log.get()[len(log.get())-2] != "token:c:decrypt" && !strings.Contains(strings.Join(log.get(), ","), "token:c:decrypt") {
		t.Fatal("decrypt must use the decrypt token")
	}
}

func TestDeclaresItEnforcesThePin(t *testing.T) {
	c, _, _ := mk(t, nil)
	if !c.EnforcesExpectedKeyRefs() {
		t.Fatal("must declare")
	}
}

func TestRefusesConstructionWithoutPinEmptyPinOrKeyOutsidePin(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"nil":     func(c *Config) { c.ExpectedKeyRefs = nil },
		"empty":   func(c *Config) { c.ExpectedKeyRefs = []string{} },
		"outside": func(c *Config) { c.ExpectedKeyRefs = []string{"arn:aws:kms:eu-central-1:111122223333:key/other"} },
	} {
		log := &logger{}
		c, kerr := New(cfg(log, newFake(log), mut))
		if c != nil || kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("%s: %v", name, kerr)
		}
		want := "expectedKeyRefs is required"
		if name == "outside" {
			want = "keyRef"
		}
		if !strings.Contains(kerr.Message, want) {
			t.Fatalf("%s: %s", name, kerr.Message)
		}
	}
	// Other configuration mistakes.
	log := &logger{}
	if _, kerr := New(cfg(log, newFake(log), func(c *Config) { c.Federation.Provider = "gcp" })); kerr == nil {
		t.Fatal("provider mismatch must be refused")
	}
	if _, kerr := New(cfg(log, newFake(log), func(c *Config) { c.Posture = "x" })); kerr == nil {
		t.Fatal("bad posture must be refused")
	}
}

func TestEmptyPinFromRouterReachesNoNetworkAndNoKMS(t *testing.T) {
	c, log, _ := mk(t, nil)
	_, kerr := c.Decrypt(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: "AQID"}}, k9o.CryptoCoreOptions{ExpectedKeyRefs: []string{}})
	if kerr == nil || !strings.Contains(kerr.Message, "key pin") {
		t.Fatal(kerr)
	}
	eq(t, log.get(), nil)
}

func TestCoreWithoutExpectedKeyRefsCapabilityRefusedBeforeToken(t *testing.T) {
	log := &logger{}
	f := newFake(log)
	f.caps = nil
	c, _ := New(cfg(log, f, nil))
	_, kerr := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	wantCode(t, kerr, k9o.CryptoCodeKeyPinUnenforced)
	eq(t, log.get(), nil)
}

func TestMissingCoreRefused(t *testing.T) {
	log := &logger{}
	c, _ := New(cfg(log, newFake(log), func(c *Config) { c.Core = nil }))
	_, kerr := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	wantCode(t, kerr, "KIBO_WASM_ERROR")
	eq(t, log.get(), nil)
}

func TestResponseNamingOtherProviderOrProjectRefusedNothingExchanged(t *testing.T) {
	for name, mut := range map[string]func(*k9o.CryptoFederatedToken){
		"provider": func(t *k9o.CryptoFederatedToken) { t.Provider = "gcp" },
		"project":  func(t *k9o.CryptoFederatedToken) { t.ProjectID = "proj_other" },
	} {
		log := &logger{}
		c, _ := New(cfg(log, newFake(log), func(c *Config) {
			c.FederatedToken = func(context.Context, string, string) (*k9o.CryptoFederatedToken, *k9o.KError) { return tok(mut), nil }
		}))
		_, kerr := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
		if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("%s: %v", name, kerr)
		}
		eq(t, log.get(), nil)
	}
}

func TestProjectIDFromResponseOnlyWhenNotConfigured(t *testing.T) {
	c, log, _ := mk(t, func(c *Config) { c.ProjectID = "" })
	if _, kerr := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{}); kerr != nil {
		t.Fatal(kerr)
	}
	if last := log.get()[len(log.get())-1]; last != "seal:c:proj_1:"+key {
		t.Fatal(last)
	}
	log2 := &logger{}
	none, _ := New(cfg(log2, newFake(log2), func(c *Config) {
		c.ProjectID = ""
		c.FederatedToken = func(context.Context, string, string) (*k9o.CryptoFederatedToken, *k9o.KError) {
			return tok(func(t *k9o.CryptoFederatedToken) { t.ProjectID = "" }), nil
		}
	}))
	_, kerr := none.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	wantCode(t, kerr, "K9O_INVALID_FIELD_VALUE")
}

func TestCredentialReusedUntilNearExpiryThenExchangedAgain(t *testing.T) {
	var now int64 = 1000
	c, log, _ := mk(t, func(c *Config) { c.Now = func() int64 { return now } })
	c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	if n := log.count("exchange"); n != 1 {
		t.Fatal(n)
	}
	if n := log.count("token"); n != 2 {
		t.Fatalf("a token is fetched on every call, got %d", n)
	}
	now = 9950 // inside the 60 s skew of 10000
	c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	if n := log.count("exchange"); n != 2 {
		t.Fatal(n)
	}
}

func TestStalledExchangeAbandonedAtDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c, _, _ := mk(t, func(c *Config) { c.ExchangeDeadline = 20 * time.Millisecond })
	c.cfg.Core.(*fake).acquire = func(FederationInput) (Credential, error) { <-release; return Credential{}, nil }
	start := time.Now()
	_, kerr := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	wantCode(t, kerr, "KIBO_EXCHANGE_TIMEOUT")
	if time.Since(start) > 5*time.Second {
		t.Fatal("deadline not honoured")
	}
}

func TestBadItemFailsAloneWithCoresCode(t *testing.T) {
	c, _, _ := mk(t, nil)
	enc, _ := c.Encrypt(context.Background(), "c", one(9), k9o.CryptoCoreOptions{})
	good := enc.Items[0].Ciphertext
	r, kerr := c.Decrypt(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: "AAAA"}, {Ciphertext: good}}, k9o.CryptoCoreOptions{})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if r.Items[0].OK() || r.Items[0].Code != "K9O_CRYPTO_UNEXPECTED_KEY" || r.Items[0].Message != "no" || !r.Items[1].OK() {
		t.Fatalf("%+v", r.Items)
	}
}

func TestUpstreamKMSErrorPassesThrough(t *testing.T) {
	c, _, f := mk(t, nil)
	f.sealErr = errors.New("UPSTREAM_KMS_ERROR: denied")
	r, _ := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	if r.Items[0].Code != "UPSTREAM_KMS_ERROR" || r.Items[0].Message != "denied" {
		t.Fatalf("%+v", r.Items[0])
	}
}

func TestRewrapOpensThenSealsWithRewrapToken(t *testing.T) {
	c, log, _ := mk(t, nil)
	enc, _ := c.Encrypt(context.Background(), "c", one(7), k9o.CryptoCoreOptions{})
	log.mu.Lock()
	log.l = nil
	log.mu.Unlock()
	r, kerr := c.Rewrap(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: enc.Items[0].Ciphertext}}, k9o.CryptoCoreOptions{})
	if kerr != nil || !r.Items[0].OK() || r.Items[0].Ciphertext == "" {
		t.Fatalf("%v %+v", kerr, r)
	}
	l := log.get()
	if l[0] != "token:c:rewrap" {
		t.Fatal(l)
	}
	n := len(l)
	if !strings.HasPrefix(l[n-2], "open") || !strings.HasPrefix(l[n-1], "seal") {
		t.Fatal(l)
	}
}

func TestNeitherTokenNorCredentialAppearsInErrorsOrStrings(t *testing.T) {
	c, _, f := mk(t, nil)
	f.sealErr = errors.New("UPSTREAM_KMS_ERROR: denied")
	r, _ := c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{})
	s := fmt.Sprintf("%+v %#v", r, r) + fmt.Sprint(Credential{Secret: "cred-1"}) + fmt.Sprintf("%+v %#v", FederationInput{IdentityToken: "jwt-secret"}, Credential{Secret: "cred-1"})
	if strings.Contains(s, "jwt-secret") || strings.Contains(s, "cred-1") {
		t.Fatal(s)
	}
	// A failed exchange does not echo the token either.
	f.acquire = func(in FederationInput) (Credential, error) {
		return Credential{}, errors.New("UPSTREAM_STS_ERROR: rejected")
	}
	_, kerr := c.Encrypt(context.Background(), "c2", one(1), k9o.CryptoCoreOptions{})
	if kerr == nil || strings.Contains(kerr.Error(), "jwt-secret") || kerr.Code != "UPSTREAM_STS_ERROR" {
		t.Fatal(kerr)
	}
}

func TestCiphertextIsBase64OfContainer(t *testing.T) {
	c, _, _ := mk(t, nil)
	r, _ := c.Encrypt(context.Background(), "c", one(5), k9o.CryptoCoreOptions{})
	if r.Items[0].Ciphertext != base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 5}) {
		t.Fatal(r.Items[0].Ciphertext)
	}
}

func TestPerCallPinExcludingKeyRefusesBeforeTokenAndExchange(t *testing.T) {
	c, log, _ := mk(t, nil)
	other := k9o.CryptoCoreOptions{ExpectedKeyRefs: []string{key + "-other"}}
	r, kerr := c.Encrypt(context.Background(), "c", one(1), other)
	if kerr != nil || len(r.Items) != 1 || r.Items[0].Code != "K9O_CRYPTO_UNEXPECTED_KEY" {
		t.Fatalf("%v %+v", kerr, r)
	}
	r, kerr = c.Decrypt(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: "AAAA"}}, other)
	if kerr != nil || r.Items[0].Code != "K9O_CRYPTO_UNEXPECTED_KEY" {
		t.Fatalf("%v %+v", kerr, r)
	}
	r, kerr = c.Rewrap(context.Background(), "c", []k9o.CryptoCiphertextItem{{Ciphertext: "AAAA"}}, other)
	if kerr != nil || r.Items[0].Code != "K9O_CRYPTO_UNEXPECTED_KEY" {
		t.Fatalf("%v %+v", kerr, r)
	}
	if n := len(log.get()); n != 0 {
		t.Fatalf("token/exchange/seal/open calls: %v", log.get())
	}
	r, kerr = c.Encrypt(context.Background(), "c", one(1), k9o.CryptoCoreOptions{ExpectedKeyRefs: []string{key}})
	if kerr != nil || !r.Items[0].OK() || log.count("token") != 1 || log.count("exchange") != 1 || log.count("seal") != 1 {
		t.Fatalf("%v %+v %v", kerr, r, log.get())
	}
}
