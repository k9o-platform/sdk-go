package k9o

import (
	"context"
	"net/url"
	"strconv"
)

// EndUser is a person who signs in to the project's own apps.
type EndUser struct {
	// ID is "eu_<base62>".
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	EmailVerifiedAt *string `json:"emailVerifiedAt"`
	// Status is "active", "locked" or "deleted".
	Status        string   `json:"status"`
	CreatedAt     string   `json:"createdAt"`
	LastSigninAt  *string  `json:"lastSigninAt"`
	SignInMethods []string `json:"signInMethods"`
	Region        *string  `json:"region"`
}

// ListEndUsersOptions filters EndUsers.List. A zero Limit uses the gateway
// default (50).
type ListEndUsersOptions struct {
	// Status is "active", "locked", "deleted" or "all". Empty lists active and locked users.
	Status string
	// Q is an email prefix (case-sensitive), up to 320 characters.
	Q string
	// Limit is 1 to 200.
	Limit int
	// Cursor is Meta.Cursor.Next of the previous page.
	Cursor string
}

// EndUserRoleGrant is a role held by one end user. The role is a free string,
// not a fixed list.
type EndUserRoleGrant struct {
	// ID is "eurg_<base62>".
	ID        string `json:"id"`
	EndUserID string `json:"endUserId"`
	Role      string `json:"role"`
	GrantedAt string `json:"grantedAt"`
	// GrantedBy is the subject that granted the role.
	GrantedBy string `json:"grantedBy"`
}

// ListRoleGrantsOptions filters EndUsers.RoleGrants.List.
type ListRoleGrantsOptions struct {
	// EndUserID limits the list to one end user. An unknown id gives an empty list.
	EndUserID string
}

// GrantEndUserRoleInput is the body of EndUsers.RoleGrants.Grant.
type GrantEndUserRoleInput struct {
	// EndUserID is the "eu_<base62>" of an existing end user, up to 64 characters.
	EndUserID string `json:"endUserId"`
	// Role is any string, 1 to 128 characters.
	Role string `json:"role"`
}

// RevokeRoleGrantResult is the result of EndUsers.RoleGrants.Revoke.
type RevokeRoleGrantResult struct {
	// Revoked is false when the grant does not exist or was already revoked.
	Revoked   bool    `json:"revoked"`
	RevokedAt *string `json:"revokedAt"`
}

// EndUsersNamespace exposes client.EndUsers.*. Listing and reading end users
// needs the project admin role.
type EndUsersNamespace struct {
	config *httpClientConfig

	// RoleGrants manages the roles end users hold.
	RoleGrants *EndUserRoleGrantsNamespace
}

func newEndUsersNamespace(cfg *httpClientConfig) *EndUsersNamespace {
	return &EndUsersNamespace{config: cfg, RoleGrants: &EndUserRoleGrantsNamespace{config: cfg}}
}

// List returns end users, newest first, one page at a time: GET
// /@<slug>/end-users. Pass Meta.Cursor.Next of one page as Cursor of the next,
// until it is nil.
func (n *EndUsersNamespace) List(ctx context.Context, opts ListEndUsersOptions) ([]EndUser, *Meta, *KError) {
	if kerr := checkLimit(opts.Limit); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "end users")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.Status != "" {
		q["status"] = opts.Status
	}
	if opts.Q != "" {
		q["q"] = opts.Q
	}
	if opts.Limit != 0 {
		q["limit"] = strconv.Itoa(opts.Limit)
	}
	if opts.Cursor != "" {
		q["cursor"] = opts.Cursor
	}
	env, kerr := doRequest[[]EndUser](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "end-users"), Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Get returns one end user. An unknown id is a 404 K9O_END_USER_NOT_FOUND.
func (n *EndUsersNamespace) Get(ctx context.Context, id string) (*EndUser, *Meta, *KError) {
	if kerr := requireID(id, "end user"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "end users")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[EndUser](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "end-users/"+url.PathEscape(id))})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// EndUserRoleGrantsNamespace exposes client.EndUsers.RoleGrants.*. Granting and
// revoking need the project admin role; reading is open to more project roles.
type EndUserRoleGrantsNamespace struct {
	config *httpClientConfig
}

// List returns the active grants, oldest first, without paging: GET
// /@<slug>/end-user-role-grants. It unwraps the gateway's {grants} object.
func (n *EndUserRoleGrantsNamespace) List(ctx context.Context, opts ListRoleGrantsOptions) ([]EndUserRoleGrant, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "end users")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.EndUserID != "" {
		q["endUserId"] = opts.EndUserID
	}
	env, kerr := doRequest[struct {
		Grants []EndUserRoleGrant `json:"grants"`
	}](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "end-user-role-grants"), Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data.Grants, &env.Meta, nil
}

// Grant gives an end user a role: POST /@<slug>/end-user-role-grants. Granting
// a role the user already holds is a 400 K9O_INVALID_FIELD_VALUE; an unknown end
// user is a 404 K9O_NOT_FOUND. A revoked role can be granted again.
func (n *EndUserRoleGrantsNamespace) Grant(ctx context.Context, input GrantEndUserRoleInput) (*EndUserRoleGrant, *Meta, *KError) {
	if input.EndUserID == "" || len(input.EndUserID) > 64 {
		return nil, nil, invalidField("endUserId must be a string of 1 to 64 characters", nil)
	}
	if input.Role == "" || len(input.Role) > 128 {
		return nil, nil, invalidField("role must be a string of 1 to 128 characters", nil)
	}
	slug, kerr := requireProjectSlug(n.config, "end users")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[struct {
		Grant EndUserRoleGrant `json:"grant"`
	}](ctx, n.config, &requestInit{Method: "POST", Path: ProjectPath(slug, "end-user-role-grants"), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data.Grant, &env.Meta, nil
}

// Revoke revokes a grant (it is kept, marked revoked): DELETE
// /@<slug>/end-user-role-grants/<id>. An unknown or already revoked id is not an
// error: Revoked is false.
func (n *EndUserRoleGrantsNamespace) Revoke(ctx context.Context, grantID string) (*RevokeRoleGrantResult, *Meta, *KError) {
	if kerr := requireID(grantID, "grant"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "end users")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[RevokeRoleGrantResult](ctx, n.config, &requestInit{Method: "DELETE", Path: ProjectPath(slug, "end-user-role-grants/"+url.PathEscape(grantID))})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}
