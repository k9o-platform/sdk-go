package k9o

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const locateBase = "https://acme.api-de01.k9o.io"

func TestChooseLocatedOriginUses(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		answer any
	}{
		{"a sibling region of the same org, family and apex", "https://acme.api-us01.k9o.io", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io"}},
		{"the base's own region", locateBase, map[string]any{"region": "de01", "origin": locateBase}},
		{"a trailing slash", "https://acme.api-us01.k9o.io", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseLocatedOrigin(locateBase, tc.answer); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChooseLocatedOriginFallsBackToBase(t *testing.T) {
	cases := []struct {
		name   string
		answer any
	}{
		{"null origin (single host, self-hosted)", map[string]any{"region": "de01", "origin": nil}},
		{"a missing answer", nil},
		{"http", map[string]any{"region": "us01", "origin": "http://acme.api-us01.k9o.io"}},
		{"userinfo", map[string]any{"region": "us01", "origin": "https://u:p@acme.api-us01.k9o.io"}},
		{"a port", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io:8443"}},
		{"a path", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io/x"}},
		{"a query", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io/?a=1"}},
		{"another org", map[string]any{"region": "us01", "origin": "https://evil.api-us01.k9o.io"}},
		{"another apex", map[string]any{"region": "us01", "origin": "https://acme.api-us01.evil.io"}},
		{"another family", map[string]any{"region": "us01", "origin": "https://acme.api-staging-us01.k9o.io"}},
		{"a code that differs from region", map[string]any{"region": "us01", "origin": "https://acme.api-fr01.k9o.io"}},
		{"a region that is not a code", map[string]any{"region": "usa", "origin": "https://acme.api-usa.k9o.io"}},
		{"a host with no region", map[string]any{"region": "us01", "origin": "https://acme.api.k9o.io"}},
		{"a region as a subdomain", map[string]any{"region": "us01", "origin": "https://us01.acme.api-de01.k9o.io"}},
		{"a look-alike apex", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io.evil.io"}},
		{"a fragment", map[string]any{"region": "us01", "origin": "https://acme.api-us01.k9o.io/#x"}},
		{"an origin that is not a string", map[string]any{"region": "us01", "origin": 7}},
		{"an answer that is not an object", "https://acme.api-us01.k9o.io"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseLocatedOrigin(locateBase, tc.answer); got != locateBase {
				t.Fatalf("got %q, want the base", got)
			}
		})
	}
}

func TestChooseLocatedOriginHTTPOnlyLoopback(t *testing.T) {
	if got := chooseLocatedOrigin("http://localhost:8080", map[string]any{"region": "de01", "origin": "http://localhost:8080"}); got != "http://localhost:8080" {
		t.Fatalf("got %q", got)
	}
	if got := chooseLocatedOrigin("http://localhost:8080", map[string]any{"region": "de01", "origin": "http://evil.localhost:8080"}); got != "http://localhost:8080" {
		t.Fatalf("got %q", got)
	}
	if got := chooseLocatedOrigin("https://localhost", map[string]any{"region": "de01", "origin": "http://localhost"}); got != "https://localhost" {
		t.Fatalf("got %q", got)
	}
}

func TestChooseLocatedOriginBaseNotGatewayShape(t *testing.T) {
	const base = "https://gw.internal.example"
	if got := chooseLocatedOrigin(base, map[string]any{"region": "us01", "origin": "https://gw-us01.internal.example"}); got != base {
		t.Fatalf("got %q", got)
	}
}

type locateSeen struct {
	method, url string
	body        map[string]any
	header      http.Header
}

type locateHarness struct {
	mu   sync.Mutex
	seen []locateSeen
}

func (h *locateHarness) urls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.seen))
	for i, s := range h.seen {
		out[i] = s.url
	}
	return out
}

func (h *locateHarness) count(suffix string) int {
	n := 0
	for _, u := range h.urls() {
		if strings.HasSuffix(u, suffix) {
			n++
		}
	}
	return n
}

var locateSession = map[string]any{"accessToken": "a", "refreshToken": "r-1", "expiresAt": "x", "refreshExpiresAt": "x"}
var locateAuthResult = map[string]any{"user": map[string]any{"id": "eu_1"}, "session": locateSession}

func okEnv(data any) *http.Response {
	return jsonResponse(200, map[string]any{"data": data, "meta": map[string]any{}})
}

func newLocateHarness(t *testing.T, answer func(url string) *http.Response) (*locateHarness, *Client) {
	t.Helper()
	h := &locateHarness{}
	rt := roundTripFunc(func(req *http.Request) *http.Response {
		var body map[string]any
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(b, &body)
		}
		h.mu.Lock()
		h.seen = append(h.seen, locateSeen{req.Method, req.URL.String(), body, req.Header.Clone()})
		h.mu.Unlock()
		return answer(req.URL.String())
	})
	c, kerr := NewClient(ClientOpts{
		BaseURL:        locateBase,
		ProjectSlug:    "portal",
		APIKey:         "key-secret",
		DefaultHeaders: map[string]string{"X-Api-Key": "default-secret"},
		HTTPClient:     &http.Client{Transport: rt},
	})
	if kerr != nil {
		t.Fatalf("NewClient: %v", kerr)
	}
	return h, c
}

func door(region string, origin any) *http.Response {
	return okEnv(map[string]any{"region": region, "origin": origin})
}

func TestSignInSendsOnlyEmailToDoorThenPasswordToLocatedOrigin(t *testing.T) {
	h, c := newLocateHarness(t, func(u string) *http.Response {
		if strings.HasSuffix(u, "/gate/locate") {
			return door("us01", "https://acme.api-us01.k9o.io")
		}
		return okEnv(locateAuthResult)
	})
	if _, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
		t.Fatal(kerr)
	}
	want := []string{locateBase + "/@portal/gate/locate", "https://acme.api-us01.k9o.io/@portal/gate/signin"}
	if got := h.urls(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("urls = %v, want %v", got, want)
	}
	door := h.seen[0]
	if len(door.body) != 1 || door.body["email"] != "a@b.co" {
		t.Fatalf("door body = %v", door.body)
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if v := door.header.Get(name); v != "" {
			t.Fatalf("door carried %s: %q", name, v)
		}
	}
	if h.seen[1].header.Get("Authorization") != "Bearer key-secret" {
		t.Fatalf("credential call lost its Authorization: %v", h.seen[1].header)
	}
}

func TestSignInNeverSendsPasswordToOriginThatFailsTheRule(t *testing.T) {
	h, c := newLocateHarness(t, func(u string) *http.Response {
		if strings.HasSuffix(u, "/gate/locate") {
			return door("us01", "https://evil.example")
		}
		return okEnv(locateAuthResult)
	})
	if _, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
		t.Fatal(kerr)
	}
	if got := h.urls()[1]; got != locateBase+"/@portal/gate/signin" {
		t.Fatalf("signin went to %s", got)
	}
	for _, u := range h.urls() {
		if strings.Contains(u, "evil.example") {
			t.Fatalf("a request went to %s", u)
		}
	}
}

func TestSignInUsesBaseWhenDoorAnswersNullOrIsMalformed(t *testing.T) {
	answers := map[string]func() *http.Response{
		"null origin": func() *http.Response { return door("de01", nil) },
		"malformed 2xx": func() *http.Response {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not json"))}
		},
		"no data":          func() *http.Response { return jsonResponse(200, map[string]any{"meta": map[string]any{}}) },
		"data is a string": func() *http.Response { return okEnv("us01") },
	}
	for name, ans := range answers {
		t.Run(name, func(t *testing.T) {
			h, c := newLocateHarness(t, func(u string) *http.Response {
				if strings.HasSuffix(u, "/gate/locate") {
					return ans()
				}
				return okEnv(locateAuthResult)
			})
			if _, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
				t.Fatal(kerr)
			}
			if got := h.urls(); len(got) != 2 || got[1] != locateBase+"/@portal/gate/signin" {
				t.Fatalf("urls = %v", got)
			}
		})
	}
}

func TestSignInUsesBaseWhenDoorAnswers404(t *testing.T) {
	h, c := newLocateHarness(t, func(u string) *http.Response {
		if strings.HasSuffix(u, "/gate/locate") {
			return jsonResponse(404, map[string]any{"code": "K9O_NOT_FOUND", "message": "x", "context": map[string]any{}, "recommendation": nil})
		}
		return okEnv(locateAuthResult)
	})
	if _, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
		t.Fatal(kerr)
	}
	if got := h.urls(); len(got) != 2 || got[0] != locateBase+"/@portal/gate/locate" || got[1] != locateBase+"/@portal/gate/signin" {
		t.Fatalf("urls = %v", got)
	}
}

func TestSignInRaisesA404OnTheCredentialRequestAfterA404Door(t *testing.T) {
	h, c := newLocateHarness(t, func(string) *http.Response {
		return jsonResponse(404, map[string]any{"code": "K9O_NOT_FOUND", "message": "x", "context": map[string]any{}, "recommendation": nil})
	})
	_, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"})
	if kerr == nil || kerr.Status != 404 {
		t.Fatalf("kerr = %+v", kerr)
	}
	if got := h.urls(); len(got) != 2 || got[1] != locateBase+"/@portal/gate/signin" {
		t.Fatalf("urls = %v", got)
	}
}

func TestSignInSendsNothingWhenDoorIsDownOrRateLimited(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{503, "K9O_GATE_REGION_UNAVAILABLE"}, {429, "K9O_RATE_LIMITED"}, {500, "K9O_INTERNAL_ERROR"}} {
		t.Run(tc.code, func(t *testing.T) {
			h, c := newLocateHarness(t, func(string) *http.Response {
				return jsonResponse(tc.status, map[string]any{"code": tc.code, "message": "x", "context": map[string]any{}, "recommendation": nil})
			})
			_, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"})
			if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status {
				t.Fatalf("kerr = %+v", kerr)
			}
			if n := len(h.urls()); n != 1 {
				t.Fatalf("requests = %d, want 1 (no retry, no credential call)", n)
			}
		})
	}
}

func TestSignInDoesNotCacheTheLocatedOrigin(t *testing.T) {
	n := 0
	h, c := newLocateHarness(t, func(u string) *http.Response {
		if strings.HasSuffix(u, "/gate/locate") {
			n++
			if n == 1 {
				return door("us01", "https://acme.api-us01.k9o.io")
			}
			return door("fr01", "https://acme.api-fr01.k9o.io")
		}
		return okEnv(locateAuthResult)
	})
	for i := 0; i < 2; i++ {
		if _, kerr := c.Gate.SignIn(context.Background(), SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
			t.Fatal(kerr)
		}
	}
	if got := h.count("/gate/locate"); got != 2 {
		t.Fatalf("locate calls = %d, want 2", got)
	}
	var signins []string
	for _, u := range h.urls() {
		if strings.HasSuffix(u, "/signin") {
			signins = append(signins, u)
		}
	}
	want := []string{"https://acme.api-us01.k9o.io/@portal/gate/signin", "https://acme.api-fr01.k9o.io/@portal/gate/signin"}
	if fmt.Sprint(signins) != fmt.Sprint(want) {
		t.Fatalf("signins = %v, want %v", signins, want)
	}
}

func TestRefreshAndSignOutGoToTheIssuingOriginThenForget(t *testing.T) {
	h, c := newLocateHarness(t, func(u string) *http.Response {
		switch {
		case strings.HasSuffix(u, "/gate/locate"):
			return door("us01", "https://acme.api-us01.k9o.io")
		case strings.HasSuffix(u, "/signin"):
			return okEnv(locateAuthResult)
		}
		return okEnv(map[string]any{"revoked": true})
	})
	ctx := context.Background()
	if _, kerr := c.Gate.SignIn(ctx, SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
		t.Fatal(kerr)
	}
	if _, kerr := c.Gate.RefreshToken(ctx, RefreshTokenInput{RefreshToken: "r-1"}); kerr != nil {
		t.Fatal(kerr)
	}
	if _, kerr := c.Gate.SignOut(ctx, SignOutInput{RefreshToken: "r-1"}); kerr != nil {
		t.Fatal(kerr)
	}
	urls := h.urls()
	has := func(want string) bool {
		for _, u := range urls {
			if u == want {
				return true
			}
		}
		return false
	}
	if !has("https://acme.api-us01.k9o.io/@portal/gate/refresh") || !has("https://acme.api-us01.k9o.io/@portal/gate/signout") {
		t.Fatalf("urls = %v", urls)
	}
	if _, kerr := c.Gate.RefreshToken(ctx, RefreshTokenInput{RefreshToken: "r-1"}); kerr != nil {
		t.Fatal(kerr)
	}
	if got := h.urls()[len(h.urls())-1]; got != locateBase+"/@portal/gate/refresh" {
		t.Fatalf("after sign-out the refresh went to %s", got)
	}
}

func TestSessionHintsAreBounded(t *testing.T) {
	var hs sessionHints
	for i := 0; i < sessionHintLimit+10; i++ {
		hs.remember(fmt.Sprintf("r-%d", i), "https://o")
	}
	if len(hs.m) != sessionHintLimit || len(hs.order) != sessionHintLimit {
		t.Fatalf("size = %d/%d, want %d", len(hs.m), len(hs.order), sessionHintLimit)
	}
	if hs.get("r-0") != "" || hs.get("r-9") != "" || hs.get(fmt.Sprintf("r-%d", sessionHintLimit+9)) == "" {
		t.Fatal("oldest hints should be evicted first")
	}
}

func TestSignUpAndWhoamiDoNotUseTheDoor(t *testing.T) {
	h, c := newLocateHarness(t, func(string) *http.Response { return okEnv(locateAuthResult) })
	ctx := context.Background()
	if _, kerr := c.Gate.SignUp(ctx, SignUpInput{Email: "a@b.co", Password: "pw-long-enough-1"}); kerr != nil {
		t.Fatal(kerr)
	}
	if _, kerr := c.Gate.Whoami(ctx); kerr != nil {
		t.Fatal(kerr)
	}
	if n := h.count("/locate"); n != 0 {
		t.Fatalf("locate calls = %d", n)
	}
}

func TestRefreshRotatesTheTokenAndTheHintFollowsIt(t *testing.T) {
	h, c := newLocateHarness(t, func(u string) *http.Response {
		switch {
		case strings.HasSuffix(u, "/gate/locate"):
			return door("us01", "https://acme.api-us01.k9o.io")
		case strings.HasSuffix(u, "/signin"):
			return okEnv(locateAuthResult)
		}
		return okEnv(map[string]any{"accessToken": "a2", "refreshToken": "r-2", "expiresAt": "x"})
	})
	ctx := context.Background()
	if _, kerr := c.Gate.SignIn(ctx, SignInInput{Email: "a@b.co", Password: "pw"}); kerr != nil {
		t.Fatal(kerr)
	}
	env, kerr := c.Gate.RefreshToken(ctx, RefreshTokenInput{RefreshToken: "r-1"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if got := env.Data.(map[string]any)["refreshToken"]; got != "r-2" {
		t.Fatalf("refreshToken = %v, want r-2", got)
	}
	if _, kerr := c.Gate.RefreshToken(ctx, RefreshTokenInput{RefreshToken: "r-2"}); kerr != nil {
		t.Fatal(kerr)
	}
	urls := h.urls()
	if got := urls[len(urls)-1]; got != "https://acme.api-us01.k9o.io/@portal/gate/refresh" {
		t.Fatalf("the second refresh went to %s", got)
	}
	if _, kerr := c.Gate.SignOut(ctx, SignOutInput{RefreshToken: "r-1"}); kerr != nil {
		t.Fatal(kerr)
	}
	urls = h.urls()
	if got := urls[len(urls)-1]; got != locateBase+"/@portal/gate/signout" {
		t.Fatalf("the old secret still had a hint: sign-out went to %s", got)
	}
}

func TestARefusedRefreshCarriesTheSessionExpiredReason(t *testing.T) {
	for _, reason := range []string{"replay", "revoked", "inactive", "max_length"} {
		_, c := newLocateHarness(t, func(string) *http.Response {
			return jsonResponse(401, map[string]any{"code": "K9O_SESSION_EXPIRED", "message": "The session has ended.", "context": map[string]any{"reason": reason}})
		})
		_, kerr := c.Gate.RefreshToken(context.Background(), RefreshTokenInput{RefreshToken: "r-1"})
		if kerr == nil || kerr.Code != "K9O_SESSION_EXPIRED" || kerr.Status != 401 || kerr.Context["reason"] != reason {
			t.Fatalf("reason %s: got %+v", reason, kerr)
		}
	}
}
