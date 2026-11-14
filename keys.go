package k9o

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// KeyByokRef identifies a customer-held key (BYOK) without revealing it.
type KeyByokRef struct {
	// Provider is the key management service, for example "aws-kms".
	Provider *string `json:"provider"`
	Region   *string `json:"region"`
	// ExternalKeyFingerprint is the first 16 characters of base64url(SHA-256)
	// of your key's identifier, so you can match it to your key. The
	// identifier itself is never returned.
	ExternalKeyFingerprint *string `json:"externalKeyFingerprint"`
}

// KeyHyokRef identifies a key held in your HSM cluster (HYOK).
type KeyHyokRef struct {
	ClusterID   *string `json:"clusterId"`
	ClusterName *string `json:"clusterName"`
	// ExternalKeyFingerprint is the first 16 characters of base64url(SHA-256)
	// of the HSM key handle. The handle itself is never returned.
	ExternalKeyFingerprint *string `json:"externalKeyFingerprint"`
}

// Key is one version of a project encryption key: metadata only, never key material.
type Key struct {
	// ID is the key id, "vkey_<uuid>". One entry per key version.
	ID string `json:"id"`
	// Version starts at 1 and goes up by one on each rotation.
	Version *int `json:"version"`
	// Status is "active", "rotating" or "revoked".
	Status *string `json:"status"`
	// CustodyMode is "PLATFORM_MANAGED", "SKR", "BYOK" or "HYOK".
	CustodyMode *string `json:"custodyMode"`
	CreatedAt   *string `json:"createdAt"`
	// RotatedAt is when the key was last rotated; nil if never.
	RotatedAt *string `json:"rotatedAt"`
	// ByokRef is set for a BYOK key, nil otherwise.
	ByokRef *KeyByokRef `json:"byokRef"`
	// HyokRef is set for a HYOK key, nil otherwise.
	HyokRef *KeyHyokRef `json:"hyokRef"`
}

// KeyList is the answer to Keys.List.
type KeyList struct {
	Items []Key `json:"items"`
}

// KeyRotation is the answer to Keys.Rotate.
type KeyRotation struct {
	KeyID           string  `json:"keyId"`
	PreviousVersion *int    `json:"previousVersion"`
	NewVersion      *int    `json:"newVersion"`
	Status          *string `json:"status"` // "rotated"
}

// KeysListOptions configures Keys.List.
type KeysListOptions struct {
	// All also lists the rotating and revoked versions. Default: the active key only.
	All bool
}

// KeysNamespace exposes client.Keys.*: the inventory of the project's
// encryption keys. Metadata only; no key material is ever returned.
type KeysNamespace struct {
	config *httpClientConfig
}

func (n *KeysNamespace) path(sub string) (string, *KError) {
	slug, kerr := requireProjectSlug(n.config, "keys")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "vault/keys"+sub), nil
}

// List lists the project's encryption keys (GET /@<slug>/vault/keys): the
// active key by default, every version with KeysListOptions{All: true}. The
// list is not paged. It
// needs PROJECT_ADMIN or PROJECT_AUDITOR on the project and a person's
// credential: end-user, extension, service and workload tokens answer 403
// K9O_TRUST_ZONE_VIOLATION.
func (n *KeysNamespace) List(ctx context.Context, opts KeysListOptions) (*KeyList, *Meta, *KError) {
	p, kerr := n.path("")
	if kerr != nil {
		return nil, nil, kerr
	}
	init := &requestInit{Method: "GET", Path: p}
	if opts.All {
		init.Query = map[string]string{"status": "all"}
	}
	env, kerr := doRequest[KeyList](ctx, n.config, init)
	if kerr != nil {
		return nil, nil, kerr
	}
	if env.Data.Items == nil {
		env.Data.Items = []Key{}
	}
	return &env.Data, &env.Meta, nil
}

// Get returns one key by id (GET /@<slug>/vault/keys/<keyId>). Same access as List.
func (n *KeysNamespace) Get(ctx context.Context, keyID string) (*Key, *Meta, *KError) {
	if kerr := requireID(keyID, "key"); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("/" + url.PathEscape(keyID))
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[Key](ctx, n.config, &requestInit{Method: "GET", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// StepUpChallenge is the challenge a 401 K9O_STEP_UP_REQUIRED carries (in
// Context["stepUp"], and as KError.StepUp). Answer it with a proof and call
// again: Keys.Rotate does this through KeysRotateOptions.StepUp.
type StepUpChallenge struct {
	// ScopeSetRoot and Nonce are echoed back unchanged in the proof header.
	ScopeSetRoot string `json:"scopeSetRoot"`
	Nonce        string `json:"nonce"`
	// MinTier is the lowest assurance tier that satisfies the challenge, for
	// example "T3_KEY_POSSESSION".
	MinTier string `json:"minTier"`
	// Methods are the proof methods on offer: "webauthn", "dpop" or "mtls".
	Methods []string `json:"methods"`
	// MaxAgeSec is how long the proof may be (120).
	MaxAgeSec int `json:"maxAgeSec"`
	// SingleUse is true: the proof is good for one request.
	SingleUse bool `json:"singleUse"`
	// RpID is the WebAuthn relying party id, when the gateway names one.
	RpID string `json:"rpId,omitempty"`
}

func stepUpFromContext(ctx map[string]any) *StepUpChallenge {
	raw, ok := ctx["stepUp"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var c StepUpChallenge
	if json.Unmarshal(b, &c) != nil || c.ScopeSetRoot == "" || c.Nonce == "" {
		return nil
	}
	return &c
}

// StepUpRequest is what a step-up hook receives: the challenge and the request
// that is being retried. A DPoP proof binds to HTTPMethod and URL.
type StepUpRequest struct {
	Challenge StepUpChallenge
	// HTTPMethod is the method of the request to retry, for example "POST".
	HTTPMethod string
	// URL is the full URL of that request.
	URL string
}

// StepUpProof is a hook's answer: one of the offered methods and that method's
// own proof (a WebAuthn assertion, a DPoP proof JWT, an mTLS proof), as a
// string. The SDK does not read it and never logs it.
type StepUpProof struct {
	Method string
	Proof  string
}

// StepUpFunc produces the proof for a step-up challenge. No gateway route
// mints one: your code does, with the user's key. Return an error to give up;
// the SDK then returns the original 401 with your error as KError.Cause.
type StepUpFunc func(ctx context.Context, req StepUpRequest) (StepUpProof, error)

// stepUpHeader is the request header that carries the proof.
const stepUpHeader = "x-k9o-step-up-proof"

// encodeStepUpProof builds the header value: base64url (no padding) of
// {scopeSetRoot, nonce, method, proof}.
func encodeStepUpProof(c StepUpChallenge, p StepUpProof) (string, error) {
	b, err := json.Marshal(struct {
		ScopeSetRoot string `json:"scopeSetRoot"`
		Nonce        string `json:"nonce"`
		Method       string `json:"method"`
		Proof        string `json:"proof"`
	}{c.ScopeSetRoot, c.Nonce, p.Method, p.Proof})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// KeysRotateOptions configures Keys.Rotate.
type KeysRotateOptions struct {
	// StepUp answers the step-up challenge. When the first request gets 401
	// K9O_STEP_UP_REQUIRED, the SDK calls it once with the challenge and sends
	// the same request again with the proof in the x-k9o-step-up-proof header.
	// Without it, Rotate returns the 401 and the challenge is in KError.StepUp.
	StepUp StepUpFunc
}

// Rotate rotates the project's active key (POST /@<slug>/vault/keys/<keyId>/rotate):
// a new version becomes active, the data keys are re-wrapped under it and the
// old version is revoked. If the re-wrap fails nothing changes.
//
// It needs PROJECT_ADMIN, a person's credential and step-up. Errors arrive as
// *KError:
//   - 401 K9O_STEP_UP_REQUIRED: the challenge is in KError.StepUp (and
//     Context["stepUp"]). With opts.StepUp set, the SDK answers it once and
//     retries the same request with the proof header; the second answer is
//     returned as it is, whatever it is. A proof the gateway cannot read is a
//     401 K9O_STEP_UP_PROOF_MALFORMED.
//   - 409 K9O_STATE_CONFLICT: Context["reason"] is "rotation_in_flight" (one
//     rotation at a time) or "key_not_active" (only the active key rotates).
//   - 429 K9O_RATE_LIMITED: Context["reason"] is "rotation_rate_cap" (at most 3
//     rotations in a rolling 24 hours) or "key_service_busy". RetryAfterSec
//     holds the wait when the gateway sent one (body or Retry-After header).
//
// A rotation is never retried by the SDK except for that one step-up retry.
func (n *KeysNamespace) Rotate(ctx context.Context, keyID string, opts KeysRotateOptions) (*KeyRotation, *Meta, *KError) {
	if kerr := requireID(keyID, "key"); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("/" + url.PathEscape(keyID) + "/rotate")
	if kerr != nil {
		return nil, nil, kerr
	}
	init := &requestInit{Method: "POST", Path: p, Body: map[string]any{}}
	env, kerr := doRequest[KeyRotation](ctx, n.config, init)
	if kerr != nil && kerr.Code == "K9O_STEP_UP_REQUIRED" && kerr.Status == 401 && opts.StepUp != nil && kerr.StepUp != nil {
		header, herr := n.stepUpHeader(ctx, opts.StepUp, kerr, init)
		if herr != nil {
			return nil, nil, herr
		}
		retry := *init
		retry.Headers = map[string]string{stepUpHeader: header}
		env, kerr = doRequest[KeyRotation](ctx, n.config, &retry)
	}
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// stepUpHeader runs the hook and builds the header. On a hook failure it
// returns the original 401 with the hook's error as Cause.
func (n *KeysNamespace) stepUpHeader(ctx context.Context, hook StepUpFunc, orig *KError, init *requestInit) (string, *KError) {
	ch := *orig.StepUp
	proof, err := hook(ctx, StepUpRequest{
		Challenge:  ch,
		HTTPMethod: init.Method,
		URL:        strings.TrimRight(n.config.resolveOrigin(ctx), "/") + init.Path,
	})
	fail := func(cause error) *KError {
		out := *orig
		out.Cause = cause
		return &out
	}
	if err != nil {
		return "", fail(err)
	}
	if proof.Method == "" || proof.Proof == "" {
		return "", fail(errors.New("step-up hook returned an empty method or proof"))
	}
	offered := false
	for _, m := range ch.Methods {
		if m == proof.Method {
			offered = true
		}
	}
	if len(ch.Methods) > 0 && !offered {
		return "", fail(fmt.Errorf("step-up method %q is not one of the offered methods %v", proof.Method, ch.Methods))
	}
	header, err := encodeStepUpProof(ch, proof)
	if err != nil {
		return "", fail(err)
	}
	return header, nil
}
