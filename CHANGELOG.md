# Changelog — github.com/k9o/sdk-go

## 1.5.2

Released with `k9o.dev/sdk-go/kibo` 1.5.2, which adds linux/arm64. All SDKs are at 1.5.2 from this release.

### Changed

- `K9O_TIER_FEATURE_GATED` is no longer documented as an error to expect: the platform no longer gates features by plan.
- The LLM model type no longer has `TierGate`, which the gateway does not send.
- The `tierGating` option is removed from sign-in provider creation; the platform no longer gates features by plan.
- README: Architecture at a glance, illustrations, and the package table lists only the packages for that SDK.

## 1.5.1

Released with `k9o.dev/sdk-go/kibo` 1.5.1, which replaces the unusable `kibo` 1.5.0.

## 1.5.0

### Changed

- `Audit.Tail` and `Audit.Replay` read the gateway's real frames. The five audit fields sit inside the frame's `payload`, and the frame's SSE `id` is the feed's own id, not the audit event's: the tail now delivers one event per frame (`AuditEventID`, `Type`, `ActorType`, `OccurredAt`, `Outcome`, plus `EventID` for the frame), de-duplicates on the frame's `eventId`, resumes with `Last-Event-ID` set to the SSE `id`, and keeps the last `AuditEventID` for the catch-up.
  After a `resync` the tail now reconnects first without a position, holds the frames that arrive, replays the log after the last delivered event, then merges replay, held and live frames, each `AuditEventID` once, so nothing published during the catch-up is lost; repeated resyncs back off. `Audit.Replay` with no `Limit` sends `limit: 1000` (up to 10000 when given; outside 1 to 10000 is `K9O_INVALID_FIELD_VALUE`).
  `Audit.Replay` returns an `AuditReplayPage` (`Events`, `NextCursor`, `HasMore`) with the events flattened the same way; `Sequence` is a string and `Topic` is added. `AfterAuditEventID` wins over `FromSequence`. Before this change the tail delivered no events and a replay returned an empty list.
- `RollupCellEvent` carries the decoded payload of a `cell` frame (`CellKey`, `Bucket`, `Dims`, `Final`, `Measures`, `Status`, `Reason`, `Topic`, `OccurredAt`, `Suppressed()`), with `Data` still holding the payload as received. `EventID` is the frame's `eventId`; the resume cursor is the SSE `id`. A `resync` with the action `reread-source` reaches `OnResync`.
- `Keys.Rotate(ctx, keyID, KeysRotateOptions{StepUp})` takes an optional step-up function. On 401 `K9O_STEP_UP_REQUIRED` the SDK calls it once with the `StepUpRequest` (the challenge, the HTTP method and the URL), then sends the same request again with the `x-k9o-step-up-proof` header, and never a second time. Without it the 401 comes back with the challenge on `KError.StepUp`. `KError` gains `StepUp`, `Cause` and `Unwrap`.
- `KeyList` no longer has `NextCursor`: the key list is not paged.
- `Rampart.Classify` refuses an `input` that is not a JSON object (a string, number, list or nil) with `K9O_INVALID_FIELD_VALUE` before sending; the gateway will answer 400 for it.

### Added

- `Approvals.Comments.Add(ctx, pendingID, AddApprovalCommentInput{Body, Visibility})` posts a comment on a pending change (`POST /@<slug>/pending-approvals/<pendingId>/comments`) and `Approvals.Comments.List(ctx, pendingID, ListApprovalCommentsOptions{Limit, Cursor})` reads the comments the caller may see, oldest first, as an `ApprovalCommentPage` (`Comments`, `NextCursor`). `Visibility` is `ApprovalCommentEveryone` or `ApprovalCommentApproversOnly`, fixed when posted. `Add` checks the id, a body of 1 to 4000 characters and the visibility before sending (`K9O_INVALID_FIELD_VALUE`). Needs a session; a personal access token alone is refused with 403 `K9O_RBAC_DENY`. Other refusals: `K9O_APPROVAL_COMMENT_NOT_ELIGIBLE` (403), `K9O_APPROVAL_COMMENT_CLOSED` (409), `K9O_NOT_FOUND` (404).
- `Realtime.Replay(ctx, topic, RealtimeReplayOptions{FromSequence, UntilSequence, Limit, AfterAuditEventID})` reads a topic's events in order (`POST /@<slug>/realtime/replay`) and returns a `RealtimeReplayPage` (`Events`, `NextCursor`, `HasMore`, `Resync`). `RealtimeEvent` gains `Sequence`, set on replayed events. `Audit.Replay` is unchanged.
- `Gate.SignIn` asks the project which region holds the email first (`POST /@<slug>/gate/locate`, body `{"email": ...}` only, no credential headers, no `DefaultHeaders`) and sends the password to the origin it names only if that host is your own org, gateway family and apex with the region code added or swapped in (https; no userinfo, port, path, query or fragment; the code equals the answer's `region`). Anything else, a null origin or an unreadable answer uses the client's base URL. The located origin is not cached; a bounded hint (64) per refresh token sends `Gate.RefreshToken` and `Gate.SignOut` to the origin that issued the session and is removed on sign-out. A non-2xx answer from that step (`K9O_RATE_LIMITED`, `K9O_GATE_REGION_UNAVAILABLE`, ...) is returned as the `*KError`, the password is not sent, and nothing is retried. If the project's locate step answers 404, the client uses the base address.
- The Kibo client core matches the configured key against the call's `ExpectedKeyRefs` before it requests a federated token or exchanges a credential; a key outside the call's pin gets `K9O_CRYPTO_UNEXPECTED_KEY` on every item, with no token request and no seal or open.
- The custody check before each `Crypto.Encrypt` names the crypt (`GET /crypto/mode?crypt=<crypt>`), so a personal access token or workload policy scoped to named crypts can encrypt; `Crypto.Mode` still sends no query.
- `Crypto.ModeFor(ctx, crypt)` asks the custody mode about one crypt (`?crypt=<crypt>`); a bad crypt is refused with `K9O_INVALID_FIELD_VALUE` before sending. `Crypto.Mode(ctx)` is unchanged.
- `Crypto.Encrypt` reads the `route` field of the project's mode answer (`CryptoMode.Route`). On BYOK or HYOK only an explicit `"server"` sends plaintext to the platform (it still needs a non-empty epoch); a missing route or `"client"` uses your client core. A route that is neither `"server"` nor `"client"` is refused with `K9O_CRYPTO_CUSTODY_UNKNOWN` on any custody mode, and nothing is sent. The mode is still read fresh before every encrypt.
- `Crypto.CryptsList`, `CryptsCreate` and `CryptsRetire` manage the crypt registry (`GET`/`POST /@<slug>/crypto/crypts`, `POST .../crypts/<name>/retire`) and return `Crypt` (`Name`, `Status` `active` or `retired`, `CreatedAt`, `RetiredAt`). A bad name is refused with `K9O_INVALID_FIELD_VALUE` (field `name`) before sending, and `CryptsCreate` sends only `name`. An answer with a missing list, an unknown status, a missing `name` or `createdAt`, or a `retiredAt` that is neither a string nor null is `K9O_INTERNAL_ERROR`. Gateway refusals (`K9O_CRYPT_ALREADY_EXISTS`, `K9O_CRYPT_LIMIT_EXCEEDED`, `K9O_CRYPT_RETIRED`, `K9O_CRYPT_NOT_FOUND`, `K9O_RBAC_DENY`) come back as the `*KError`. Needs `crypto:manage_crypts` (project admins by default). A retired name only makes `Encrypt` refuse it; `Decrypt` and `Rewrap` never read the registry.
- `CryptoFederatedToken` has optional `ProjectID` and `Provider` fields, filled when the gateway names them and empty otherwise. A client core checks them against its own configuration and never follows them.
- `Crypto.FederatedToken(ctx, crypt, op)` asks for a short-lived token (15 minutes at most) that your own cloud KMS or HSM can trust (`POST /@<slug>/crypto/federated-token`) and returns `CryptoFederatedToken` (`Token`, `ExpiresAt`, `Issuer`, `Subject`, `Audience`, `Kid`). The crypt and `op` (`encrypt`, `decrypt`, `rewrap`) are checked before sending; the call is never retried, and the token is never put in an error.
- `ClientOpts.ExpectedKeyRefs` pins the keys your own KMS or HSM holds (BYOK and HYOK). `Crypto` passes the list to the registered client core on every call in a new `CryptoCoreOptions` argument, and the core refuses any other key before it uses a token or makes a network call. A core declares that it does by implementing `CryptoCoreEnforcer` (`EnforcesExpectedKeyRefs() bool`) and returning `true`; with the option set and no declaration, `Encrypt`, `Decrypt` and `Rewrap` return `CryptoCodeKeyPinUnenforced` (`K9O_CRYPTO_KEY_PIN_UNENFORCED`, no HTTP status) before the core is called. `NewClient` refuses an empty list, or a blank, spaced or `*` entry, with `K9O_INVALID_FIELD_VALUE`. `KeyRefMatches(keyRef, expected, provider)` compares by the rule of the provider you pass (required, never inferred from the reference) (AWS whole reference; GCP without the version; Azure the first five parts in lower case; Vault `<path>/<key>` without `:<version>`), with `KeyRefProvider` constants `KeyRefAWS`, `KeyRefGCP`, `KeyRefAzure` and `KeyRefVault`. `CryptoClientCore.Encrypt`, `Decrypt` and `Rewrap` take the `CryptoCoreOptions` as a last parameter.
- `Realtime.Subscribe` resumes the change feed on its own: it reconnects with `Last-Event-ID` when a stream ends (the gateway closes one after at most 300 seconds) or the connection drops,
  drops events it already delivered (by `EventID`, the last 1024 remembered), ignores heartbeats, and replaces a connection that sends no bytes for 45 seconds.
  A 4xx answer ends the subscription once; 5xx answers and network errors are retried with a delay of up to 30 seconds.
  `RealtimeEvent` gains `EventID`. `SubscribeOptions.OnResync` runs on a `resync` frame, which is not delivered as an event, and the subscription continues without a position. Delivery is at-least-once.
- `KError.RequestID` is filled from the gateway's `x-k9o-request-id` response header when the error body has none (a body value wins; empty when neither is present), on every error built from a non-2xx response, including stream opens, dataset downloads and the key-set fetch.
- `KError.RetryAfterSec` (`*float64`): the wait the gateway asked for, from the `retryAfterSec` number in the `recommendation` object; nil when there is none. Retry behavior is unchanged.
- `RequireApproval()` on single-record inserts (`InsertOne`), `Update` and `Delete`: the write is queued for approval (HTTP 202) instead of applied.
  The result is the queued change or, when an `auto_approve` policy applies the write at once, the normal result; `AsQueuedChange` and `IsQueuedWrite` tell them apart.
  Upserts and bulk writes do not take it.
- `Functions.Triggers`: `List`, `Create`, `Update` and `Delete` for a function's event triggers (`Collection`, `Events`, `Filter`, `Enabled`, `LogOkInvocations`). `Delete` answers 204, so it returns only an error.
  The SDK checks `Events` (1 to 4 of `insert`, `update`, `delete`, `upsert`) and the collection name before sending.
- `Schedules`: `List`, `Get`, `Create`, `Update`, `Delete`, `Runs` and `RunNow` for scheduled triggers. `List` takes `Status`, `FunctionName`, `Limit` and `Cursor`.
  A schedule interval under 5 minutes is rejected with 400 `K9O_INVALID_FIELD_VALUE` (field `schedule_expression`), on every plan.
- `EndUsers`: `List` and `Get` for the project's end users, and `RoleGrants.List`, `Grant` and `Revoke` for the roles they hold.
  `Role` is any string; revoking an unknown or already revoked grant answers `Revoked: false`.
- `Vectors`: `Nearest(ctx, collection, queryVector, NearestOptions{TopK, MinScore, Metric, Filters})` and `Upsert(ctx, collection, rows)` for collections of your own embeddings.
  The vector is sent in the request body (up to 16000 numbers, 1 to 1000 rows per upsert). A collection name must start with `_`.
  The SDK checks the name, the vector length and numbers, `TopK`, `Metric` and the row count before sending.
- `Approvals.List` takes `Cursor`; the next page's cursor is `meta.Cursor.Next` (nil on the last page). `Approvals.Bulk` results carry `Failed` (`ID`, `Code`, `Reason`), an empty list when no row failed; the answer is HTTP 200 even when rows fail.
- `Meta.Approval`, set on the answer to a queued write.
- `VectorSearch` on the select builder sends the embedding in the request body, so real embeddings work. It cannot be combined with `TextSearch`.
- `Custody.Status`: the project's custody mode, provider, last job and HSM details (read-only).
- `Metadata`: `Filter`, `Set`, `Search`, `Vectors.Nearest`, `Subscribe`, `Index.Get` and `Index.Declare` for the columns you declare on a collection. Column names get the `metadata.` prefix when it is missing.
  The SDK checks surfaces, `Limit` and `K` (1 to 1000), operators, `Metric`, the embedding, the 64-column limit and `Cursor` with more than one surface before sending.
  `Nearest` returns no score and at most 50 rows; `Search` has no second page; `Subscribe` events carry only `collection` and `id`, reconnect with `Last-Event-ID` and take `FromSequence` on the first connection only. `DecodeMetadataChange` reads an event's payload.
- `Rampart`: `Classify`, `JWKS` and `VerifyReceipt` for the hosted classifier. `Classify` returns the verdict and the signed receipt as received; Rampart is advisory and blocks nothing, and an unavailable classifier is an error (503 `K9O_RAMPART_CLASSIFIER_UNAVAILABLE`), never a pass.
  `VerifyReceipt` takes the `Classify` result (or a bare receipt string) and checks the key (by `kid`, from the key feed, cached with one refetch on an unknown `kid`), `alg`, `typ`, the Ed25519 signature, `iss` (the `Issuer` option, else the issuer the key feed advertises), `project_id` (the `ProjectID` option, else the one on the `Classify` result), `iat`/`exp` (60 s skew by default) and the input hash, and returns the verified claims or a `*ReceiptError` (`malformed`, `key_unknown`, `key_unavailable`, `alg`, `typ`, `signature`, `issuer`, `project`, `project_unknown`, `expired`, `not_yet_valid`, `input_hash`).
  `CanonicalJSON` is the platform's canonical form used for the input hash (keys sorted by UTF-8 byte order, integers only up to 2^53-1), so an input with a fractional number cannot be classified or verified; send such values as strings.
  A long input is scored in overlapping windows and the highest injection score decides; past 16 windows (about 6,000 tokens) the answer is 413 `K9O_RAMPART_INPUT_TOO_LONG` with no verdict, and 503 `K9O_RAMPART_CLASSIFIER_UNAVAILABLE` still means the classifier could not answer. The thresholds (calibration `rampart-launch-v1`) are a starting calibration and may move; a change is a new `calibrationVersion`.
- `Analytics`: `List`, `Get`, `Create`, `Delete`, `Query`, `Read`, `Write`, `Upload`, `DownloadOriginal` and `Materialize` for datasets, and `Analytics.Rollups` and `Analytics.Schedules` (`Create`, `List`, `Delete`, `Reattest`). `Rollups`: `List`, `Read` and `Subscribe` (closed cells live, on the change feed).
  `Reattest` is for people only: a service, workload or extension token gets 403 `K9O_RBAC_DENY` (`reason` `human_only`). A rollup or schedule pauses after its 7 to 365 days unless a person re-attests it.
- `Keys`: `List`, `Get` and `Rotate` for the project's encryption keys (metadata only, no key material). `Rotate` needs project admin and a fresh step-up; 401 `K9O_STEP_UP_REQUIRED`, 409 `K9O_STATE_CONFLICT` and 429 `K9O_RATE_LIMITED` (at most 3 rotations in 24 hours) come back as `*KError`.
- `Audit.Tail`: the project's audit events live (`AuditEventID`, `Type`, `ActorType`, `OccurredAt`, `Outcome`) on the change feed, with `Types` to narrow it. On a `replay-ledger` resync it catches up from the audit log by itself. `Audit.Replay` reads the log in order after an `AuditEventID` or from a sequence.
- `ReceiptClaims.Windows`: the number of windows a receipt's input was scored in. `CodeRampartInputTooLong` and `CodeRampartClassifierUnavailable` name the two classifier error codes.
- `KError.RetryAfterSec` also reads a `Retry-After` header given in seconds when the error body carries no wait.
- `moat` package (`k9o.dev/sdk-go/moat`): pins the system prompt, validates input and output against a JSON Schema subset, refuses role and system markers in untrusted input, and runs an optional PII filter over output.
  It runs in your process with no network call and one dependency beyond the standard library (`golang.org/x/text`, for NFKC). The pin is structural: it stops injected text from becoming the system prompt and does not stop it reaching the model.
  The marker scan matches on a normalised copy (NFKC, combining marks ignored, ignorable and format characters removed, a small set of Cyrillic and Greek look-alikes of the marker letters read as the Latin letter, case-folded, horizontal whitespace collapsed, matched line by line); the text sent to the model is unchanged.
- README sections for Rampart, Moat, writes that wait for approval, function triggers, schedules, vector search, metadata and end-user role grants.
- README: what an event-trigger function receives (the JSON payload, redaction, `triggeredAt`, and deduping on `eventId`).
- `Crypto`: `Encrypt`, `Decrypt` and `Rewrap` for server-side encryption under the project's key (`POST /@<slug>/crypto/encrypt | decrypt | rewrap`). The caller never names a key; a required `crypt`, a registered name (`[a-z0-9-]{1,64}`, not `actions`) and an optional `context` bind the ciphertext, and both must be given again on decrypt.
  Up to 100 items per call, 64 KiB per plaintext and 1 MiB per call, 256 bytes of context; the SDK checks them before sending (`K9O_INVALID_FIELD_VALUE`, `K9O_PAYLOAD_TOO_LARGE`) and does the base64. Each item has its own result or code; the item shape follows the gateway. The ciphertext is opaque and never parsed. A crypto call is never retried. Encrypt can answer 404 `K9O_CRYPT_NOT_FOUND` (name not registered) or 409 `K9O_CRYPT_RETIRED` (name retired; decrypt and rewrap are not refused for either); both pass through as `*KError`, with constants `CryptoCodeCryptNotFound` and `CryptoCodeCryptRetired`.
  `Encrypt` checks the project's custody mode on every call (`Crypto.Mode`, no cache) and routes by it: platform-managed, confidential-workload and SKR keys use the server route (the request carries `custodyEpoch`); BYOK and HYOK keys use the client route and never send plaintext to the platform. Register a Kibo client core with `Crypto.UseClientCore`; without one such a call raises `K9O_ZK_CLIENT_CORE_UNAVAILABLE` (501). An unknown, missing or malformed mode answer fails closed with `CryptoCodeCustodyUnknown` (`K9O_CRYPTO_CUSTODY_UNKNOWN`, no HTTP status) and a failed mode call returns its own error unchanged; neither sends anything. `Decrypt` and `Rewrap` try the platform first and reroute to the client core on a 409 `K9O_ZK_STORE_FLOW_REQUIRED` for the client route.
  For machine calls: encrypt needs only a grant on the crypt; decrypt and rewrap need a service token created with the crypt, a workload policy that names it, a scoped personal access token created with step-up, or a session with a fresh step-up; extensions, end users and mobile cannot call it, and IAM on the crypt decides (default floor `PROJECT_EDITOR`). The route serves platform-managed keys only: a project whose key is held in the customer's own KMS or HSM (BYOK or HYOK) gets 409 `K9O_ZK_STORE_FLOW_REQUIRED` for the whole call, pointing to client-side `kibo.encrypt` and `kibo.decrypt`, which call the customer's KMS directly (a rewrap into cloud BYOK answers the same 409).
  The ciphertext is covered by the project key's lifecycle only (a zeroize or a project purge makes it unreadable everywhere), not by per-subject erasure. Client-side Kibo, the extension route and this route are three constructions with different AAD domains, and none opens the others' ciphertext. The route is not live on the gateway yet: a 404 on `/crypto/*` is expected until it is.

### Fixed

- `Realtime.Subscribe` and `Metadata.Subscribe` (and the other feeds built on the same loop) delivered no events against the gateway. A new connection without `Last-Event-ID` opens with a `resync` frame that only reports the current head, and the feed took it for a lost position: it reconnected and called `OnResync`, over and over. A `resync` that is the first data on a connection opened without a position (no `Last-Event-ID`, no `FromSequence`) is now informational: no reconnect, no `OnResync`, and the live events on the same connection are delivered. A `resync` after an event, or on a connection that sent a position, still calls `OnResync` and reconnects without a position.
- A gateway error whose `recommendation` is an object (for example `{"action": "..."}` or `{"retryAfterSec": 30, "note": "..."}`) was reported as `K9O_INTERNAL_ERROR`. It now keeps its code, message, status and context.
  `Recommendation` stays a string: the object's `action`, else its `note`, else empty. Any other JSON type in the field also gives an empty string.

## 1.4.0 — 2026-10-26

### Added

- `Query` and `QueryNamed`: run one read-only SQL statement with positional or `:name` parameters. `QueryInto` and `QueryNamedInto` decode rows into your own type.
- `Search`: full-text search over one column of a collection.
- `TextSearch` and `VectorSearch` on the select builder, for full-text and nearest-neighbour search.
- `AI.Generate`: run a prompt through an LLM, with an optional `Model`. Same result as `Compute.LLMs.Invoke`.
- `Streams`: `Create`, `List`, `Get`, `Delete`, `Publish`, `Replay` and `Subscribe` for user data streams. `Subscribe` reconnects from the last sequence.
- `Approvals`: `List`, `Get`, `Approve`, `Reject` and `Bulk` for the pending-change queue.
- `ApprovalPolicies`: `List`, `Create` and `Delete` for the rules that decide which writes are queued.
- `Schema.Collections.List`: the project's collections by name, with cursor paging.

### Changed

- README description.
- Function deploy examples use the sandbox tier.

## 1.3.0 — 2026-10-26

Every method now matches the gateway and is checked by a contract test.

### Changed

- `Projects.Create` takes `DisplayName` (required) and optional `Slug`, `Region`, `CustodyMode`. `Get` and `Update` use the projects routes.
- `Functions.Deploy` takes `Name`, `Runtime`, `Tier` (`gateway` or `sandbox`), `ArtifactKey`, `Version` and `Description`. Inline code is gone.
- `Functions.Invoke` sends `{payload}` and accepts `InvokeOptions`. `Functions.List` accepts `Limit`, `Cursor`, `Status`, `Source`, `Tier`.
- Policies `Create`, `Update` and `Validate` take a `Policy` document. `Policies.List` takes only `status`.
- `Audit.Query` filters are `EventType`, `ActorID`, `After`, `Before`, `Limit`, `Cursor`.
- Role grants use `/role-grants` and identity providers use `/identity-providers`, with typed create and update inputs.
- `Org.DefaultLLMGet` and `DefaultLLMSet` return records. `Org.SMTP.Set` takes `PasswordRef` and `FromAddress`; `Test` takes `to` and `subject`.
- `Me.APITokens.Create` takes `Label`, `ScopeManifest` and `TTLSeconds`. `Me.Sessions.Revoke` and `RevokeAll` use the session routes.
- `Compute.LLMs.Invoke` parameters are flat and the result has `Content` and `Usage`. Stream chunks are OpenAI-style.
- `Studio.Workflows.Trigger` sends `{triggerContext}`. `Gate.SignOut` requires the refresh token.
- `Kibo.Vault.Secrets.Get` looks up a secret by exact name; the region is optional.
- The API host is read from the base URL: any `<org>.api.<apex>` host works and no domain is built in.

### Removed

- `Gate.VerifySession`, `Gate.Hosted`, `Realtime.Replay` and the polling transport.
- `Me.Notifications`, `Me.AuditLog`, `Me.DataExport`.
- `Org.Groups.Create`, `Org.Groups.Delete`, `Org.Members.Get`, `Org.SMTP.Get`.
- The `modality` filter of `Compute.LLMs.List` and the `triggerType` filter of `Studio.Workflows.List`.

### Added

- `Functions.UploadArtifact(ctx, bytes, "wasm" | "tar")` returns the `ArtifactKey` for `Deploy`.

## 1.2.0 — 2026-10-25

- The README now introduces the Kilimanjaro building blocks (Keep, Pulse, Gate, Kibo). No change to the public API.

## 1.1.1 — 2026-10-25

- Documentation links corrected: the documentation hub is https://docs.kilimanjaro.dev, with the website, platform API and console listed in a Links section. Package metadata points at the same places.

## 1.1.0 — 2026-10-25

- Documentation rewritten with runnable examples and expected output; internal cross-references removed from the published sources. No change to the public API.
- Error text for `update()` / `delete()` without an `id` filter no longer mentions a version or a roadmap.

## 1.0.0 — 2026-10-23

First public release. See the README for the full API overview.
