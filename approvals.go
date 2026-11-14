package k9o

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PendingChange is a change waiting for (or past) a decision. Status is
// "pending_approval", "approved", "rejected", "expired" or "redeemed".
type PendingChange struct {
	// ID is "apr_<base62>".
	ID         string `json:"id"`
	Status     string `json:"status"`
	Collection string `json:"collection"`
	// Operation is "insert", "update" or "delete".
	Operation string `json:"operation"`
	MakerID   string `json:"makerId"`
	// ProposedPayload is the proposed record for a data write; for other kinds
	// of change, a description of the action.
	ProposedPayload map[string]any `json:"proposedPayload"`
	TargetRecordID  *string        `json:"targetRecordId"`
	ApproverID      *string        `json:"approverId"`
	DecidedAt       *string        `json:"decidedAt"`
	DecisionReason  *string        `json:"decisionReason"`
	CreatedAt       string         `json:"createdAt"`
	ExpiresAt       *string        `json:"expiresAt"`
}

// QueuedChange is what a write answers with when it was queued instead of
// applied (HTTP 202): the change as it was proposed. The decision fields
// (approver, decision time and reason, creation and expiry) are not part of
// this answer; read the full change with Approvals.Get.
type QueuedChange struct {
	// ID is "apr_<base62>"; pass it to Approvals.Get, Approve or Reject.
	ID     string `json:"id"`
	Status string `json:"status"`
	// Collection is the collection the write targets.
	Collection string `json:"collection"`
	// Operation is "insert", "update" or "delete".
	Operation string `json:"operation"`
	MakerID   string `json:"makerId"`
	// ProposedPayload is the record for an insert, the patch for an update and
	// an empty object for a delete.
	ProposedPayload map[string]any `json:"proposedPayload"`
	// TargetRecordID is the record the change targets; nil for an insert.
	TargetRecordID *string `json:"targetRecordId"`
}

// IsQueuedWrite reports whether the answer to a write sent with RequireApproval
// is a queued change (HTTP 202) rather than an applied row: an auto_approve
// policy applies the write even when the flag is set.
func IsQueuedWrite(env *Envelope[any]) bool {
	_, ok := AsQueuedChange(env)
	return ok
}

// AsQueuedChange returns the queued change when the answer to a write is one,
// and false when the write was applied. A row of your own that has a "status"
// column is not mistaken for a queued change: Meta.Approval must say
// "pending_approval" too.
func AsQueuedChange(env *Envelope[any]) (*QueuedChange, bool) {
	if env == nil || env.Meta.Approval == nil || env.Meta.Approval.Status != "pending_approval" {
		return nil, false
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["status"] != "pending_approval" {
		return nil, false
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	var q QueuedChange
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, false
	}
	return &q, true
}

// ListApprovalsOptions filters Approvals.List. Empty Status means all
// statuses; a zero Limit uses the gateway default (50).
type ListApprovalsOptions struct {
	// Status is one of "pending_approval", "approved", "rejected", "expired", "redeemed".
	Status string
	// Limit is 1 to 200.
	Limit int
	// Cursor is the opaque Meta.Cursor.Next of the previous page. Use it with
	// the same Status and Limit; the gateway refuses it otherwise.
	Cursor string
}

// ApproveInput is the body of Approvals.Approve. Reason is an optional note
// stored with the decision; nil omits it.
type ApproveInput struct {
	Reason *string `json:"reason,omitempty"`
}

// RejectInput is the body of Approvals.Reject. Reason is required and must be
// non-empty.
type RejectInput struct {
	Reason string `json:"reason"`
}

// BulkApprovalsInput is the body of Approvals.Bulk.
type BulkApprovalsInput struct {
	// Action is "approve" or "reject".
	Action string `json:"action"`
	// Criteria is a predicate over each pending change, for example
	// {"eq": ["change.table", "orders"]}. It must not be nil.
	Criteria map[string]any `json:"criteria"`
	// Reason is required when Action is "reject".
	Reason string `json:"reason,omitempty"`
}

// BulkApprovalFailure is one matched change that Approvals.Bulk could not
// decide.
type BulkApprovalFailure struct {
	// ID is the pending change, "apr_<base62>".
	ID string `json:"id"`
	// Code is a K9O_* error code, for example "K9O_STATE_CONFLICT". Treat it as
	// an open set.
	Code string `json:"code"`
	// Reason is the error message for this row.
	Reason string `json:"reason"`
}

// BulkApprovalsResult is the result of Approvals.Bulk. Every matched change is
// in exactly one of AffectedIDs and Failed. The HTTP status is 200 even when
// rows fail, and rows decided before a failure stay decided, so compare
// AffectedIDs with the set you meant to decide.
type BulkApprovalsResult struct {
	AttestationID string   `json:"attestationId"`
	Action        string   `json:"action"`
	AffectedCount int      `json:"affectedCount"`
	AffectedIDs   []string `json:"affectedIds"`
	// Failed lists the matched changes that were not decided; empty (never
	// nil) when none failed.
	Failed []BulkApprovalFailure `json:"failed"`
	Reason *string               `json:"reason"`
}

// ApprovalPolicy is a rule: writes to Collection with Operation that match
// Predicate get Action.
type ApprovalPolicy struct {
	// ID is "apo_<base62>".
	ID         string         `json:"id"`
	Collection string         `json:"collection"`
	Operation  string         `json:"operation"`
	Predicate  map[string]any `json:"predicate"`
	// Action is "auto_approve" or "queue".
	Action     string `json:"action"`
	AuthoredBy string `json:"authoredBy"`
	CreatedAt  string `json:"createdAt"`
}

// CreateApprovalPolicyInput is the body of ApprovalPolicies.Create.
type CreateApprovalPolicyInput struct {
	// Collection matches ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$.
	Collection string `json:"collection"`
	// Operation is "insert", "update" or "delete".
	Operation string `json:"operation"`
	// Action is "auto_approve" or "queue".
	Action string `json:"action"`
	// Predicate is a non-nil predicate object.
	Predicate map[string]any `json:"predicate"`
}

// ApprovalPolicyDeleted is the result of ApprovalPolicies.Delete.
type ApprovalPolicyDeleted struct {
	ID string `json:"id"`
}

var approvalCollectionRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

func invalidField(message string, context map[string]any) *KError {
	return &KError{Code: "K9O_INVALID_FIELD_VALUE", Message: message, Context: context}
}

func requireID(id, what string) *KError {
	if id == "" {
		return invalidField(what+" id must be a non-empty string", nil)
	}
	return nil
}

func requireReason(reason string) *KError {
	if strings.TrimSpace(reason) == "" {
		return invalidField("reason is required and must be a non-empty string", nil)
	}
	return nil
}

func checkLimit(limit int) *KError {
	if limit != 0 && (limit < 1 || limit > 200) {
		return invalidField("limit must be a whole number from 1 to 200", map[string]any{"limit": limit})
	}
	return nil
}

// ApprovalsNamespace exposes client.Approvals.* (the pending-change queue).
type ApprovalsNamespace struct {
	config *httpClientConfig
	// Comments reads and posts the comments on a pending change.
	Comments *ApprovalCommentsNamespace
}

func newApprovalsNamespace(cfg *httpClientConfig) *ApprovalsNamespace {
	return &ApprovalsNamespace{config: cfg, Comments: &ApprovalCommentsNamespace{config: cfg}}
}

func approvalsPath(slug, rest string) string {
	p := "pending-approvals"
	if rest != "" {
		p += "/" + rest
	}
	return ProjectPath(slug, p)
}

// List returns pending changes, newest first: GET /@<slug>/pending-approvals.
// A page holds Limit changes (1 to 200, default 50); when more exist,
// Meta.Cursor.Next is set: pass it as Cursor for the next page. It is nil on
// the last page. An invalid cursor answers 400 K9O_INVALID_CURSOR.
// Project admins and auditors see the whole queue; other members see only the
// changes they proposed.
func (n *ApprovalsNamespace) List(ctx context.Context, opts ListApprovalsOptions) ([]PendingChange, *Meta, *KError) {
	if kerr := checkLimit(opts.Limit); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.Status != "" {
		q["status"] = opts.Status
	}
	if opts.Limit != 0 {
		q["limit"] = strconv.Itoa(opts.Limit)
	}
	if opts.Cursor != "" {
		q["cursor"] = opts.Cursor
	}
	env, kerr := doRequest[[]PendingChange](ctx, n.config, &requestInit{Method: "GET", Path: approvalsPath(slug, ""), Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Get returns one pending change. Members who are not queue admins can read
// only their own.
func (n *ApprovalsNamespace) Get(ctx context.Context, id string) (*PendingChange, *Meta, *KError) {
	if kerr := requireID(id, "approval"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[PendingChange](ctx, n.config, &requestInit{Method: "GET", Path: approvalsPath(slug, url.PathEscape(id))})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Approve approves a pending change. Read Status on the result:
// "pending_approval" means your approval was recorded but more approvers are
// needed and the change is not applied yet.
func (n *ApprovalsNamespace) Approve(ctx context.Context, id string, input ApproveInput) (*PendingChange, *Meta, *KError) {
	if kerr := requireID(id, "approval"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[PendingChange](ctx, n.config, &requestInit{Method: "POST", Path: approvalsPath(slug, url.PathEscape(id)+"/approve"), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Reject rejects a pending change. Reason is required.
func (n *ApprovalsNamespace) Reject(ctx context.Context, id string, input RejectInput) (*PendingChange, *Meta, *KError) {
	if kerr := requireReason(input.Reason); kerr != nil {
		return nil, nil, kerr
	}
	if kerr := requireID(id, "approval"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[PendingChange](ctx, n.config, &requestInit{Method: "POST", Path: approvalsPath(slug, url.PathEscape(id)+"/reject"), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Bulk approves or rejects every pending change matching Criteria. It answers
// 200 even when rows fail: they are listed in Failed, and rows already decided
// are not rolled back. Compare AffectedIDs with what you intended.
func (n *ApprovalsNamespace) Bulk(ctx context.Context, input BulkApprovalsInput) (*BulkApprovalsResult, *Meta, *KError) {
	if input.Action != "approve" && input.Action != "reject" {
		return nil, nil, invalidField(`action must be "approve" or "reject"`, nil)
	}
	if input.Criteria == nil {
		return nil, nil, invalidField("criteria must be a predicate object", nil)
	}
	if input.Action == "reject" {
		if kerr := requireReason(input.Reason); kerr != nil {
			return nil, nil, kerr
		}
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[BulkApprovalsResult](ctx, n.config, &requestInit{Method: "POST", Path: approvalsPath(slug, "bulk"), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	if env.Data.Failed == nil {
		env.Data.Failed = []BulkApprovalFailure{}
	}
	return &env.Data, &env.Meta, nil
}

// ApprovalPoliciesNamespace exposes client.ApprovalPolicies.* (the rules that
// decide which writes are queued).
type ApprovalPoliciesNamespace struct {
	config *httpClientConfig
}

func approvalPoliciesPath(slug, rest string) string {
	p := "approval-policies"
	if rest != "" {
		p += "/" + rest
	}
	return ProjectPath(slug, p)
}

// List returns all policies, oldest first. The first matching policy for a
// write decides.
func (n *ApprovalPoliciesNamespace) List(ctx context.Context) ([]ApprovalPolicy, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "approvalPolicies")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[[]ApprovalPolicy](ctx, n.config, &requestInit{Method: "GET", Path: approvalPoliciesPath(slug, "")})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Create creates a policy. Identical policies are not de-duplicated.
func (n *ApprovalPoliciesNamespace) Create(ctx context.Context, input CreateApprovalPolicyInput) (*ApprovalPolicy, *Meta, *KError) {
	if !approvalCollectionRE.MatchString(input.Collection) {
		return nil, nil, invalidField("collection must match ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$", map[string]any{"collection": input.Collection})
	}
	if input.Operation != "insert" && input.Operation != "update" && input.Operation != "delete" {
		return nil, nil, invalidField(`operation must be "insert", "update" or "delete"`, nil)
	}
	if input.Action != "auto_approve" && input.Action != "queue" {
		return nil, nil, invalidField(`action must be "auto_approve" or "queue"`, nil)
	}
	if input.Predicate == nil {
		return nil, nil, invalidField("predicate must be a predicate object", nil)
	}
	slug, kerr := requireProjectSlug(n.config, "approvalPolicies")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[ApprovalPolicy](ctx, n.config, &requestInit{Method: "POST", Path: approvalPoliciesPath(slug, ""), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Delete deletes a policy.
func (n *ApprovalPoliciesNamespace) Delete(ctx context.Context, id string) (*ApprovalPolicyDeleted, *Meta, *KError) {
	if kerr := requireID(id, "policy"); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvalPolicies")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[ApprovalPolicyDeleted](ctx, n.config, &requestInit{Method: "DELETE", Path: approvalPoliciesPath(slug, url.PathEscape(id))})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Visibility values of an approval comment. It is fixed when the comment is
// posted.
const (
	// ApprovalCommentEveryone reaches everyone who can read the change.
	ApprovalCommentEveryone = "everyone"
	// ApprovalCommentApproversOnly reaches only people eligible to decide the
	// change, never the person who asked for it.
	ApprovalCommentApproversOnly = "approvers_only"
)

// approvalCommentMaxChars is the longest comment body, in characters.
const approvalCommentMaxChars = 4000

// ApprovalComment is one comment on a pending change.
type ApprovalComment struct {
	// ID is "acm_<id>".
	ID string `json:"id"`
	// CaseID is the pending change the comment is on.
	CaseID string `json:"caseId"`
	// AuthorID is the author's opaque user id.
	AuthorID string `json:"authorId"`
	// Visibility is "everyone" or "approvers_only".
	Visibility string `json:"visibility"`
	// Body is plain text. Render it as text, never as HTML.
	Body string `json:"body"`
	// CreatedAt is an RFC 3339 date-time.
	CreatedAt string `json:"createdAt"`
}

// AddApprovalCommentInput is the body of Approvals.Comments.Add.
type AddApprovalCommentInput struct {
	// Body is plain text of 1 to 4000 characters.
	Body string `json:"body"`
	// Visibility is required: "everyone" or "approvers_only".
	Visibility string `json:"visibility"`
}

// ListApprovalCommentsOptions are the optional parameters of
// Approvals.Comments.List.
type ListApprovalCommentsOptions struct {
	// Limit is 1 to 200; zero uses the gateway default (50).
	Limit int
	// Cursor is the NextCursor of the previous page.
	Cursor string
}

// ApprovalCommentPage is one page of comments, oldest first.
type ApprovalCommentPage struct {
	Comments []ApprovalComment
	// NextCursor is nil on the last page.
	NextCursor *string
}

// ApprovalCommentsNamespace exposes client.Approvals.Comments.*.
type ApprovalCommentsNamespace struct {
	config *httpClientConfig
}

// Add posts a comment on a pending change:
// POST /@<slug>/pending-approvals/<pendingId>/comments. A comment is never a
// decision. There is no edit or delete; a correction is a new comment.
// It needs a session: a personal access token alone is refused with 403
// K9O_RBAC_DENY. Other refusals: 403 K9O_APPROVAL_COMMENT_NOT_ELIGIBLE, 409
// K9O_APPROVAL_COMMENT_CLOSED (decided, expired or cancelled), 404
// K9O_NOT_FOUND, 400 K9O_INVALID_FIELD_VALUE.
func (n *ApprovalCommentsNamespace) Add(ctx context.Context, pendingID string, input AddApprovalCommentInput) (*ApprovalComment, *Meta, *KError) {
	if kerr := requireID(pendingID, "pending approval"); kerr != nil {
		return nil, nil, kerr
	}
	if length := utf8.RuneCountInString(input.Body); length < 1 || length > approvalCommentMaxChars {
		return nil, nil, invalidField("body must be 1 to 4000 characters", map[string]any{"field": "body"})
	}
	if input.Visibility != ApprovalCommentEveryone && input.Visibility != ApprovalCommentApproversOnly {
		return nil, nil, invalidField(`visibility must be "everyone" or "approvers_only"`, map[string]any{"field": "visibility"})
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[ApprovalComment](ctx, n.config, &requestInit{Method: "POST", Path: approvalsPath(slug, url.PathEscape(pendingID)+"/comments"), Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// List returns the comments on a pending change the caller may see, oldest
// first: GET /@<slug>/pending-approvals/<pendingId>/comments. A page holds
// Limit comments (1 to 200, default 50); pass NextCursor as Cursor for the
// next page. A bad cursor answers 400 K9O_INVALID_CURSOR. Comments stay
// readable after the change is decided.
func (n *ApprovalCommentsNamespace) List(ctx context.Context, pendingID string, opts ListApprovalCommentsOptions) (*ApprovalCommentPage, *Meta, *KError) {
	if kerr := requireID(pendingID, "pending approval"); kerr != nil {
		return nil, nil, kerr
	}
	if kerr := checkLimit(opts.Limit); kerr != nil {
		return nil, nil, kerr
	}
	slug, kerr := requireProjectSlug(n.config, "approvals")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.Limit != 0 {
		q["limit"] = strconv.Itoa(opts.Limit)
	}
	if opts.Cursor != "" {
		q["cursor"] = opts.Cursor
	}
	var raw []byte
	env, kerr := doRequest[[]ApprovalComment](ctx, n.config, &requestInit{Method: "GET", Path: approvalsPath(slug, url.PathEscape(pendingID)+"/comments"), Query: q, RawResponse: &raw})
	if kerr != nil {
		return nil, nil, kerr
	}
	var sibling struct {
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &sibling); err != nil {
		return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: "malformed comments answer: expected {data, nextCursor, meta}", Status: 200}
	}
	comments := env.Data
	if comments == nil {
		comments = []ApprovalComment{}
	}
	return &ApprovalCommentPage{Comments: comments, NextCursor: sibling.NextCursor}, &env.Meta, nil
}
