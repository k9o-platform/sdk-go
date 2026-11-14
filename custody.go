package k9o

import "context"

// CustodyLastJob is the newest custody change of a project.
type CustodyLastJob struct {
	// Kind is "transition" or "external_transition".
	Kind string `json:"kind"`
	// Status is "pending", "running", "completed" or "failed".
	Status string `json:"status"`
	// CompletedAt is an ISO 8601 time; nil until the job finishes.
	CompletedAt *string `json:"completedAt"`
}

// CustodyHSMAttestation is the attestation state of an attached HSM. Both
// fields are nil today.
type CustodyHSMAttestation struct {
	Status     *string `json:"status"`
	VerifiedAt *string `json:"verifiedAt"`
}

// CustodyHSM describes the HSM behind a project whose provider is "hsm".
type CustodyHSM struct {
	// Vendor is "thales-luna", "entrust-nshield", "aws-cloudhsm" or nil.
	Vendor      *string               `json:"vendor"`
	ClusterID   string                `json:"clusterId"`
	Region      string                `json:"region"`
	Attestation CustodyHSMAttestation `json:"attestation"`
	// Keys is nil today.
	Keys []map[string]any `json:"keys"`
}

// CustodyStatus is the answer to Custody.Status.
type CustodyStatus struct {
	// Mode is the project's custody mode, for example "PLATFORM_MANAGED".
	Mode string `json:"mode"`
	// Attached is true when an external key or HSM is attached.
	Attached bool `json:"attached"`
	// Provider is "platform", "external_kms", "hsm" or nil.
	Provider *string `json:"provider"`
	// LastJob is nil when the project never changed custody.
	LastJob *CustodyLastJob `json:"lastJob"`
	// HSM is nil unless Provider is "hsm" (a missing key also reads as nil).
	HSM *CustodyHSM `json:"hsm"`
}

// CustodyNamespace exposes client.Custody.*: the project's key custody.
// Changing custody is a Console action.
type CustodyNamespace struct {
	config *httpClientConfig
}

// Status reads the project's custody state. It needs the project admin or
// project auditor role: another role answers 403 K9O_RBAC_DENY, a caller who
// is not a project member 404 K9O_NOT_FOUND, and an attached project whose key
// manager cannot answer 502 K9O_UPSTREAM_UNAVAILABLE.
func (n *CustodyNamespace) Status(ctx context.Context) (*CustodyStatus, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "custody")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[CustodyStatus](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "custody/status")})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}
