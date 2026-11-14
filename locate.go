package k9o

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Locating the end user's region for the credential flows.
//
// region.go finds the org's regional API host. An end user can live in another
// region of the project, so password sign-in first asks the project which
// region holds the address (POST /@<slug>/gate/locate {"email": ...} at that
// regional base) and sends the credential to the origin it names. The answer is
// the same for a known and an unknown address.
//
// Rules:
//   - The located origin is used for that one attempt. It is never cached as
//     authority. The one thing kept is a hint per refresh token, so refresh and
//     sign-out reach the region that issued the session; a stale hint gets a 401.
//   - An origin is used only if it is https (http only when the base is itself
//     http on a loopback host and the origin is loopback too), has no userinfo,
//     port, path, query or fragment, and its host is exactly the base host's
//     org, family and apex with the region code added or swapped in the service
//     label, and that code equals the answer's region. Anything else, or a null
//     origin, means the base is used. A password never goes to a host that
//     fails this.

var regionCodeRegex = regexp.MustCompile(`^[a-z]{2}\d{2}$`)

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "127.0.0.1" || h == "::1"
}

// chooseLocatedOrigin returns the origin the credential goes to: the located
// one if it passes the rule, else base. answer is the decoded "data" of the
// locate envelope. Pure; never panics.
func chooseLocatedOrigin(base string, answer any) string {
	m, ok := answer.(map[string]any)
	if !ok {
		return base
	}
	origin, ok := m["origin"].(string)
	if !ok {
		return base
	}
	region, ok := m["region"].(string)
	if !ok || !regionCodeRegex.MatchString(region) {
		return base
	}
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return base
	}
	u, err := url.Parse(origin)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return base
	}
	if u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return base
	}
	if u.Path != "" && u.Path != "/" {
		return base
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && b.Scheme == "http" && isLoopbackHost(b.Hostname()) && isLoopbackHost(u.Hostname())) {
		return base
	}
	bh, uh := strings.ToLower(b.Hostname()), strings.ToLower(u.Hostname())
	if u.Scheme == b.Scheme && uh == bh && b.Port() == "" {
		return b.Scheme + "://" + b.Host
	}
	from, to := parseAPIHost(bh), parseAPIHost(uh)
	if from == nil || to == nil || to.Region != region {
		return base
	}
	if from.OrgSlug != to.OrgSlug || from.Family != to.Family || from.Apex != to.Apex {
		return base
	}
	return u.Scheme + "://" + uh
}

// locateOrigin asks the project which region holds email and returns the origin
// the credential goes to. A non-2xx answer from the door is returned as the
// usual *KError and the caller must not send the credential, except 404: the
// project has no locate step, so the base is returned. A 2xx answer that
// cannot be read means the base.
func (g *GateNamespace) locateOrigin(ctx context.Context, slug, email string) (string, *KError) {
	base := g.config.resolveOrigin(ctx)
	env, kerr := doRequest[json.RawMessage](ctx, g.config, &requestInit{
		Method:    "POST",
		Path:      ProjectPath(slug, "gate/locate"),
		Body:      map[string]string{"email": email},
		Anonymous: true,
		Origin:    base,
	})
	if kerr != nil {
		if kerr.Status >= 200 && kerr.Status < 300 {
			return base, nil // malformed 2xx body
		}
		if kerr.Status == 404 {
			return base, nil // no locate step on this project: use the base
		}
		return "", kerr
	}
	var answer any
	if len(env.Data) == 0 || json.Unmarshal(env.Data, &answer) != nil {
		return base, nil
	}
	return chooseLocatedOrigin(base, answer), nil
}

const sessionHintLimit = 64

// sessionHints is a bounded refresh token -> origin map. A hint, never authority.
type sessionHints struct {
	mu    sync.Mutex
	order []string
	m     map[string]string
}

func (h *sessionHints) remember(refreshToken, origin string) {
	if refreshToken == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]string{}
	}
	h.removeLocked(refreshToken)
	h.m[refreshToken] = origin
	h.order = append(h.order, refreshToken)
	for len(h.order) > sessionHintLimit {
		delete(h.m, h.order[0])
		h.order = h.order[1:]
	}
}

func (h *sessionHints) get(refreshToken string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.m[refreshToken]
}

func (h *sessionHints) forget(refreshToken string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(refreshToken)
}

func (h *sessionHints) removeLocked(refreshToken string) {
	if _, ok := h.m[refreshToken]; !ok {
		return
	}
	delete(h.m, refreshToken)
	for i, k := range h.order {
		if k == refreshToken {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

// rotate moves the hint from a refresh token that was just used to the one the refresh answered with: the old one
// stops working, and the session is still issued by the same origin. A data without a new token changes nothing.
func (h *sessionHints) rotate(old string, data any) {
	d, _ := data.(map[string]any)
	next, _ := d["refreshToken"].(string)
	origin := h.get(old)
	if next == "" || origin == "" {
		return
	}
	h.forget(old)
	h.remember(next, origin)
}

// sessionRefreshToken reads data.session.refreshToken from a sign-in answer.
func sessionRefreshToken(data any) string {
	d, _ := data.(map[string]any)
	s, _ := d["session"].(map[string]any)
	t, _ := s["refreshToken"].(string)
	return t
}
