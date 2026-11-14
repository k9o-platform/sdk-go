package k9o

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type routedCall struct {
	method, path, query string
	body                map[string]any
}

type routedStub struct {
	calls []routedCall
	// modes are served in order, the last one repeats.
	modes []any
	// posts are served in order, the last one repeats.
	posts []func() *http.Response
	modeN int
	postN int
}

func (s *routedStub) client(t *testing.T) *Client {
	return rampartTestClient(t, s.handler())
}

func (s *routedStub) handler() roundTripFunc {
	return func(req *http.Request) *http.Response {
		var b []byte
		if req.Body != nil {
			b, _ = io.ReadAll(req.Body)
		}
		call := routedCall{method: req.Method, path: req.URL.Path, query: req.URL.RawQuery, body: map[string]any{}}
		_ = json.Unmarshal(b, &call.body)
		s.calls = append(s.calls, call)
		if req.Method == "GET" {
			i := s.modeN
			if i >= len(s.modes) {
				i = len(s.modes) - 1
			}
			s.modeN++
			switch m := s.modes[i].(type) {
			case int:
				return jsonResponse(m, map[string]any{"code": "K9O_INTERNAL_ERROR", "message": "mode down"})
			default:
				return jsonResponse(200, dkOK(m))
			}
		}
		i := s.postN
		if i >= len(s.posts) {
			i = len(s.posts) - 1
		}
		s.postN++
		return s.posts[i]()
	}
}

func (s *routedStub) count(method, suffix string) int {
	n := 0
	for _, c := range s.calls {
		if c.method == method && strings.HasSuffix(c.path, suffix) {
			n++
		}
	}
	return n
}

func modeBody(mode, epoch string) map[string]any {
	return map[string]any{"custodyMode": mode, "epoch": epoch}
}

func okItems() func() *http.Response {
	return func() *http.Response {
		return jsonResponse(200, dkOK(map[string]any{"items": []any{map[string]any{"ciphertext": "Y3Q=", "plaintext": cryptoB64("p")}}}))
	}
}

func flow409(route, mode, epoch string) func() *http.Response {
	return func() *http.Response {
		return jsonResponse(409, map[string]any{"code": "K9O_ZK_STORE_FLOW_REQUIRED", "message": "client route", "context": map[string]any{"custodyMode": mode, "route": route, "epoch": epoch}})
	}
}

type fakeCore struct {
	enc, dec, rew int
	last          CryptoCoreOptions
	declares      bool
}

func (f *fakeCore) EnforcesExpectedKeyRefs() bool { return f.declares }

// bareCore exposes only the three CryptoClientCore methods, so it does not
// implement CryptoCoreEnforcer.
type bareCore struct{ CryptoClientCore }

func (f *fakeCore) res(tag string) *CryptoResult {
	return &CryptoResult{Items: []CryptoItemResult{{Ciphertext: tag, Plaintext: []byte(tag)}}}
}
func (f *fakeCore) Encrypt(_ context.Context, _ string, _ []CryptoEncryptItem, o CryptoCoreOptions) (*CryptoResult, *KError) {
	f.last = o
	f.enc++
	return f.res("core-enc"), nil
}
func (f *fakeCore) Decrypt(_ context.Context, _ string, _ []CryptoCiphertextItem, o CryptoCoreOptions) (*CryptoResult, *KError) {
	f.last = o
	f.dec++
	return f.res("core-dec"), nil
}
func (f *fakeCore) Rewrap(_ context.Context, _ string, _ []CryptoCiphertextItem, o CryptoCoreOptions) (*CryptoResult, *KError) {
	f.last = o
	f.rew++
	return f.res("core-rew"), nil
}

var oneItem = []CryptoEncryptItem{{Plaintext: []byte("secret")}}
var oneCipher = []CryptoCiphertextItem{{Ciphertext: "AA"}}

func TestCryptoRouterPlatformOneModeOnePost(t *testing.T) {
	for _, m := range []string{"PLATFORM_MANAGED", "CONFIDENTIAL_WORKLOAD", "SKR"} {
		st := &routedStub{modes: []any{modeBody(m, "e7")}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil {
			t.Fatal(kerr)
		}
		if len(st.calls) != 2 || st.calls[0].method != "GET" || !strings.HasSuffix(st.calls[0].path, "/crypto/mode") || st.calls[1].method != "POST" {
			t.Fatalf("%s: %+v", m, st.calls)
		}
		b := st.calls[1].body
		if b["custodyEpoch"] != "e7" || len(b) != 3 || b["crypt"] != "k" || b["items"] == nil {
			t.Fatalf("%s body: %v", m, b)
		}
	}
}

func TestCryptoRouterNoModeCache(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("PLATFORM_MANAGED", "e1")}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	for i := 0; i < 2; i++ {
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil {
			t.Fatal(kerr)
		}
	}
	if st.count("GET", "/crypto/mode") != 2 || st.count("POST", "/crypto/encrypt") != 2 {
		t.Fatalf("%+v", st.calls)
	}
}

func TestCryptoRouterClientRouteNoCoreSendsNothing(t *testing.T) {
	for _, m := range []string{"BYOK", "HYOK"} {
		st := &routedStub{modes: []any{modeBody(m, "e1")}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
		if kerr == nil || kerr.Code != CryptoCodeClientCoreUnavailable || kerr.Status != 501 || kerr.Context["custodyMode"] != m || kerr.Context["requirement"] != "kibo-client-core" {
			t.Fatalf("%s: %v", m, kerr)
		}
		if st.count("POST", "/crypto/encrypt") != 0 {
			t.Fatal("plaintext must not be sent")
		}
	}
}

func TestCryptoRouterClientRouteWithCore(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("BYOK", "e1")}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	res, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
	if kerr != nil || core.enc != 1 || res.Items[0].Ciphertext != "core-enc" || st.count("POST", "/crypto/encrypt") != 0 {
		t.Fatalf("%v %+v %+v", kerr, core, st.calls)
	}
}

func TestCryptoRouterEpochRace(t *testing.T) {
	st := &routedStub{
		modes: []any{modeBody("PLATFORM_MANAGED", "e1"), modeBody("BYOK", "e2")},
		posts: []func() *http.Response{flow409("client", "BYOK", "e2"), okItems()},
	}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil {
		t.Fatal(kerr)
	}
	if st.count("GET", "/crypto/mode") != 2 || st.count("POST", "/crypto/encrypt") != 1 || core.enc != 1 {
		t.Fatalf("%+v core=%+v", st.calls, core)
	}

	// A second 409 is raised: two attempts in all.
	st = &routedStub{
		modes: []any{modeBody("PLATFORM_MANAGED", "e1"), modeBody("PLATFORM_MANAGED", "e2")},
		posts: []func() *http.Response{flow409("client", "BYOK", "e2"), flow409("client", "BYOK", "e3")},
	}
	c = st.client(t)
	_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
	if kerr == nil || kerr.Code != CryptoCodeZKStoreFlowRequired || kerr.Status != 409 {
		t.Fatalf("%v", kerr)
	}
	if st.count("GET", "/crypto/mode") != 2 || st.count("POST", "/crypto/encrypt") != 2 {
		t.Fatalf("%+v", st.calls)
	}
}

func TestCryptoRouterDecryptRewrapByCiphertext(t *testing.T) {
	for _, op := range []string{"decrypt", "rewrap"} {
		call := func(c *Client) (*CryptoResult, *KError) {
			if op == "decrypt" {
				r, _, k := c.Crypto.Decrypt(context.Background(), "k", oneCipher)
				return r, k
			}
			r, _, k := c.Crypto.Rewrap(context.Background(), "k", oneCipher)
			return r, k
		}
		coreCount := func(f *fakeCore) int {
			if op == "decrypt" {
				return f.dec
			}
			return f.rew
		}
		// 409 route client -> core.
		st := &routedStub{posts: []func() *http.Response{flow409("client", "BYOK", "e1")}}
		c := st.client(t)
		core := &fakeCore{}
		c.Crypto.UseClientCore(core)
		if _, k := call(c); k != nil || coreCount(core) != 1 || core.enc != 0 {
			t.Fatalf("%s: %v %+v", op, k, core)
		}
		if st.count("GET", "/crypto/mode") != 0 || st.count("POST", "/crypto/"+op) != 1 {
			t.Fatalf("%s: %+v", op, st.calls)
		}
		// 409 route client, no core -> typed unavailable.
		st = &routedStub{posts: []func() *http.Response{flow409("client", "HYOK", "e1")}}
		c = st.client(t)
		if _, k := call(c); k == nil || k.Code != CryptoCodeClientCoreUnavailable || k.Status != 501 || k.Context["custodyMode"] != "HYOK" {
			t.Fatalf("%s: %v", op, k)
		}
		// 200 -> no core.
		st = &routedStub{posts: []func() *http.Response{okItems()}}
		c = st.client(t)
		core = &fakeCore{}
		c.Crypto.UseClientCore(core)
		if _, k := call(c); k != nil || coreCount(core) != 0 {
			t.Fatalf("%s: %v", op, k)
		}
		// Other 409 and 400 are raised unchanged.
		for _, tc := range []struct {
			status int
			code   string
			ctx    map[string]any
		}{
			{409, "K9O_SEALED_UNDER_RETIRED_KEY", map[string]any{}},
			{409, "K9O_ZK_STORE_FLOW_REQUIRED", map[string]any{"route": "server"}},
			{400, "K9O_ENVELOPE_INTEGRITY_FAILURE", map[string]any{}},
		} {
			tc := tc
			st = &routedStub{posts: []func() *http.Response{func() *http.Response {
				return jsonResponse(tc.status, map[string]any{"code": tc.code, "message": "m", "context": tc.ctx})
			}}}
			c = st.client(t)
			core = &fakeCore{}
			c.Crypto.UseClientCore(core)
			if _, k := call(c); k == nil || k.Code != tc.code || k.Status != tc.status || coreCount(core) != 0 {
				t.Fatalf("%s %d: %v", op, tc.status, k)
			}
		}
	}
}

func TestCryptoRouterFailsClosed(t *testing.T) {
	type tcase struct {
		mode     any
		code     string
		status   int
		seenMode string
	}
	for name, tc := range map[string]tcase{
		"unknown":      {modeBody("SOMETHING_NEW", "e1"), CryptoCodeCustodyUnknown, 0, "SOMETHING_NEW"},
		"missing":      {map[string]any{"epoch": "e1"}, CryptoCodeCustodyUnknown, 0, ""},
		"no epoch":     {modeBody("PLATFORM_MANAGED", ""), CryptoCodeCustodyUnknown, 0, "PLATFORM_MANAGED"},
		"404/5xx call": {503, "K9O_INTERNAL_ERROR", 500, ""},
	} {
		st := &routedStub{modes: []any{tc.mode}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		core := &fakeCore{}
		c.Crypto.UseClientCore(core)
		_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
		if kerr == nil || kerr.Code != tc.code {
			t.Fatalf("%s: got %v", name, kerr)
		}
		if tc.code == CryptoCodeCustodyUnknown {
			if kerr.Status != 0 {
				t.Fatalf("%s: SDK-local code has status %d", name, kerr.Status)
			}
			if got, _ := kerr.Context["custodyMode"].(string); got != tc.seenMode {
				t.Fatalf("%s: context %v", name, kerr.Context)
			}
		}
		if st.count("POST", "/crypto/encrypt") != 0 || core.enc != 0 {
			t.Fatalf("%s: nothing may be sent", name)
		}
	}
	st := &routedStub{modes: []any{404}, posts: []func() *http.Response{okItems()}}
	if _, _, kerr := st.client(t).Crypto.Encrypt(context.Background(), "k", oneItem); kerr == nil || kerr.Code == CryptoCodeCustodyUnknown {
		t.Fatalf("a failed mode call must keep its own error: %v", kerr)
	}
}

func TestCryptoModeMethod(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("HYOK", "abc")}}
	c := st.client(t)
	m, _, kerr := c.Crypto.Mode(context.Background())
	if kerr != nil || m.CustodyMode != "HYOK" || m.Epoch != "abc" {
		t.Fatalf("%v %+v", kerr, m)
	}
}

func TestCryptoEncryptModeCheckNamesTheCrypt(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("PLATFORM_MANAGED", "e1")}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	if _, _, kerr := c.Crypto.Encrypt(context.Background(), "customer-notes", oneItem); kerr != nil {
		t.Fatal(kerr)
	}
	if st.calls[0].method != "GET" || st.calls[0].query != "crypt=customer-notes" {
		t.Fatalf("encrypt's mode GET: %+v", st.calls[0])
	}
	if _, _, kerr := c.Crypto.Mode(context.Background()); kerr != nil {
		t.Fatal(kerr)
	}
	last := st.calls[len(st.calls)-1]
	if last.method != "GET" || last.query != "" {
		t.Fatalf("Mode() must send no query: %+v", last)
	}
}

func TestCryptoModeForSendsTheCryptAndRefusesBadNamesFirst(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("PLATFORM_MANAGED", "e1"), modeBody("PLATFORM_MANAGED", "e1")}}
	c := st.client(t)
	if _, _, kerr := c.Crypto.ModeFor(context.Background(), "customer-notes"); kerr != nil {
		t.Fatal(kerr)
	}
	if len(st.calls) != 1 || st.calls[0].method != "GET" || st.calls[0].query != "crypt=customer-notes" {
		t.Fatalf("ModeFor GET: %+v", st.calls)
	}
	if _, _, kerr := c.Crypto.Mode(context.Background()); kerr != nil {
		t.Fatal(kerr)
	}
	if st.calls[1].query != "" {
		t.Fatalf("Mode() must send no query: %+v", st.calls[1])
	}
	for _, bad := range []string{"Bad_Name", "", "actions"} {
		_, _, kerr := c.Crypto.ModeFor(context.Background(), bad)
		if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("%q: want K9O_INVALID_FIELD_VALUE, got %+v", bad, kerr)
		}
	}
	if len(st.calls) != 2 {
		t.Fatalf("a bad crypt sent a request: %+v", st.calls)
	}
}

func fedBody() map[string]any {
	return dkOK(map[string]any{"token": "eyJ.tok.sig", "expiresAt": "2026-11-02T10:15:00Z", "issuer": "https://acme.api.k9o.io", "subject": "svc_1", "audience": "kms", "kid": "k1", "projectId": "proj_1", "provider": "aws"})
}

func fedStub(post func() *http.Response) *routedStub {
	return &routedStub{modes: []any{modeBody("BYOK", "e1")}, posts: []func() *http.Response{post}}
}

func TestFederatedTokenSuccessWire(t *testing.T) {
	st := fedStub(func() *http.Response { return jsonResponse(200, fedBody()) })
	got, _, kerr := st.client(t).Crypto.FederatedToken(context.Background(), "customer-notes", "rewrap")
	if kerr != nil || got.Token != "eyJ.tok.sig" || got.ExpiresAt != "2026-11-02T10:15:00Z" || got.Issuer != "https://acme.api.k9o.io" || got.Subject != "svc_1" || got.Audience != "kms" || got.Kid != "k1" || got.ProjectID != "proj_1" || got.Provider != "aws" {
		t.Fatalf("%v %+v", kerr, got)
	}
	if len(st.calls) != 1 || st.calls[0].method != "POST" || st.calls[0].path != "/@portal/crypto/federated-token" {
		t.Fatalf("%+v", st.calls)
	}
	if len(st.calls[0].body) != 2 || st.calls[0].body["crypt"] != "customer-notes" || st.calls[0].body["op"] != "rewrap" {
		t.Fatalf("%+v", st.calls[0].body)
	}
}

func TestFederatedTokenRefusesBadInputBeforeSending(t *testing.T) {
	st := fedStub(func() *http.Response { return jsonResponse(200, fedBody()) })
	c := st.client(t)
	for _, tc := range [][2]string{{"Bad Crypt", "encrypt"}, {"", "encrypt"}, {strings.Repeat("a", 65), "encrypt"}, {"ok", "sign"}, {"ok", ""}, {"ok", "Encrypt"}} {
		_, _, kerr := c.Crypto.FederatedToken(context.Background(), tc[0], tc[1])
		if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("%v: %v", tc, kerr)
		}
	}
	if len(st.calls) != 0 {
		t.Fatalf("requests were sent: %+v", st.calls)
	}
}

func TestFederatedTokenErrorsPassThroughNoRetry(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{409, "K9O_CUSTODY_SERVER_ROUTE"}, {503, "K9O_KEY_SERVICE_UNAVAILABLE"}} {
		st := fedStub(func() *http.Response {
			return jsonResponse(tc.status, map[string]any{"code": tc.code, "message": "no"})
		})
		_, _, kerr := st.client(t).Crypto.FederatedToken(context.Background(), "k", "encrypt")
		if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status {
			t.Fatalf("%d: %v", tc.status, kerr)
		}
		if len(st.calls) != 1 {
			t.Fatalf("%d: %d requests, a POST must not be retried", tc.status, len(st.calls))
		}
	}
}

func TestFederatedTokenMalformedAnswer(t *testing.T) {
	for _, drop := range []string{"token", "expiresAt", "issuer", "subject", "audience", "kid"} {
		data := fedBody()["data"].(map[string]any)
		delete(data, drop)
		st := fedStub(func() *http.Response { return jsonResponse(200, dkOK(data)) })
		got, _, kerr := st.client(t).Crypto.FederatedToken(context.Background(), "k", "decrypt")
		if got != nil || kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" || strings.Contains(kerr.Message, "eyJ.tok.sig") {
			t.Fatalf("%s: %v %+v", drop, kerr, got)
		}
	}
}

func routedClientWithRefs(t *testing.T, st *routedStub, refs []string) *Client {
	t.Helper()
	c, kerr := NewClient(ClientOpts{
		BaseURL:         "https://acme.api.k9o.io",
		ProjectSlug:     "portal",
		APIKey:          "tok",
		HTTPClient:      &http.Client{Transport: noProbe(st.handler())},
		ExpectedKeyRefs: refs,
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	return c
}

func TestExpectedKeyRefsPassedToCore(t *testing.T) {
	refs := []string{"arn:aws:kms:eu-west-1:111122223333:key/abc"}
	for _, mode := range []string{"BYOK", "HYOK"} {
		st := &routedStub{modes: []any{modeBody(mode, "e1")}, posts: []func() *http.Response{flow409("client", mode, "e1")}}
		c := routedClientWithRefs(t, st, refs)
		core := &fakeCore{declares: true}
		c.Crypto.UseClientCore(core)
		ctx := context.Background()
		if _, _, kerr := c.Crypto.Encrypt(ctx, "k", oneItem); kerr != nil || len(core.last.ExpectedKeyRefs) != 1 || core.last.ExpectedKeyRefs[0] != refs[0] {
			t.Fatalf("encrypt %s: %v %+v", mode, kerr, core.last)
		}
		core.last = CryptoCoreOptions{}
		if _, _, kerr := c.Crypto.Decrypt(ctx, "k", oneCipher); kerr != nil || len(core.last.ExpectedKeyRefs) != 1 {
			t.Fatalf("decrypt %s: %v %+v", mode, kerr, core.last)
		}
		core.last = CryptoCoreOptions{}
		if _, _, kerr := c.Crypto.Rewrap(ctx, "k", oneCipher); kerr != nil || len(core.last.ExpectedKeyRefs) != 1 {
			t.Fatalf("rewrap %s: %v %+v", mode, kerr, core.last)
		}
		// The core gets its own copy.
		core.last.ExpectedKeyRefs[0] = "changed"
		if c.Config().ExpectedKeyRefs[0] != refs[0] {
			t.Fatal("core mutated the client's list")
		}
	}
	// The caller's slice is copied at construction.
	st := &routedStub{modes: []any{modeBody("BYOK", "e1")}}
	in := []string{"k1"}
	c := routedClientWithRefs(t, st, in)
	in[0] = "k2"
	core := &fakeCore{declares: true}
	c.Crypto.UseClientCore(core)
	if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil || core.last.ExpectedKeyRefs[0] != "k1" {
		t.Fatalf("%v %+v", kerr, core.last)
	}
}

func TestExpectedKeyRefsAbsentWhenUnset(t *testing.T) {
	st := &routedStub{modes: []any{modeBody("BYOK", "e1")}}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil || core.enc != 1 || core.last.ExpectedKeyRefs != nil {
		t.Fatalf("%v %+v", kerr, core.last)
	}
}

func TestExpectedKeyRefsInvalidRejected(t *testing.T) {
	cases := map[string][]string{
		"empty":      {},
		"blank":      {"  "},
		"empty str":  {""},
		"whitespace": {"arn:aws:kms:x y"},
		"newline":    {"key\n"},
		"wildcard":   {"arn:aws:kms:eu-west-1:111122223333:key/*"},
		"one bad":    {"good", "bad*"},
	}
	for name, refs := range cases {
		c, kerr := NewClient(ClientOpts{ExpectedKeyRefs: refs})
		if c != nil || kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" || kerr.Context["field"] != "expectedKeyRefs" || !strings.HasPrefix(kerr.Message, "expectedKeyRefs: ") {
			t.Fatalf("%s: %v", name, kerr)
		}
	}
}

func TestKeyRefPinUnenforcedRefusedBeforeCore(t *testing.T) {
	refs := []string{"arn:aws:kms:eu-west-1:111122223333:key/abc"}
	for name, mk := range map[string]func() CryptoClientCore{
		"no interface":   func() CryptoClientCore { return bareCore{&fakeCore{declares: true}} },
		"declares false": func() CryptoClientCore { return &fakeCore{} },
	} {
		for _, mode := range []string{"BYOK", "HYOK"} {
			ops := map[string]func(c *Client) *KError{
				"encrypt": func(c *Client) *KError { _, _, k := c.Crypto.Encrypt(context.Background(), "k", oneItem); return k },
				"decrypt": func(c *Client) *KError { _, _, k := c.Crypto.Decrypt(context.Background(), "k", oneCipher); return k },
				"rewrap":  func(c *Client) *KError { _, _, k := c.Crypto.Rewrap(context.Background(), "k", oneCipher); return k },
			}
			for op, call := range ops {
				st := &routedStub{modes: []any{modeBody(mode, "e1")}, posts: []func() *http.Response{flow409("client", mode, "e1")}}
				c := routedClientWithRefs(t, st, refs)
				inner := &fakeCore{declares: true}
				core := mk()
				if fc, ok := core.(*fakeCore); ok {
					inner = fc
				} else {
					core = bareCore{inner}
				}
				c.Crypto.UseClientCore(core)
				kerr := call(c)
				if kerr == nil || kerr.Code != "K9O_CRYPTO_KEY_PIN_UNENFORCED" || kerr.Status != 0 ||
					kerr.Context["custodyMode"] != mode || kerr.Context["requirement"] != "client-core-enforces-expected-key-refs" ||
					kerr.Message != "expectedKeyRefs is set but the client core does not declare that it enforces it; nothing was sent" ||
					kerr.Recommendation != "Use a client core that enforces the pin (it implements EnforcesExpectedKeyRefs and returns true), or leave ExpectedKeyRefs out." {
					t.Fatalf("%s %s %s: %+v", name, mode, op, kerr)
				}
				if inner.enc+inner.dec+inner.rew != 0 {
					t.Fatalf("%s %s %s: core was called", name, mode, op)
				}
				if st.count("POST", "/crypto/encrypt") != 0 {
					t.Fatalf("%s %s %s: plaintext was sent", name, mode, op)
				}
			}
		}
	}
}

func TestKeyRefPinUnenforcedNotRaisedWithoutList(t *testing.T) {
	for _, mode := range []string{"BYOK", "HYOK"} {
		st := &routedStub{modes: []any{modeBody(mode, "e1")}, posts: []func() *http.Response{flow409("client", mode, "e1")}}
		c := st.client(t)
		inner := &fakeCore{}
		c.Crypto.UseClientCore(bareCore{inner})
		ctx := context.Background()
		if _, _, k := c.Crypto.Encrypt(ctx, "k", oneItem); k != nil {
			t.Fatal(k)
		}
		if _, _, k := c.Crypto.Decrypt(ctx, "k", oneCipher); k != nil {
			t.Fatal(k)
		}
		if _, _, k := c.Crypto.Rewrap(ctx, "k", oneCipher); k != nil {
			t.Fatal(k)
		}
		if inner.enc != 1 || inner.dec != 1 || inner.rew != 1 {
			t.Fatalf("%+v", inner)
		}
	}
}

func TestKeyRefMatches(t *testing.T) {
	gcp := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	az := "https://Vault.Example.net/keys/Orders"
	arn := "arn:aws:kms:eu-west-1:111122223333:key/abc"
	A, G, Z, V := KeyRefAWS, KeyRefGCP, KeyRefAzure, KeyRefVault
	cases := []struct {
		ref      string
		expected []string
		prov     KeyRefProvider
		want     bool
	}{
		{arn, []string{arn}, A, true},
		{arn, []string{"arn:aws:kms:eu-west-1:111122223333:key/abd"}, A, false},
		{"alias/Orders", []string{"alias/orders"}, A, false},
		{gcp + "/cryptoKeyVersions/7", []string{gcp}, G, true},
		{gcp, []string{gcp + "/cryptoKeyVersions/3"}, G, true},
		{gcp + "/cryptoKeyVersions/7", []string{gcp + "/cryptoKeyVersions/3"}, G, true},
		{gcp + "2/cryptoKeyVersions/7", []string{gcp}, G, false},
		{az + "/0123456789abcdef0123456789ABCDEF", []string{"https://vault.example.net/keys/orders"}, Z, true},
		{az, []string{az + "/anyversion"}, Z, true},
		{az + "/v1", []string{az + "/v2"}, Z, true},
		{"https://vault.example.net/keys/other", []string{az}, Z, false},
		{"http://vault.example.net/keys/orders/v", []string{"http://vault.example.net/keys/orders"}, Z, true},
		{"transit/keys/orders:3", []string{"transit/keys/orders"}, V, true},
		{"transit/keys/orders", []string{"transit/keys/orders:7"}, V, true},
		{"transit/keys/orders:3", []string{"transit/keys/other"}, V, false},
		{"transit/a/orders", []string{"transit/b/orders"}, V, false},
		{"orders:3", []string{"orders"}, V, true},
		{"orders", []string{"/orders"}, V, true},
		{gcp + "/cryptoKeyVersions/7", []string{gcp}, A, false},
		{arn + ":1", []string{arn}, V, true},
		{gcp, nil, G, false},
		{gcp + "/cryptoKeyVersions/1", []string{"other", gcp}, G, true},
		{arn, []string{arn}, "", false},
		{arn, []string{arn}, "unknown", false},
	}
	for i, tc := range cases {
		if got := KeyRefMatches(tc.ref, tc.expected, tc.prov); got != tc.want {
			t.Fatalf("case %d (%s): got %v", i, tc.ref, got)
		}
	}
}

func TestKeyRefMatchesURLLookingVaultRef(t *testing.T) {
	ref := "https://vault.example.com:8200/transit/keys/orders:3"
	same := []string{"https://vault.example.com:8200/transit/keys/orders"}
	other := []string{"https://vault.example.com:8200/transit/keys/other"}
	if !KeyRefMatches(ref, same, KeyRefVault) {
		t.Fatal("vault rule: version dropped, same key")
	}
	if KeyRefMatches(ref, other, KeyRefVault) {
		t.Fatal("vault rule: a different key matched")
	}
	// The Azure rule keeps only the first five parts, so it would wrongly
	// accept the other key; this is why the provider is never guessed.
	if !KeyRefMatches(ref, other, KeyRefAzure) {
		t.Fatal("azure rule differs from the vault rule on this reference")
	}
}

func cryptJSON(name, status string, retired any) map[string]any {
	return map[string]any{"name": name, "status": status, "createdAt": "2026-11-02T10:00:00Z", "retiredAt": retired}
}

func TestCryptsList(t *testing.T) {
	st := &routedStub{modes: []any{map[string]any{"crypts": []any{cryptJSON("a", "active", nil), cryptJSON("b", "retired", "2026-11-03T00:00:00Z")}}}}
	got, _, kerr := st.client(t).Crypto.CryptsList(context.Background())
	if kerr != nil || len(got) != 2 || got[0].Name != "a" || got[0].RetiredAt != nil || got[1].Status != "retired" || got[1].RetiredAt == nil || *got[1].RetiredAt != "2026-11-03T00:00:00Z" {
		t.Fatalf("%v %+v", kerr, got)
	}
	if len(st.calls) != 1 || st.calls[0].method != "GET" || st.calls[0].path != "/@portal/crypto/crypts" {
		t.Fatalf("%+v", st.calls)
	}
}

func TestCryptsListEmpty(t *testing.T) {
	st := &routedStub{modes: []any{map[string]any{"crypts": []any{}}}}
	got, _, kerr := st.client(t).Crypto.CryptsList(context.Background())
	if kerr != nil || got == nil || len(got) != 0 {
		t.Fatalf("%v %+v", kerr, got)
	}
}

func TestCryptsCreateSendsOnlyName(t *testing.T) {
	st := &routedStub{posts: []func() *http.Response{func() *http.Response { return jsonResponse(201, dkOK(cryptJSON("notes", "active", nil))) }}}
	got, _, kerr := st.client(t).Crypto.CryptsCreate(context.Background(), "notes")
	if kerr != nil || got.Name != "notes" || got.Status != "active" || got.RetiredAt != nil {
		t.Fatalf("%v %+v", kerr, got)
	}
	if len(st.calls) != 1 || st.calls[0].method != "POST" || st.calls[0].path != "/@portal/crypto/crypts" || len(st.calls[0].body) != 1 || st.calls[0].body["name"] != "notes" {
		t.Fatalf("%+v", st.calls)
	}
}

func TestCryptsRetireNoBodyRightURL(t *testing.T) {
	st := &routedStub{posts: []func() *http.Response{func() *http.Response {
		return jsonResponse(200, dkOK(cryptJSON("notes", "retired", "2026-11-03T00:00:00Z")))
	}}}
	got, _, kerr := st.client(t).Crypto.CryptsRetire(context.Background(), "notes")
	if kerr != nil || got.Status != "retired" || got.RetiredAt == nil {
		t.Fatalf("%v %+v", kerr, got)
	}
	if len(st.calls) != 1 || st.calls[0].method != "POST" || st.calls[0].path != "/@portal/crypto/crypts/notes/retire" || len(st.calls[0].body) != 0 {
		t.Fatalf("%+v", st.calls)
	}
}

func TestCryptsBadNamesRefusedNothingSent(t *testing.T) {
	st := &routedStub{modes: []any{200}, posts: []func() *http.Response{func() *http.Response { return jsonResponse(201, dkOK(cryptJSON("x", "active", nil))) }}}
	c := st.client(t)
	for _, name := range []string{"", "Bad", "has space", "actions", "a/b", "../x", strings.Repeat("a", 65)} {
		_, _, k1 := c.Crypto.CryptsCreate(context.Background(), name)
		_, _, k2 := c.Crypto.CryptsRetire(context.Background(), name)
		for _, kerr := range []*KError{k1, k2} {
			if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" || kerr.Context["field"] != "name" {
				t.Fatalf("%q: %v", name, kerr)
			}
		}
	}
	if len(st.calls) != 0 {
		t.Fatalf("requests were sent: %+v", st.calls)
	}
}

func TestCryptsGatewayRefusalPassesThrough(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{409, "K9O_CRYPT_ALREADY_EXISTS"}, {409, "K9O_CRYPT_LIMIT_EXCEEDED"}, {409, "K9O_CRYPT_RETIRED"}, {404, "K9O_CRYPT_NOT_FOUND"}, {403, "K9O_RBAC_DENY"}} {
		st := &routedStub{posts: []func() *http.Response{func() *http.Response {
			return jsonResponse(tc.status, map[string]any{"code": tc.code, "message": "no"})
		}}}
		_, _, kerr := st.client(t).Crypto.CryptsRetire(context.Background(), "k")
		if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status {
			t.Fatalf("%s: %v", tc.code, kerr)
		}
	}
}

func TestCryptsMalformedAnswerFailsClosed(t *testing.T) {
	good := func() map[string]any { return cryptJSON("k", "active", nil) }
	mut := func(f func(m map[string]any)) map[string]any { m := good(); f(m); return m }
	for name, data := range map[string]any{
		"no name":      mut(func(m map[string]any) { delete(m, "name") }),
		"bad status":   mut(func(m map[string]any) { m["status"] = "gone" }),
		"no createdAt": mut(func(m map[string]any) { delete(m, "createdAt") }),
		"retiredAt 5":  mut(func(m map[string]any) { m["retiredAt"] = 5 }),
		"no retiredAt": mut(func(m map[string]any) { delete(m, "retiredAt") }),
		"not object":   "x",
	} {
		d := data
		st := &routedStub{posts: []func() *http.Response{func() *http.Response { return jsonResponse(200, dkOK(d)) }}}
		got, _, kerr := st.client(t).Crypto.CryptsRetire(context.Background(), "k")
		if got != nil || kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" || !strings.HasPrefix(kerr.Message, "crypto.crypts.retire: the gateway answered ") {
			t.Fatalf("%s: %v %+v", name, kerr, got)
		}
	}
	for name, data := range map[string]any{
		"no crypts":  map[string]any{},
		"bad member": map[string]any{"crypts": []any{map[string]any{"name": "a"}}},
	} {
		d := data
		st := &routedStub{modes: []any{d}}
		got, _, kerr := st.client(t).Crypto.CryptsList(context.Background())
		if got != nil || kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" || !strings.HasPrefix(kerr.Message, "crypto.crypts.list: the gateway answered ") {
			t.Fatalf("%s: %v %+v", name, kerr, got)
		}
	}
}

func routeMode(mode, epoch string, route any) map[string]any {
	m := modeBody(mode, epoch)
	if route != nil {
		m["route"] = route
	}
	return m
}

func TestCryptoRouteByokMissingRouteUsesClientCore(t *testing.T) {
	for _, m := range []string{"BYOK", "HYOK"} {
		st := &routedStub{modes: []any{routeMode(m, "e1", nil)}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		core := &fakeCore{}
		c.Crypto.UseClientCore(core)
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil || core.enc != 1 || st.count("POST", "/crypto/encrypt") != 0 {
			t.Fatalf("%s %v %+v %+v", m, kerr, core, st.calls)
		}
	}
}

func TestCryptoRouteByokClientRouteUsesClientCore(t *testing.T) {
	st := &routedStub{modes: []any{routeMode("BYOK", "e1", "client")}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil || core.enc != 1 || st.count("POST", "/crypto/encrypt") != 0 {
		t.Fatalf("%v %+v %+v", kerr, core, st.calls)
	}
}

func TestCryptoRouteByokServerRouteSendsToServer(t *testing.T) {
	for _, m := range []string{"BYOK", "HYOK"} {
		st := &routedStub{modes: []any{routeMode(m, "e9", "server")}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		core := &fakeCore{}
		c.Crypto.UseClientCore(core)
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil {
			t.Fatal(kerr)
		}
		if core.enc != 0 || st.count("POST", "/crypto/encrypt") != 1 || st.calls[1].body["custodyEpoch"] != "e9" {
			t.Fatalf("%s %+v %+v", m, core, st.calls)
		}
	}
}

func TestCryptoRouteUnknownRouteRefusesSendsNothing(t *testing.T) {
	for _, mode := range []string{"BYOK", "HYOK", "PLATFORM_MANAGED"} {
		for _, route := range []any{"bogus", "SERVER", 5, true, []any{"server"}} {
			st := &routedStub{modes: []any{routeMode(mode, "e1", route)}, posts: []func() *http.Response{okItems()}}
			c := st.client(t)
			core := &fakeCore{}
			c.Crypto.UseClientCore(core)
			_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
			if kerr == nil || kerr.Code != CryptoCodeCustodyUnknown || core.enc != 0 || st.count("POST", "/crypto/encrypt") != 0 {
				t.Fatalf("%s %v: %v %+v %+v", mode, route, kerr, core, st.calls)
			}
			if kerr.Context["custodyMode"] != mode {
				t.Fatalf("context %v", kerr.Context)
			}
			if s, isStr := route.(string); isStr && kerr.Context["route"] != s {
				t.Fatalf("route context %v", kerr.Context)
			}
			if _, has := kerr.Context["route"]; has && func() bool { _, ok := route.(string); return !ok }() {
				t.Fatalf("non-string route leaked into context %v", kerr.Context)
			}
		}
	}
}

// Each non-string or off-list route value is refused on its own: an explicit
// JSON null, a number other than the one in the table above, an empty string
// and the other casings. A guard that waves through any single value must fail.
func TestCryptoRouteUnknownRouteEachValueRefused(t *testing.T) {
	routes := map[string]any{
		"null":         json.RawMessage("null"),
		"number 123":   123,
		"number 0":     0,
		"empty string": "",
		"Client":       "Client",
		"bogus":        "bogus",
		"object":       map[string]any{},
	}
	for _, mode := range []string{"BYOK", "HYOK"} {
		for name, route := range routes {
			st := &routedStub{modes: []any{routeMode(mode, "e1", route)}, posts: []func() *http.Response{okItems()}}
			c := st.client(t)
			core := &fakeCore{}
			c.Crypto.UseClientCore(core)
			_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
			if kerr == nil || kerr.Code != CryptoCodeCustodyUnknown || core.enc != 0 || st.count("POST", "/crypto/encrypt") != 0 {
				t.Fatalf("%s route %s: %v %+v %+v", mode, name, kerr, core, st.calls)
			}
		}
	}
}

func TestCryptoRouteByokServerNoEpochRefuses(t *testing.T) {
	st := &routedStub{modes: []any{routeMode("BYOK", "", "server")}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	_, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem)
	if kerr == nil || kerr.Code != CryptoCodeCustodyUnknown || core.enc != 0 || st.count("POST", "/crypto/encrypt") != 0 {
		t.Fatalf("%v %+v %+v", kerr, core, st.calls)
	}
}

func TestCryptoRoutePlatformManagedUnchanged(t *testing.T) {
	for _, route := range []any{nil, "server"} {
		st := &routedStub{modes: []any{routeMode("PLATFORM_MANAGED", "e3", route)}, posts: []func() *http.Response{okItems()}}
		c := st.client(t)
		core := &fakeCore{}
		c.Crypto.UseClientCore(core)
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil || core.enc != 0 || st.count("POST", "/crypto/encrypt") != 1 {
			t.Fatalf("%v %v %+v", route, kerr, st.calls)
		}
	}
}

func TestCryptoRouteModeReadFreshEveryEncrypt(t *testing.T) {
	st := &routedStub{modes: []any{routeMode("BYOK", "e1", "server"), routeMode("BYOK", "e2", nil)}, posts: []func() *http.Response{okItems()}}
	c := st.client(t)
	core := &fakeCore{}
	c.Crypto.UseClientCore(core)
	for i := 0; i < 2; i++ {
		if _, _, kerr := c.Crypto.Encrypt(context.Background(), "k", oneItem); kerr != nil {
			t.Fatal(kerr)
		}
	}
	if st.count("GET", "/crypto/mode") != 2 || st.count("POST", "/crypto/encrypt") != 1 || core.enc != 1 {
		t.Fatalf("%+v %+v", core, st.calls)
	}
}
