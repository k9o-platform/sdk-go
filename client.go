package k9o

import (
	"net/http"
)

// ClientOpts configures NewClient. All fields are optional.
type ClientOpts struct {
	// BaseURL is the gateway origin (scheme + host); rejects path/query/fragment.
	// OPTIONAL: empty defaults to DefaultBaseURL (a convenience, never used to
	// parse a host). When BaseURL is a platform host (`api.<apex>` or
	// `api-staging.<apex>`, any apex) and OrgSlug is set, the SDK derives the org
	// host `<OrgSlug>.<host>`. Any other host (an org host, an internal or
	// air-gapped host, localhost) is used unchanged.
	BaseURL string
	// APIKey is the default bearer token; per-call override is supported by
	// internal request helpers (not currently exposed on the public API).
	APIKey string
	// OrgSlug is the org context; sent as X-K9O-Org-Slug header.
	OrgSlug string
	// ProjectSlug is the project context; sent as X-K9O-Project-Slug header.
	ProjectSlug string
	// DefaultHeaders are merged into every request.
	DefaultHeaders map[string]string
	// HTTPClient overrides the default http.Client (useful for tests + timeouts).
	HTTPClient *http.Client
	// RetryConfig overrides retry/backoff; nil ⇒ defaults
	// (3 retries, exponential) on idempotent methods + transient failures.
	RetryConfig *RetryConfig
	// ExpectedKeyRefs lists the key identifiers (ARNs, resource names, key
	// URLs) your own KMS or HSM keys have. Set it when the project uses BYOK or
	// HYOK: the client core is handed the list on every call and must refuse,
	// before it uses any token or makes any network call, a key that is not in
	// it. The key version is ignored in the comparison. Take the values from
	// your own cloud configuration, never from the platform, and scope the
	// cloud role to those keys, never "*". Nil means not set; an empty list, a
	// blank entry, an entry with whitespace or one containing "*" is refused
	// by NewClient with K9O_INVALID_FIELD_VALUE.
	ExpectedKeyRefs []string
}

// Client is the customer-facing SDK client. Namespaces hang off the
// struct via promoted fields:
//
//	client.Projects.List(ctx)
//	client.Gate.SignIn(ctx, ...)
//
// Data DSL via the From method:
//
//	data, meta, kerr := k9o.SelectExecute[YourRowType](ctx, client.From("posts").Select())
type Client struct {
	config *httpClientConfig

	Projects         *ProjectsNamespace
	Gate             *GateNamespace
	Studio           *StudioNamespace
	Compute          *ComputeNamespace
	AI               *AINamespace
	Realtime         *RealtimeNamespace
	Streams          *StreamsNamespace
	Org              *OrgNamespace
	Orgs             *OrgsNamespace
	Migrations       *MigrationsNamespace
	Functions        *FunctionsNamespace
	Policies         *PoliciesNamespace
	Approvals        *ApprovalsNamespace
	ApprovalPolicies *ApprovalPoliciesNamespace
	Schema           *SchemaNamespace
	Audit            *AuditNamespace
	Me               *MeNamespace
	Kibo             *KiboNamespace
	Storage          *StorageNamespace
	Schedules        *SchedulesNamespace
	EndUsers         *EndUsersNamespace
	Vectors          *VectorsNamespace
	Custody          *CustodyNamespace
	Metadata         *MetadataNamespace
	Rampart          *RampartNamespace
	Analytics        *AnalyticsNamespace
	Rollups          *RollupsNamespace
	Keys             *KeysNamespace
	Crypto           *CryptoNamespace
}

// NewClient constructs a Client bound to the supplied options. Resolves
// BaseURL (optional → default; platform host + OrgSlug → derived org host;
// any other host passes through) and validates it at construct time; returns
// *KError(K9O_INVALID_BASE_URL) on violation.
func NewClient(opts ClientOpts) (*Client, *KError) {
	normalised, kerr := ResolveBaseURL(opts.BaseURL, opts.OrgSlug)
	if kerr != nil {
		return nil, kerr
	}
	refs, kerr := checkExpectedKeyRefs(opts.ExpectedKeyRefs)
	if kerr != nil {
		return nil, kerr
	}
	cfg := &httpClientConfig{
		BaseURL:         normalised,
		APIKey:          opts.APIKey,
		OrgSlug:         opts.OrgSlug,
		ProjectSlug:     opts.ProjectSlug,
		DefaultHeaders:  opts.DefaultHeaders,
		HTTPClient:      opts.HTTPClient,
		Retry:           opts.RetryConfig,
		ExpectedKeyRefs: refs,
	}
	return &Client{
		config:           cfg,
		Projects:         &ProjectsNamespace{config: cfg},
		Gate:             &GateNamespace{config: cfg},
		Studio:           newStudioNamespace(cfg),
		Compute:          newComputeNamespace(cfg),
		AI:               &AINamespace{config: cfg},
		Realtime:         &RealtimeNamespace{config: cfg},
		Streams:          &StreamsNamespace{config: cfg},
		Org:              newOrgNamespace(cfg),
		Orgs:             &OrgsNamespace{config: cfg},
		Migrations:       &MigrationsNamespace{config: cfg},
		Functions:        newFunctionsNamespace(cfg),
		Policies:         &PoliciesNamespace{config: cfg},
		Approvals:        newApprovalsNamespace(cfg),
		ApprovalPolicies: &ApprovalPoliciesNamespace{config: cfg},
		Schema:           newSchemaNamespace(cfg),
		Audit:            &AuditNamespace{config: cfg},
		Me:               newMeNamespace(cfg),
		Kibo:             newKiboNamespace(cfg),
		Storage:          &StorageNamespace{config: cfg},
		Schedules:        &SchedulesNamespace{config: cfg},
		EndUsers:         newEndUsersNamespace(cfg),
		Vectors:          &VectorsNamespace{config: cfg},
		Custody:          &CustodyNamespace{config: cfg},
		Metadata:         newMetadataNamespace(cfg),
		Rampart:          newRampartNamespace(cfg),
		Analytics:        newAnalyticsNamespace(cfg),
		Rollups:          &RollupsNamespace{config: cfg},
		Keys:             &KeysNamespace{config: cfg},
		Crypto:           &CryptoNamespace{config: cfg},
	}, nil
}

// Config returns the bound HTTP config (read-only access for advanced use).
func (c *Client) Config() ClientOpts {
	return ClientOpts{
		BaseURL:         c.config.BaseURL,
		APIKey:          c.config.APIKey,
		OrgSlug:         c.config.OrgSlug,
		ProjectSlug:     c.config.ProjectSlug,
		DefaultHeaders:  c.config.DefaultHeaders,
		HTTPClient:      c.config.HTTPClient,
		ExpectedKeyRefs: append([]string(nil), c.config.ExpectedKeyRefs...),
	}
}

// From is the data-plane DSL entry point.
//
//	rows, meta, kerr := k9o.SelectExecute[Post](ctx, client.From("posts").Select().Eq("status", "published"))
func (c *Client) From(collection string) *FromBuilder {
	return &FromBuilder{config: c.config, collection: collection}
}
