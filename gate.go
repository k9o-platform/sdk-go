package k9o

import "context"

// SignInInput is the input shape for Gate.SignIn.
type SignInInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignUpInput is the input shape for Gate.SignUp.
type SignUpInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
}

// SignOutInput is the input shape for Gate.SignOut.
type SignOutInput struct {
	RefreshToken string `json:"refreshToken"`
}

// RefreshTokenInput is the input shape for Gate.RefreshToken.
type RefreshTokenInput struct {
	RefreshToken string `json:"refreshToken"`
}

// GateNamespace exposes client.Gate.* — Gate IAK auth surface.
// Requires ProjectSlug bound on the client (project-scoped).
type GateNamespace struct {
	config *httpClientConfig
}

func (g *GateNamespace) requireProject() (string, *KError) {
	if g.config.ProjectSlug == "" {
		return "", &KError{
			Code:    "K9O_MISSING_PROJECT_CONTEXT",
			Message: "client.Gate operations require ProjectSlug bound on the client",
			Context: map[string]any{},
		}
	}
	return g.config.ProjectSlug, nil
}

// SignUp creates a new end-user identity.
func (g *GateNamespace) SignUp(ctx context.Context, input SignUpInput) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	return doRequest[any](ctx, g.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "gate/signup"),
		Body:   input,
	})
}

// SignIn signs in an existing end-user with email + password. It first asks the
// project which region holds the email (one request carrying only the email) and
// sends the password only to a region host of your own org and apex, else to the
// client's base. A non-2xx answer from that step (429 K9O_RATE_LIMITED, 503
// K9O_GATE_REGION_UNAVAILABLE, ...) is returned as the *KError and the password
// is not sent; the call is safe to retry.
func (g *GateNamespace) SignIn(ctx context.Context, input SignInInput) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	origin, kerr := g.locateOrigin(ctx, slug, input.Email)
	if kerr != nil {
		return nil, kerr
	}
	env, kerr := doRequest[any](ctx, g.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "gate/signin"),
		Body:   input,
		Origin: origin,
	})
	if kerr != nil {
		return nil, kerr
	}
	g.config.hints.remember(sessionRefreshToken(env.Data), origin)
	return env, nil
}

// SignOut revokes the current session.
func (g *GateNamespace) SignOut(ctx context.Context, input SignOutInput) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	env, kerr := doRequest[any](ctx, g.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "gate/signout"),
		Body:   input,
		Origin: g.config.hints.get(input.RefreshToken),
	})
	g.config.hints.forget(input.RefreshToken)
	return env, kerr
}

// Whoami returns the active end-user identity.
func (g *GateNamespace) Whoami(ctx context.Context) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	return doRequest[any](ctx, g.config, &requestInit{
		Method: "GET",
		Path:   ProjectPath(slug, "gate/whoami"),
	})
}

// RevokeAllSessions ends every live session of the signed-in end user, on every device, this one included
// (POST /@<project>/auth/sessions/revoke-all). Call it with the end user's access token as the client's bearer token
// (the token Whoami uses), not a refresh token. The answer's Data is {revokedCount}. Each ended session's next refresh
// returns a *KError with Code "K9O_SESSION_EXPIRED" and Context["reason"] "revoked"; access tokens already issued keep
// working until they expire. An operator's token returns "K9O_TRUST_ZONE_VIOLATION" (403), and a token issued for
// another project "K9O_PROJECT_SCOPE_MISMATCH" (403).
func (g *GateNamespace) RevokeAllSessions(ctx context.Context) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	return doRequest[any](ctx, g.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "auth/sessions/revoke-all"),
	})
}

// RefreshToken exchanges the refresh token for a new access token and a new refresh token. The answer's Data is
// {accessToken, refreshToken, expiresAt}. The refresh token you sent stops working: store the returned one before
// the next refresh, and refresh one at a time per session. A second call with a secret that was already used is a
// replay: the session is revoked and the call returns a *KError with Code "K9O_SESSION_EXPIRED" and Context["reason"]
// "replay"; the newer secret then gets "revoked". The other reasons are "inactive" and "max_length".
func (g *GateNamespace) RefreshToken(ctx context.Context, input RefreshTokenInput) (*Envelope[any], *KError) {
	slug, kerr := g.requireProject()
	if kerr != nil {
		return nil, kerr
	}
	env, kerr := doRequest[any](ctx, g.config, &requestInit{
		Method: "POST",
		Path:   ProjectPath(slug, "gate/refresh"),
		Body:   input,
		Origin: g.config.hints.get(input.RefreshToken),
	})
	if kerr != nil {
		return nil, kerr
	}
	g.config.hints.rotate(input.RefreshToken, env.Data)
	return env, nil
}
