package k9o

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"testing"
)

type fakeReply struct {
	status int
	body   any // nil with status 204
}

func workloadClient(t *testing.T, calls *[]streamsCall, reply fakeReply) *Client {
	t.Helper()
	return p6Client(t, roundTripFunc(func(req *http.Request) *http.Response {
		*calls = append(*calls, recordCall(req))
		if reply.status == 204 {
			return &http.Response{StatusCode: 204, Body: io.NopCloser(http.NoBody), Header: http.Header{}}
		}
		return jsonResponse(reply.status, reply.body)
	}))
}

func wantCall(t *testing.T, name string, got streamsCall, method, path string, query map[string]string, body string) {
	t.Helper()
	if got.Method != method || got.Path != path || got.Body != body || !reflect.DeepEqual(got.Query, query) {
		t.Fatalf("%s wire: got %+v, want %s %s %v %q", name, got, method, path, query, body)
	}
}

var queuedInsertBody = map[string]any{
	"data": map[string]any{
		"id": "apr_5oLshD2MVT27", "status": "pending_approval", "collection": "conformance_kv", "operation": "insert",
		"makerId": "usr_conformance0001", "proposedPayload": map[string]any{"id": "contract2-probe-0001", "val": "contract2-probe"},
		"targetRecordId": nil,
	},
	"meta": map[string]any{
		"cost": map[string]any{}, "timing": map[string]any{},
		"context":  map[string]any{"requestId": "req_1", "traceId": "req_1", "projectId": "proj_1"},
		"approval": map[string]any{"status": "pending_approval"},
	},
}

func TestRequireApprovalInsertQueued(t *testing.T) {
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{202, queuedInsertBody})
	rec := map[string]any{"id": "contract2-probe-0001", "val": "contract2-probe"}
	env, kerr := c.From("conformance_kv").InsertOne(rec).RequireApproval().Execute(context.Background())
	if kerr != nil {
		t.Fatalf("Execute: %v", kerr)
	}
	wantCall(t, "insert", calls[0], "POST", "/@portal/data/conformance_kv", map[string]string{"requireApproval": "true"}, `{"id":"contract2-probe-0001","val":"contract2-probe"}`)
	q, ok := AsQueuedChange(env)
	if !ok || !IsQueuedWrite(env) {
		t.Fatalf("queued write not recognised: %+v", env)
	}
	if q.ID != "apr_5oLshD2MVT27" || q.Status != "pending_approval" || q.Operation != "insert" || q.Collection != "conformance_kv" ||
		q.MakerID != "usr_conformance0001" || q.TargetRecordID != nil || q.ProposedPayload["val"] != "contract2-probe" {
		t.Fatalf("QueuedChange: %+v", q)
	}
	if env.Meta.Approval == nil || env.Meta.Approval.Status != "pending_approval" {
		t.Fatalf("meta.approval: %+v", env.Meta.Approval)
	}
}

func TestRequireApprovalAppliedRowIsNotQueued(t *testing.T) {
	ctx := context.Background()
	// An auto_approve policy applies the write even with the flag: plain 201 row.
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{201, map[string]any{"data": map[string]any{"id": "r1", "val": "x"}, "meta": map[string]any{}}})
	env, kerr := c.From("conformance_kv").InsertOne(map[string]any{"id": "r1"}).RequireApproval().Execute(ctx)
	if kerr != nil || IsQueuedWrite(env) {
		t.Fatalf("applied row taken for queued: %+v %v", env, kerr)
	}
	// A row with its own "status" column is not mistaken for a queued change.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"id": "r1", "status": "pending_approval"}, "meta": map[string]any{}}})
	env, kerr = c.From("conformance_kv").Update(map[string]any{"a": 1}).Eq("id", "r1").RequireApproval().Execute(ctx)
	if kerr != nil || IsQueuedWrite(env) {
		t.Fatalf("row with status column taken for queued: %+v %v", env, kerr)
	}
	if IsQueuedWrite(nil) {
		t.Fatal("nil is not a queued write")
	}
}

func TestRequireApprovalUpdateDeleteWire(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	queued := func(op string, target any, payload map[string]any) map[string]any {
		return map[string]any{
			"data": map[string]any{"id": "apr_2", "status": "pending_approval", "collection": "orders", "operation": op, "makerId": "usr_1", "proposedPayload": payload, "targetRecordId": target},
			"meta": map[string]any{"approval": map[string]any{"status": "pending_approval"}},
		}
	}
	c := workloadClient(t, &calls, fakeReply{202, queued("update", "o-1", map[string]any{"total": 5})})
	env, kerr := c.From("orders").Update(map[string]any{"total": 5}).Eq("id", "o-1").RequireApproval().Execute(ctx)
	q, ok := AsQueuedChange(env)
	if kerr != nil || !ok || q.Operation != "update" || q.TargetRecordID == nil || *q.TargetRecordID != "o-1" {
		t.Fatalf("update: %+v %v", q, kerr)
	}
	wantCall(t, "update", calls[0], "PATCH", "/@portal/data/orders/o-1", map[string]string{"requireApproval": "true"}, `{"total":5}`)

	calls = nil
	c = workloadClient(t, &calls, fakeReply{202, queued("delete", "o-1", map[string]any{})})
	env, kerr = c.From("orders").Delete().Eq("id", "o-1").RequireApproval().Execute(ctx)
	q, ok = AsQueuedChange(env)
	if kerr != nil || !ok || q.Operation != "delete" || len(q.ProposedPayload) != 0 {
		t.Fatalf("delete: %+v %v", q, kerr)
	}
	wantCall(t, "delete", calls[0], "DELETE", "/@portal/data/orders/o-1", map[string]string{"requireApproval": "true"}, "")
}

func TestWritesWithoutRequireApprovalSendNoFlag(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"id": "o-1"}, "meta": map[string]any{}}})
	c.From("orders").Insert(ctx, map[string]any{"id": "o-1"})
	c.From("orders").InsertOne(map[string]any{"id": "o-1"}).Execute(ctx)
	c.From("orders").Update(map[string]any{"a": 1}).Eq("id", "o-1").Execute(ctx)
	c.From("orders").Delete().Eq("id", "o-1").Execute(ctx)
	if len(calls) != 4 {
		t.Fatalf("calls: %d", len(calls))
	}
	for i, call := range calls {
		if len(call.Query) != 0 {
			t.Fatalf("call %d sent a query: %+v", i, call)
		}
	}
}

func TestFunctionTriggersWire(t *testing.T) {
	ctx := context.Background()
	trigger := map[string]any{
		"id": "0b8f6a52-3f0e-4c8e-9a47-5d2b0f1c7e11", "functionName": "notify", "collection": "orders", "events": []any{"insert", "update"},
		"filter": nil, "enabled": true, "logOkInvocations": false, "createdAt": "2026-10-26T17:59:56.815Z",
	}
	var calls []streamsCall

	// List: empty live fixture.
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{}, "meta": map[string]any{}}})
	got, _, kerr := c.Functions.Triggers.List(ctx, "w4c-probe")
	if kerr != nil || len(got) != 0 {
		t.Fatalf("List: %+v %v", got, kerr)
	}
	wantCall(t, "list", calls[0], "GET", "/@portal/functions/w4c-probe/triggers", map[string]string{}, "")

	// Create.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{201, map[string]any{"data": trigger, "meta": map[string]any{}}})
	tr, _, kerr := c.Functions.Triggers.Create(ctx, "notify", CreateTriggerInput{Collection: "orders", Events: []string{"insert", "update"}})
	if kerr != nil || tr.ID != trigger["id"] || tr.Filter != nil || *tr.Collection != "orders" || !tr.Enabled || len(tr.Events) != 2 {
		t.Fatalf("Create: %+v %v", tr, kerr)
	}
	wantCall(t, "create", calls[0], "POST", "/@portal/functions/notify/triggers", map[string]string{}, `{"collection":"orders","events":["insert","update"]}`)

	calls = nil
	off := false
	c.Functions.Triggers.Create(ctx, "notify", CreateTriggerInput{Collection: "orders", Events: []string{"upsert"}, Filter: map[string]any{"eq": []any{"resource.status", "paid"}}, Enabled: &off, LogOkInvocations: true})
	wantCall(t, "create full", calls[0], "POST", "/@portal/functions/notify/triggers", map[string]string{},
		`{"collection":"orders","events":["upsert"],"filter":{"eq":["resource.status","paid"]},"enabled":false,"logOkInvocations":true}`)

	// Update: enabled only, then clear filter.
	calls = nil
	c.Functions.Triggers.Update(ctx, "notify", "0b8f6a52", UpdateTriggerInput{Enabled: &off})
	wantCall(t, "update", calls[0], "PATCH", "/@portal/functions/notify/triggers/0b8f6a52", map[string]string{}, `{"enabled":false}`)
	calls = nil
	c.Functions.Triggers.Update(ctx, "notify", "0b8f6a52", UpdateTriggerInput{ClearFilter: true})
	wantCall(t, "update clear", calls[0], "PATCH", "/@portal/functions/notify/triggers/0b8f6a52", map[string]string{}, `{"filter":null}`)
	calls = nil
	c.Functions.Triggers.Update(ctx, "notify", "0b8f6a52", UpdateTriggerInput{Events: []string{"delete"}, Filter: map[string]any{"eq": []any{"a", 1}}})
	wantCall(t, "update events+filter", calls[0], "PATCH", "/@portal/functions/notify/triggers/0b8f6a52", map[string]string{}, `{"events":["delete"],"filter":{"eq":["a",1]}}`)

	// Delete: 204, no body.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{status: 204})
	if kerr := c.Functions.Triggers.Delete(ctx, "notify", "0b8f6a52"); kerr != nil {
		t.Fatalf("Delete 204: %v", kerr)
	}
	wantCall(t, "delete", calls[0], "DELETE", "/@portal/functions/notify/triggers/0b8f6a52", map[string]string{}, "")
}

func TestFunctionTriggersValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{}, "meta": map[string]any{}}})
	tt := c.Functions.Triggers
	for name, f := range map[string]func() *KError{
		"no events": func() *KError { _, _, k := tt.Create(ctx, "f", CreateTriggerInput{Collection: "orders"}); return k },
		"five events": func() *KError {
			_, _, k := tt.Create(ctx, "f", CreateTriggerInput{Collection: "orders", Events: []string{"insert", "update", "delete", "upsert", "insert"}})
			return k
		},
		"bad event": func() *KError {
			_, _, k := tt.Create(ctx, "f", CreateTriggerInput{Collection: "orders", Events: []string{"truncate"}})
			return k
		},
		"bad collection": func() *KError {
			_, _, k := tt.Create(ctx, "f", CreateTriggerInput{Collection: "1orders", Events: []string{"insert"}})
			return k
		},
		"empty name":  func() *KError { _, _, k := tt.List(ctx, ""); return k },
		"empty patch": func() *KError { _, _, k := tt.Update(ctx, "f", "id", UpdateTriggerInput{}); return k },
		"bad patch event": func() *KError {
			_, _, k := tt.Update(ctx, "f", "id", UpdateTriggerInput{Events: []string{"x"}})
			return k
		},
		"empty trigger id": func() *KError { return tt.Delete(ctx, "f", "") },
	} {
		wantInvalid(t, name, f(), calls)
	}

	// Gateway error surfaces as a KError.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{404, map[string]any{"code": "K9O_FUNCTION_NOT_FOUND", "message": "no such function", "context": map[string]any{}}})
	if _, _, kerr := c.Functions.Triggers.List(ctx, "gone"); kerr == nil || kerr.Code != "K9O_FUNCTION_NOT_FOUND" {
		t.Fatalf("404: %v", kerr)
	}
}

func TestSchedulesWire(t *testing.T) {
	ctx := context.Background()
	sched := map[string]any{
		"id": "7f0c", "functionName": "nightly-report", "scheduleExpression": "0 0 * * *", "scheduleTimezone": "Europe/Berlin", "catchupPolicy": "skip",
		"collection": nil, "enabled": true, "status": "active", "logOkInvocations": false, "createdAt": "t", "lastInvocationAt": nil, "nextInvocationAt": nil,
	}
	var calls []streamsCall

	// List: empty live fixture, no options.
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{}, "meta": map[string]any{"cursor": map[string]any{"next": nil}}}})
	got, meta, kerr := c.Schedules.List(ctx, ListSchedulesOptions{})
	if kerr != nil || len(got) != 0 || meta.Cursor == nil || meta.Cursor.Next != nil {
		t.Fatalf("List: %+v %+v %v", got, meta, kerr)
	}
	wantCall(t, "list", calls[0], "GET", "/@portal/forge/schedules", map[string]string{}, "")

	// List with every filter: functionName goes out as function_name.
	calls = nil
	c.Schedules.List(ctx, ListSchedulesOptions{Status: "paused", FunctionName: "nightly-report", Limit: 10, Cursor: "abc"})
	wantCall(t, "list filtered", calls[0], "GET", "/@portal/forge/schedules",
		map[string]string{"status": "paused", "function_name": "nightly-report", "limit": "10", "cursor": "abc"}, "")

	// Get / Create / Update.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": sched, "meta": map[string]any{}}})
	s, _, kerr := c.Schedules.Get(ctx, "7f0c")
	if kerr != nil || s.Status != "active" || s.ScheduleTimezone != "Europe/Berlin" || s.Collection != nil {
		t.Fatalf("Get: %+v %v", s, kerr)
	}
	wantCall(t, "get", calls[0], "GET", "/@portal/forge/schedules/7f0c", map[string]string{}, "")

	calls = nil
	c.Schedules.Create(ctx, CreateScheduleInput{FunctionName: "nightly-report", ScheduleExpression: "@daily"})
	wantCall(t, "create", calls[0], "POST", "/@portal/forge/schedules", map[string]string{}, `{"functionName":"nightly-report","scheduleExpression":"@daily"}`)
	calls = nil
	off := false
	c.Schedules.Create(ctx, CreateScheduleInput{FunctionName: "f", ScheduleExpression: "0 * * * *", ScheduleTimezone: "Europe/Berlin", CatchupPolicy: "run_once", Collection: "orders", Enabled: &off, LogOkInvocations: true})
	wantCall(t, "create full", calls[0], "POST", "/@portal/forge/schedules", map[string]string{},
		`{"functionName":"f","scheduleExpression":"0 * * * *","scheduleTimezone":"Europe/Berlin","catchupPolicy":"run_once","collection":"orders","enabled":false,"logOkInvocations":true}`)

	calls = nil
	c.Schedules.Update(ctx, "7f0c", UpdateScheduleInput{Status: "paused"})
	wantCall(t, "update", calls[0], "PATCH", "/@portal/forge/schedules/7f0c", map[string]string{}, `{"status":"paused"}`)
	calls = nil
	c.Schedules.Update(ctx, "7f0c", UpdateScheduleInput{Enabled: &off, LogOkInvocations: &off})
	wantCall(t, "update bools", calls[0], "PATCH", "/@portal/forge/schedules/7f0c", map[string]string{}, `{"enabled":false,"logOkInvocations":false}`)

	// Runs.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{map[string]any{
		"id": "run1", "triggerId": "7f0c", "functionName": "nightly-report", "collection": nil, "event": "schedule", "status": "ok", "attemptCount": 1,
		"errorDetail": nil, "errorOutput": nil, "recordId": nil, "triggeredAt": "t", "completedAt": "t2",
	}}, "meta": map[string]any{}}})
	runs, _, kerr := c.Schedules.Runs(ctx, "7f0c", ListScheduleRunsOptions{Limit: 5, Cursor: "n"})
	if kerr != nil || len(runs) != 1 || runs[0].AttemptCount != 1 || runs[0].TriggerID != "7f0c" || *runs[0].CompletedAt != "t2" {
		t.Fatalf("Runs: %+v %v", runs, kerr)
	}
	wantCall(t, "runs", calls[0], "GET", "/@portal/forge/schedules/7f0c/runs", map[string]string{"limit": "5", "cursor": "n"}, "")

	// RunNow: 202, body returned raw.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{202, map[string]any{"data": map[string]any{"runId": "run2", "queued": true}, "meta": map[string]any{}}})
	raw, _, kerr := c.Schedules.RunNow(ctx, "7f0c")
	if kerr != nil || raw["runId"] != "run2" || raw["queued"] != true {
		t.Fatalf("RunNow: %+v %v", raw, kerr)
	}
	wantCall(t, "run-now", calls[0], "POST", "/@portal/forge/schedules/7f0c/run-now", map[string]string{}, "")

	// Delete: 204.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{status: 204})
	if kerr := c.Schedules.Delete(ctx, "7f0c"); kerr != nil {
		t.Fatalf("Delete 204: %v", kerr)
	}
	wantCall(t, "delete", calls[0], "DELETE", "/@portal/forge/schedules/7f0c", map[string]string{}, "")
}

func TestSchedulesValidation(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{}, "meta": map[string]any{}}})
	for name, f := range map[string]func() *KError{
		"list limit 201": func() *KError { _, _, k := c.Schedules.List(ctx, ListSchedulesOptions{Limit: 201}); return k },
		"runs limit -1":  func() *KError { _, _, k := c.Schedules.Runs(ctx, "x", ListScheduleRunsOptions{Limit: -1}); return k },
		"get empty id":   func() *KError { _, _, k := c.Schedules.Get(ctx, ""); return k },
		"empty patch":    func() *KError { _, _, k := c.Schedules.Update(ctx, "x", UpdateScheduleInput{}); return k },
		"delete empty":   func() *KError { return c.Schedules.Delete(ctx, "") },
		"run-now empty":  func() *KError { _, _, k := c.Schedules.RunNow(ctx, ""); return k },
	} {
		wantInvalid(t, name, f(), calls)
	}
}

func TestEndUsersWire(t *testing.T) {
	ctx := context.Background()
	user := map[string]any{
		"id": "eu_YmGE3Z6nkv5i", "email": "user@example.com", "emailVerifiedAt": nil, "status": "active", "createdAt": "2026-10-09T12:15:13.430Z",
		"lastSigninAt": "2026-10-21T06:39:02.321Z", "signInMethods": []any{"password", "magic_link"}, "region": nil,
	}
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": []any{user}, "meta": map[string]any{"cursor": map[string]any{"next": "n2"}}}})
	users, meta, kerr := c.EndUsers.List(ctx, ListEndUsersOptions{Status: "all", Q: "us", Limit: 2, Cursor: "n1"})
	if kerr != nil || len(users) != 1 || users[0].ID != "eu_YmGE3Z6nkv5i" || users[0].EmailVerifiedAt != nil || *users[0].LastSigninAt != "2026-10-21T06:39:02.321Z" ||
		len(users[0].SignInMethods) != 2 || users[0].Region != nil || *meta.Cursor.Next != "n2" {
		t.Fatalf("List: %+v %v", users, kerr)
	}
	wantCall(t, "list", calls[0], "GET", "/@portal/end-users", map[string]string{"status": "all", "q": "us", "limit": "2", "cursor": "n1"}, "")
	calls = nil
	c.EndUsers.List(ctx, ListEndUsersOptions{})
	wantCall(t, "list default", calls[0], "GET", "/@portal/end-users", map[string]string{}, "")

	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": user, "meta": map[string]any{}}})
	u, _, kerr := c.EndUsers.Get(ctx, "eu_YmGE3Z6nkv5i")
	if kerr != nil || u.Email != "user@example.com" || u.Status != "active" {
		t.Fatalf("Get: %+v %v", u, kerr)
	}
	wantCall(t, "get", calls[0], "GET", "/@portal/end-users/eu_YmGE3Z6nkv5i", map[string]string{}, "")

	// Unknown end user: gateway 404.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{404, map[string]any{"code": "K9O_END_USER_NOT_FOUND", "message": "no such end user", "context": map[string]any{}}})
	if _, _, kerr := c.EndUsers.Get(ctx, "eu_nope"); kerr == nil || kerr.Code != "K9O_END_USER_NOT_FOUND" {
		t.Fatalf("404: %v", kerr)
	}
}

func TestRoleGrantsWire(t *testing.T) {
	ctx := context.Background()
	grant := map[string]any{"id": "eurg_AbCdEf123456", "endUserId": "eu_YmGE3Z6nkv5i", "role": "billing-manager", "grantedAt": "2026-10-26T17:59:56.815Z", "grantedBy": "usr_conformance0001"}
	var calls []streamsCall

	// List: live fixture {grants: []} unwraps to an empty slice.
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"grants": []any{}}, "meta": map[string]any{}}})
	grants, _, kerr := c.EndUsers.RoleGrants.List(ctx, ListRoleGrantsOptions{})
	if kerr != nil || len(grants) != 0 {
		t.Fatalf("List empty: %+v %v", grants, kerr)
	}
	wantCall(t, "list", calls[0], "GET", "/@portal/end-user-role-grants", map[string]string{}, "")
	calls = nil
	c.EndUsers.RoleGrants.List(ctx, ListRoleGrantsOptions{EndUserID: "eu_x"})
	wantCall(t, "list filtered", calls[0], "GET", "/@portal/end-user-role-grants", map[string]string{"endUserId": "eu_x"}, "")

	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"grants": []any{grant}}, "meta": map[string]any{}}})
	grants, _, kerr = c.EndUsers.RoleGrants.List(ctx, ListRoleGrantsOptions{EndUserID: "eu_YmGE3Z6nkv5i"})
	if kerr != nil || len(grants) != 1 || grants[0].Role != "billing-manager" || grants[0].GrantedBy != "usr_conformance0001" {
		t.Fatalf("List: %+v %v", grants, kerr)
	}

	// Grant unwraps data.grant.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"grant": grant}, "meta": map[string]any{}}})
	g, _, kerr := c.EndUsers.RoleGrants.Grant(ctx, GrantEndUserRoleInput{EndUserID: "eu_YmGE3Z6nkv5i", Role: "billing-manager"})
	if kerr != nil || g.ID != "eurg_AbCdEf123456" || g.EndUserID != "eu_YmGE3Z6nkv5i" || g.Role != "billing-manager" {
		t.Fatalf("Grant: %+v %v", g, kerr)
	}
	wantCall(t, "grant", calls[0], "POST", "/@portal/end-user-role-grants", map[string]string{}, `{"endUserId":"eu_YmGE3Z6nkv5i","role":"billing-manager"}`)

	// Revoke: revoked now, then unknown/already revoked is not an error.
	calls = nil
	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"revoked": true, "revokedAt": "2026-10-26T18:00:00.000Z"}, "meta": map[string]any{}}})
	r, _, kerr := c.EndUsers.RoleGrants.Revoke(ctx, "eurg_AbCdEf123456")
	if kerr != nil || !r.Revoked || r.RevokedAt == nil || *r.RevokedAt != "2026-10-26T18:00:00.000Z" {
		t.Fatalf("Revoke: %+v %v", r, kerr)
	}
	wantCall(t, "revoke", calls[0], "DELETE", "/@portal/end-user-role-grants/eurg_AbCdEf123456", map[string]string{}, "")

	c = workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"revoked": false, "revokedAt": nil}, "meta": map[string]any{}}})
	r, _, kerr = c.EndUsers.RoleGrants.Revoke(ctx, "eurg_unknown")
	if kerr != nil || r.Revoked || r.RevokedAt != nil {
		t.Fatalf("Revoke unknown: %+v %v", r, kerr)
	}
}

func TestEndUsersValidation(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{}, "meta": map[string]any{}}})
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	for name, f := range map[string]func() *KError{
		"list limit 0 above": func() *KError { _, _, k := c.EndUsers.List(ctx, ListEndUsersOptions{Limit: 300}); return k },
		"get empty":          func() *KError { _, _, k := c.EndUsers.Get(ctx, ""); return k },
		"grant no user": func() *KError {
			_, _, k := c.EndUsers.RoleGrants.Grant(ctx, GrantEndUserRoleInput{Role: "r"})
			return k
		},
		"grant no role": func() *KError {
			_, _, k := c.EndUsers.RoleGrants.Grant(ctx, GrantEndUserRoleInput{EndUserID: "eu_1"})
			return k
		},
		"grant long role": func() *KError {
			_, _, k := c.EndUsers.RoleGrants.Grant(ctx, GrantEndUserRoleInput{EndUserID: "eu_1", Role: string(long)})
			return k
		},
		"revoke empty": func() *KError { _, _, k := c.EndUsers.RoleGrants.Revoke(ctx, ""); return k },
	} {
		wantInvalid(t, name, f(), calls)
	}
}
