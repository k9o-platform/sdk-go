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
	"time"
)

var keyJSON = map[string]any{
	"id": "vkey_0f2c", "version": 3, "status": "active", "custodyMode": "BYOK",
	"createdAt": "2026-09-01T10:00:00Z", "rotatedAt": "2026-10-01T10:00:00Z",
	"byokRef": map[string]any{"provider": "aws-kms", "region": "eu-central-1", "externalKeyFingerprint": "Zm9vYmFyYmF6cXV4"},
	"hyokRef": nil,
}

func TestKeysListGetFields(t *testing.T) {
	ctx := context.Background()
	hyok := map[string]any{"id": "vkey_aa", "version": 1, "status": "revoked", "custodyMode": "HYOK", "createdAt": "2026-01-01T00:00:00Z", "rotatedAt": nil,
		"byokRef": nil, "hyokRef": map[string]any{"clusterId": "hsm_1", "clusterName": "berlin-hsm", "externalKeyFingerprint": "YWJjZGVmZ2hpamts"}}
	pm := map[string]any{"id": "vkey_bb", "version": nil, "status": nil, "custodyMode": nil, "createdAt": nil, "rotatedAt": nil, "byokRef": nil, "hyokRef": nil}
	c, s := dkClient(t, 200, dkOK(map[string]any{"items": []any{keyJSON, hyok, pm}, "nextCursor": nil}))
	list, meta, kerr := c.Keys.List(ctx, KeysListOptions{All: true})
	if kerr != nil || meta == nil {
		t.Fatal(kerr)
	}
	wantReq(t, s, "GET", "/@portal/vault/keys", "status=all")
	if len(list.Items) != 3 {
		t.Fatalf("list: %+v", list)
	}
	k := list.Items[0]
	if k.ID != "vkey_0f2c" || *k.Version != 3 || *k.Status != "active" || *k.CustodyMode != "BYOK" || *k.CreatedAt != "2026-09-01T10:00:00Z" || *k.RotatedAt != "2026-10-01T10:00:00Z" ||
		*k.ByokRef.Provider != "aws-kms" || *k.ByokRef.Region != "eu-central-1" || *k.ByokRef.ExternalKeyFingerprint != "Zm9vYmFyYmF6cXV4" || k.HyokRef != nil {
		t.Fatalf("byok key: %+v", k)
	}
	h := list.Items[1]
	if *h.HyokRef.ClusterID != "hsm_1" || *h.HyokRef.ClusterName != "berlin-hsm" || *h.HyokRef.ExternalKeyFingerprint != "YWJjZGVmZ2hpamts" || h.ByokRef != nil || h.RotatedAt != nil {
		t.Fatalf("hyok key: %+v", h)
	}
	n := list.Items[2]
	if n.Version != nil || n.Status != nil || n.CustodyMode != nil || n.CreatedAt != nil {
		t.Fatalf("nullable fields must stay nil: %+v", n)
	}

	// default lists the active key only: no query
	c, s = dkClient(t, 200, dkOK(map[string]any{"items": nil, "nextCursor": nil}))
	list, _, kerr = c.Keys.List(ctx, KeysListOptions{})
	if kerr != nil || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("empty: %+v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/vault/keys", "")

	c, s = dkClient(t, 200, dkOK(keyJSON))
	got, _, kerr := c.Keys.Get(ctx, "vkey_0f2c")
	if kerr != nil || got.ID != "vkey_0f2c" || *got.ByokRef.Provider != "aws-kms" {
		t.Fatalf("get: %+v %v", got, kerr)
	}
	wantReq(t, s, "GET", "/@portal/vault/keys/vkey_0f2c", "")
	if _, _, kerr = c.Keys.Get(ctx, ""); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Fatalf("empty id: %v", kerr)
	}

	// a key never carries key material: the SDK type has no such field
	raw, _ := json.Marshal(got)
	for _, banned := range []string{"material", "secret", "arn", "handle"} {
		if strings.Contains(strings.ToLower(string(raw)), banned) {
			t.Errorf("Key serialises %q", banned)
		}
	}
}

func TestKeysListToleratesNextCursor(t *testing.T) {
	c, s := dkClient(t, 200, dkOK(map[string]any{"items": []any{keyJSON}, "nextCursor": "cur_9"}))
	list, _, kerr := c.Keys.List(context.Background(), KeysListOptions{})
	if kerr != nil || len(list.Items) != 1 {
		t.Fatalf("%+v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/vault/keys", "")
	if raw, _ := json.Marshal(list); strings.Contains(string(raw), "ursor") {
		t.Fatalf("KeyList must expose no cursor: %s", raw)
	}
}

func TestKeysReadRefusalsAndNotFound(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{403, "K9O_TRUST_ZONE_VIOLATION"}, {404, "K9O_NOT_FOUND"}, {502, "K9O_UPSTREAM_UNAVAILABLE"}} {
		c, _ := dkClient(t, tc.status, map[string]any{"code": tc.code, "message": "refused"})
		c.config.Retry = &RetryConfig{MaxRetries: 0}
		_, _, kerr := c.Keys.Get(context.Background(), "vkey_x")
		if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status {
			t.Errorf("%d: %v", tc.status, kerr)
		}
	}
}

func TestKeysRotate(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK(map[string]any{"keyId": "vkey_0f2c", "previousVersion": 3, "newVersion": 4, "status": "rotated"}))
	rot, _, kerr := c.Keys.Rotate(ctx, "vkey_0f2c", KeysRotateOptions{})
	if kerr != nil || rot.KeyID != "vkey_0f2c" || *rot.PreviousVersion != 3 || *rot.NewVersion != 4 || *rot.Status != "rotated" {
		t.Fatalf("rotate: %+v %v", rot, kerr)
	}
	wantReq(t, s, "POST", "/@portal/vault/keys/vkey_0f2c/rotate", "")
	if _, _, kerr = c.Keys.Rotate(ctx, "", KeysRotateOptions{}); kerr == nil {
		t.Fatal("empty id must be refused")
	}
}

var stepUpCtx = map[string]any{
	"scopeSetRoot": "ssr_1", "nonce": "n_1", "minTier": "T3_KEY_POSSESSION",
	"methods": []any{"webauthn", "dpop", "mtls"}, "maxAgeSec": 120, "singleUse": true, "rpId": "k9o.example",
}

func TestKeysRotateErrors(t *testing.T) {
	ctx := context.Background()
	calls := 0
	mk := func(status int, hdr http.Header, body map[string]any) *Client {
		return rampartTestClient(t, func(*http.Request) *http.Response {
			calls++
			r := jsonResponse(status, body)
			for k, v := range hdr {
				r.Header[k] = v
			}
			return r
		})
	}

	// step-up: the typed error, the challenge in Context, never retried
	c := mk(401, nil, map[string]any{"code": "K9O_STEP_UP_REQUIRED", "message": "step-up required", "context": map[string]any{"stepUp": stepUpCtx}})
	_, _, kerr := c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{})
	if kerr == nil || kerr.Code != "K9O_STEP_UP_REQUIRED" || kerr.Status != 401 || kerr.StepUp == nil || kerr.StepUp.Nonce != "n_1" || kerr.StepUp.ScopeSetRoot != "ssr_1" {
		t.Fatalf("step-up: %v", kerr)
	}

	// 409 with both reasons
	for _, reason := range []string{"rotation_in_flight", "key_not_active"} {
		c = mk(409, nil, map[string]any{"code": "K9O_STATE_CONFLICT", "message": "conflict", "context": map[string]any{"reason": reason}})
		if _, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{}); kerr == nil || kerr.Code != "K9O_STATE_CONFLICT" || kerr.Status != 409 || kerr.Context["reason"] != reason {
			t.Fatalf("409 %s: %v", reason, kerr)
		}
	}

	// 429 with the wait in the body
	c = mk(429, nil, map[string]any{"code": "K9O_RATE_LIMITED", "message": "3 rotations in 24 h", "context": map[string]any{"reason": "rotation_rate_cap"},
		"recommendation": map[string]any{"retryAfterSec": 3600, "note": "wait"}})
	_, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{})
	if kerr == nil || kerr.Code != "K9O_RATE_LIMITED" || kerr.Status != 429 || kerr.Context["reason"] != "rotation_rate_cap" || kerr.RetryAfterSec == nil || *kerr.RetryAfterSec != 3600 {
		t.Fatalf("429 body: %v", kerr)
	}
	// 429 with the Retry-After header only
	c = mk(429, http.Header{"Retry-After": {"7200"}}, map[string]any{"code": "K9O_RATE_LIMITED", "message": "3 rotations in 24 h", "context": map[string]any{"reason": "rotation_rate_cap"}})
	_, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{})
	if kerr == nil || kerr.RetryAfterSec == nil || *kerr.RetryAfterSec != 7200 {
		t.Fatalf("429 header: %v", kerr)
	}
	// the body wins over the header
	c = mk(429, http.Header{"Retry-After": {"7200"}}, map[string]any{"code": "K9O_RATE_LIMITED", "message": "x", "recommendation": map[string]any{"retryAfterSec": 5}})
	if _, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{}); kerr == nil || *kerr.RetryAfterSec != 5 {
		t.Fatalf("429 precedence: %v", kerr)
	}
	// an HTTP date is not read
	c = mk(429, http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, map[string]any{"code": "K9O_RATE_LIMITED", "message": "x"})
	if _, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{}); kerr == nil || kerr.RetryAfterSec != nil {
		t.Fatalf("http-date: %v", kerr)
	}

	// 5xx on a POST is not retried: one call per Rotate above
	calls = 0
	c = mk(502, nil, map[string]any{"code": "K9O_UPSTREAM_UNAVAILABLE", "message": "key service down"})
	if _, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{}); kerr == nil || kerr.Code != "K9O_UPSTREAM_UNAVAILABLE" || calls != 1 {
		t.Fatalf("502: %v calls=%d", kerr, calls)
	}
	c = mk(500, nil, map[string]any{"code": "K9O_INTERNAL_ERROR", "message": "audit record failed", "context": map[string]any{"reason": "vault_audit_write_failed"}})
	if _, _, kerr = c.Keys.Rotate(ctx, "vkey_1", KeysRotateOptions{}); kerr == nil || kerr.Context["reason"] != "vault_audit_write_failed" {
		t.Fatalf("500: %v", kerr)
	}
}

// ---- audit tail ----

// auditFrame is a tail frame in the gateway's real shape. The SSE id and the
// frame's eventId are the publisher's ("pub_" + id); the audit id is inside
// payload and is a different value.
func auditFrame(id, typ string) string {
	pub := "pub_" + id
	return fmt.Sprintf("id: %s\nevent: %s\ndata: {\"topic\":\"audit.project.%s\",\"eventType\":\"%s\",\"payload\":{\"auditEventId\":\"%s\",\"type\":\"%s\",\"actorType\":\"user\",\"occurredAt\":\"2026-10-27T10:00:00.000Z\",\"outcome\":\"success\"},\"occurredAt\":\"2026-10-27T10:00:00.000Z\",\"eventId\":\"%s\"}\n\n", pub, typ, typ, typ, id, typ, pub)
}

func recvAudit(t *testing.T, events <-chan AuditTailEvent, errs <-chan *KError) AuditTailEvent {
	t.Helper()
	select {
	case ev, okc := <-events:
		if !okc {
			t.Fatal("events closed")
		}
		return ev
	case kerr := <-errs:
		t.Fatalf("error: %v", kerr)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
	return AuditTailEvent{}
}

func TestAuditTailTopicsAndFields(t *testing.T) {
	for _, tc := range []struct {
		types []string
		topic string
	}{{nil, "audit.project.*"}, {[]string{"data.write.access.insert"}, "audit.project.data.write.access.insert"}, {[]string{"a.b", "c.d"}, "audit.project.*"}} {
		srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/@portal/realtime/subscribe" {
				t.Errorf("path: %s", r.URL.Path)
			}
			if got := r.URL.Query().Get("topic"); got != tc.topic {
				t.Errorf("topic %q, want %q", got, tc.topic)
			}
			sendSSE(w, ": hb\n\n"+auditFrame("evt_1", "a.b")+auditFrame("evt_2", "x.y"))
			<-r.Context().Done()
		})
		ctx, cancel := context.WithCancel(context.Background())
		events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{Types: tc.types})
		ev := recvAudit(t, events, errs)
		if ev != (AuditTailEvent{AuditEventID: "evt_1", Type: "a.b", ActorType: "user", OccurredAt: "2026-10-27T10:00:00.000Z", Outcome: "success", EventID: "pub_evt_1"}) {
			t.Fatalf("event: %+v", ev)
		}
		if len(tc.types) == 1 || len(tc.types) == 0 {
			if ev2 := recvAudit(t, events, errs); ev2.AuditEventID != "evt_2" {
				t.Fatalf("second: %+v", ev2)
			}
		} else {
			// several types: the SDK drops others on its own
			select {
			case ev2 := <-events:
				t.Fatalf("x.y must be filtered out: %+v", ev2)
			case <-time.After(150 * time.Millisecond):
			}
		}
		cancel()
	}
}

func TestAuditTailDedupeAndResume(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, auditFrame("evt_1", "a.b")+auditFrame("evt_2", "a.b")+auditFrame("evt_2", "a.b"))
		default:
			sendSSE(w, auditFrame("evt_2", "a.b")+auditFrame("evt_3", "a.b"))
			<-r.Context().Done()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second})
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, recvAudit(t, events, errs).AuditEventID)
	}
	if strings.Join(got, ",") != "evt_1,evt_2,evt_3" {
		t.Fatalf("ids: %v", got)
	}
	last, _ := srv.conns()
	if last[0] != "" || last[1] != "pub_evt_2" {
		t.Fatalf("Last-Event-ID must be the SSE id, not the audit id: %v", last)
	}
}

func TestAuditTailIdleTimeoutReconnects(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			sendSSE(w, auditFrame("evt_1", "a.b"))
			<-r.Context().Done() // goes quiet
			return
		}
		sendSSE(w, auditFrame("evt_2", "a.b"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{idleTimeout: 150 * time.Millisecond, retryDelay: 5 * time.Millisecond})
	recvAudit(t, events, errs)
	if ev := recvAudit(t, events, errs); ev.AuditEventID != "evt_2" {
		t.Fatalf("after idle: %+v", ev)
	}
	if last, _ := srv.conns(); len(last) < 2 || last[1] != "pub_evt_1" {
		t.Fatalf("Last-Event-ID after idle: %v", last)
	}
}

func TestAuditTailHeartbeatKeepsConnection(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			sendSSE(w, ":hb\n\n")
			time.Sleep(60 * time.Millisecond)
		}
		sendSSE(w, auditFrame("evt_1", "a.b"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{idleTimeout: 200 * time.Millisecond})
	recvAudit(t, events, errs)
	if last, _ := srv.conns(); len(last) != 1 {
		t.Fatalf("heartbeats must keep the first connection: %v", last)
	}
}

func TestAuditTailResyncReplaysLedgerThenContinues(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, auditFrame("evt_1", "a.b")+auditFrame("evt_2", "a.b"))
			// evt_2 is the last event read; the position is then lost
			sendSSE(w, "event: resync\ndata: {\"reason\":\"cursor-unknown\",\"action\":\"replay-ledger\"}\n\n")
		default:
			sendSSE(w, auditFrame("evt_5", "a.b")+auditFrame("evt_4", "a.b"))
			<-r.Context().Done()
		}
	})
	page := 0
	srv.replay = func(body map[string]any) any {
		page++
		switch page {
		case 1: // evt_2 -> evt_3, a checkpoint; more to come
			return replayPage([]any{replayEv("evt_3", "a.b", 3), replayEv("evt_c", "warden.checkpoint", 4)}, true)
		case 2: // after the last event of page 1
			return replayPage([]any{replayEv("evt_4", "a.b", 5)}, false)
		default:
			return replayPage([]any{}, false)
		}
	}
	var resyncInfo ResyncInfo
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{
		retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second,
		OnResync: func(_ context.Context, info ResyncInfo) { resyncInfo = info },
	})
	var got []string
	for i := 0; i < 6; i++ {
		got = append(got, recvAudit(t, events, errs).AuditEventID)
	}
	// 1, 2 live; 3, the checkpoint and 4 from the ledger (two pages); then
	// evt_5 live. evt_4 repeated by the live stream is dropped.
	if strings.Join(got, ",") != "evt_1,evt_2,evt_3,evt_c,evt_4,evt_5" {
		t.Fatalf("ids: %v", got)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected extra event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	if resyncInfo.Action != "replay-ledger" || resyncInfo.Reason != "cursor-unknown" {
		t.Fatalf("OnResync: %+v", resyncInfo)
	}
	srv.mu.Lock()
	posts := append([]map[string]any(nil), srv.posts...)
	srv.mu.Unlock()
	if len(posts) != 2 || posts[0]["topic"] != "audit.project.*" || posts[0]["afterAuditEventId"] != "evt_2" || posts[1]["afterAuditEventId"] != "evt_c" {
		t.Fatalf("replay requests (the second pages after the last event of the first): %v", posts)
	}
	// after a resync the new connection is fresh: no Last-Event-ID
	if last, _ := srv.conns(); len(last) != 2 || last[1] != "" {
		t.Fatalf("Last-Event-ID after resync: %v", last)
	}
	if posts[0]["limit"] != float64(1000) {
		t.Fatalf("catch-up page limit: %v", posts[0]["limit"])
	}
}

// A fresh connection that is itself answered with a resync, again and again,
// must back off and keep trying, never spin; the tail recovers when the
// gateway lets the stream through.
func TestAuditTailRepeatedResyncBacksOff(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		switch {
		case n == 0:
			sendSSE(w, auditFrame("evt_1", "a.b"))
			sendSSE(w, "event: resync\ndata: {\"action\":\"replay-ledger\"}\n\n")
		case n < 5:
			sendSSE(w, "event: resync\ndata: {\"action\":\"replay-ledger\"}\n\n")
		default:
			sendSSE(w, auditFrame("evt_2", "a.b"))
			<-r.Context().Done()
		}
	})
	srv.replay = func(map[string]any) any { return replayPage([]any{}, false) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 20 * time.Millisecond, idleTimeout: 2 * time.Second})
	recvAudit(t, events, errs)
	if ev := recvAudit(t, events, errs); ev.AuditEventID != "evt_2" {
		t.Fatalf("%+v", ev)
	}
	if last, _ := srv.conns(); len(last) != 6 {
		t.Fatalf("connections: %v", last)
	} else {
		for i, id := range last[1:] {
			if id != "" {
				t.Fatalf("connection %d must be fresh, Last-Event-ID %q", i+1, id)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// connections 1..5 follow a resync: from the second one on, each gap is
	// at least as long as the one before and the last is clearly longer
	// than the base delay
	gap := func(i int) time.Duration { return times[i].Sub(times[i-1]) }
	if gap(5) < 60*time.Millisecond || gap(5) < gap(3) {
		t.Fatalf("no back-off: %v %v %v %v %v", gap(1), gap(2), gap(3), gap(4), gap(5))
	}
}

// Event E is published after the replay's last event but before the fresh
// stream delivers its first frame: the connection was opened first, so the
// backlog carries E and it is delivered exactly once.
func TestAuditTailResyncGaplessEventBetweenReplayAndStream(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, auditFrame("evt_1", "a.b")+"event: resync\ndata: {\"action\":\"replay-ledger\"}\n\n")
		default:
			// backlog of the fresh stream: evt_2 (also in the replay) and E
			sendSSE(w, auditFrame("evt_2", "a.b")+auditFrame("evt_E", "a.b")+auditFrame("evt_E", "a.b"))
			<-r.Context().Done()
		}
	})
	srv.replay = func(map[string]any) any { return replayPage([]any{replayEv("evt_2", "a.b", 2)}, false) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second})
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, recvAudit(t, events, errs).AuditEventID)
	}
	if strings.Join(got, ",") != "evt_1,evt_2,evt_E" {
		t.Fatalf("ids: %v", got)
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// Frames that arrive while the replay runs are held and handed out after the
// replay's events, deduplicated.
func TestAuditTailBufferedFramesFollowReplayEvents(t *testing.T) {
	release := make(chan struct{})
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, auditFrame("evt_1", "a.b")+"event: resync\ndata: {\"action\":\"replay-ledger\"}\n\n")
		default:
			sendSSE(w, auditFrame("evt_3", "a.b")+auditFrame("evt_9", "a.b")+auditFrame("evt_2", "a.b"))
			<-r.Context().Done()
		}
	})
	srv.replay = func(map[string]any) any {
		<-release // the replay answers only after the frames have arrived
		return replayPage([]any{replayEv("evt_2", "a.b", 2), replayEv("evt_3", "a.b", 3)}, false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second})
	if ev := recvAudit(t, events, errs); ev.AuditEventID != "evt_1" {
		t.Fatalf("%+v", ev)
	}
	select {
	case ev := <-events:
		t.Fatalf("a buffered frame was yielded before the replay finished: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, recvAudit(t, events, errs).AuditEventID)
	}
	if strings.Join(got, ",") != "evt_2,evt_3,evt_9" {
		t.Fatalf("ids: %v", got)
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// Captured on staging: a fresh connection opens with an informational head
// resync, and live frames follow on the same stream. The tail keeps reading
// that stream: no reconnect, no OnResync, no replay.
func TestAuditTailFreshHeadResyncIsInformational(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sendSSE(w, "retry: 1000\n:ok\n\nevent: resync\ndata: {\"reason\":\"cursor-evicted\",\"head\":75,\"action\":\"replay-ledger\"}\n\n")
		sendSSE(w, rawTailFrame[len("retry: 1000\n:ok\n\n"):])
		<-r.Context().Done()
	})
	srv.replay = func(map[string]any) any { t.Error("no replay for a head resync"); return replayPage([]any{}, false) }
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second,
		OnResync: func(context.Context, ResyncInfo) { called = true }})
	if ev := recvAudit(t, events, errs); ev.AuditEventID != "evt_UNs3pW7TSadU" {
		t.Fatalf("%+v", ev)
	}
	time.Sleep(100 * time.Millisecond)
	if last, _ := srv.conns(); len(last) != 1 || called {
		t.Fatalf("connections %v, OnResync called %v", last, called)
	}
}

func bodyOf(s *dkSeen) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(s.body, &m)
	return m
}

func TestAuditReplayLimit(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK(replayPage([]any{}, false)))
	if _, kerr := c.Audit.Replay(ctx, AuditReplayOptions{}); kerr != nil {
		t.Fatal(kerr)
	}
	if got := bodyOf(s)["limit"]; got != float64(1000) {
		t.Fatalf("default limit on the wire: %v", got)
	}
	if _, kerr := c.Audit.Replay(ctx, AuditReplayOptions{Limit: 10000}); kerr != nil {
		t.Fatal(kerr)
	}
	if got := bodyOf(s)["limit"]; got != float64(10000) {
		t.Fatalf("explicit limit: %v", got)
	}
	for _, l := range []int{10001, -1} {
		if _, kerr := c.Audit.Replay(ctx, AuditReplayOptions{Limit: l}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("limit %d: %v", l, kerr)
		}
	}
}

// The frames below were captured from the live gateway.
const (
	rawTailFrame = "retry: 1000\n:ok\n\n" +
		"id: evt_TqfKnZdBRtggfnwJvuvroU\n" +
		"event: data.write.forge.scheduled_trigger.created\n" +
		"data: {\"topic\":\"audit.project.data.write.forge.scheduled_trigger.created\",\"eventType\":\"data.write.forge.scheduled_trigger.created\",\"payload\":{\"auditEventId\":\"evt_UNs3pW7TSadU\",\"type\":\"data.write.forge.scheduled_trigger.created\",\"actorType\":\"user\",\"occurredAt\":\"2026-10-27T19:31:40.636Z\",\"outcome\":null},\"occurredAt\":\"2026-10-27T19:31:40.636Z\",\"eventId\":\"evt_TqfKnZdBRtggfnwJvuvroU\"}\n\n"
)

func TestAuditTailRealWireFrame(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			// the same frame twice: the second is a duplicate
			sendSSE(w, rawTailFrame+rawTailFrame[len("retry: 1000\n:ok\n\n"):])
			return
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second})
	ev := recvAudit(t, events, errs)
	want := AuditTailEvent{
		AuditEventID: "evt_UNs3pW7TSadU", Type: "data.write.forge.scheduled_trigger.created", ActorType: "user",
		OccurredAt: "2026-10-27T19:31:40.636Z", Outcome: "", EventID: "evt_TqfKnZdBRtggfnwJvuvroU",
	}
	if ev != want {
		t.Fatalf("event: %+v", ev)
	}
	select {
	case ev := <-events:
		t.Fatalf("duplicate must be dropped: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
	// resume (the frame says "retry: 1000"): Last-Event-ID is the SSE id,
	// which is not the audit event id
	time.Sleep(1200 * time.Millisecond)
	if last, _ := srv.conns(); len(last) < 2 || last[1] != "evt_TqfKnZdBRtggfnwJvuvroU" {
		t.Fatalf("Last-Event-ID: %v", last)
	}
}

func TestAuditTailResyncWithoutPositionSkipsReplay(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			sendSSE(w, "event: resync\ndata: {\"reason\":\"cursor-unknown\",\"action\":\"replay-ledger\"}\n\n")
			return
		}
		sendSSE(w, auditFrame("evt_1", "a.b"))
		<-r.Context().Done()
	})
	srv.replay = func(map[string]any) any { t.Error("no replay without a position"); return replayPage([]any{}, false) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Audit.Tail(ctx, AuditTailOptions{retryDelay: 5 * time.Millisecond})
	if ev := recvAudit(t, events, errs); ev.AuditEventID != "evt_1" {
		t.Fatalf("%+v", ev)
	}
}

func TestAuditTailResyncReplayFailureEndsWithError(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sendSSE(w, auditFrame("evt_1", "a.b")+"event: resync\ndata: {\"action\":\"replay-ledger\"}\n\n")
	})
	inner := srv.srv.Config.Handler
	srv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { // the replay answers 400 K9O_INVALID_CURSOR
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"code": "K9O_INVALID_CURSOR", "message": "unknown audit event id"})
			return
		}
		inner.ServeHTTP(w, r)
	})
	events, errs := srv.client(t).Audit.Tail(context.Background(), AuditTailOptions{retryDelay: 5 * time.Millisecond})
	recvAudit(t, events, errs)
	select {
	case kerr := <-errs:
		if kerr == nil || kerr.Code != "K9O_INVALID_CURSOR" || kerr.Status != 400 {
			t.Fatalf("%v", kerr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
}

func TestAuditTailRefusals(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(map[string]any{"code": "K9O_RBAC_DENY", "message": "audit access required"})
	})
	events, errs := srv.client(t).Audit.Tail(context.Background(), AuditTailOptions{})
	if kerr := <-errs; kerr == nil || kerr.Code != "K9O_RBAC_DENY" || kerr.Status != 403 {
		t.Fatalf("403: %v", kerr)
	}
	if _, open := <-events; open {
		t.Fatal("events must close")
	}
	if conns, _ := srv.conns(); len(conns) != 1 {
		t.Fatalf("a 4xx ends the tail without retry: %d connections", len(conns))
	}
	_, errs = srv.client(t).Audit.Tail(context.Background(), AuditTailOptions{Types: []string{"a b"}})
	if kerr := <-errs; kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Fatalf("bad type: %v", kerr)
	}
	nc, _ := NewClient(ClientOpts{BaseURL: "https://acme.api.k9o.io"})
	_, errs = nc.Audit.Tail(context.Background(), AuditTailOptions{})
	if kerr := <-errs; kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("no project: %v", kerr)
	}
}

func replayEv(id, typ string, seq int) map[string]any {
	return map[string]any{
		"sequence": fmt.Sprint(seq), "topic": "audit.project." + typ, "eventType": typ,
		"payload":    map[string]any{"auditEventId": id, "type": typ, "actorType": "user", "occurredAt": "2026-10-27T10:00:01.000Z", "outcome": "success"},
		"occurredAt": "2026-10-27T10:00:01.000Z",
	}
}

func replayPage(events []any, hasMore bool) map[string]any {
	return map[string]any{"topic": "audit.project.*", "events": events, "nextCursor": nil, "hasMore": hasMore}
}

func TestAuditReplayWire(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK(replayPage([]any{replayEv("evt_3", "a.b", 3)}, true)))
	page, kerr := c.Audit.Replay(ctx, AuditReplayOptions{AfterAuditEventID: "evt_2", Topic: "audit.project.a.b", Limit: 50})
	if kerr != nil || len(page.Events) != 1 || !page.HasMore || page.NextCursor != nil || page.Topic != "audit.project.*" {
		t.Fatalf("replay: %+v %v", page, kerr)
	}
	e := page.Events[0]
	if e.AuditEventID != "evt_3" || e.Sequence != "3" || e.Type != "a.b" || e.ActorType != "user" || e.Outcome != "success" || e.OccurredAt != "2026-10-27T10:00:01.000Z" || e.Topic != "audit.project.a.b" {
		t.Fatalf("event: %+v", e)
	}
	wantReq(t, s, "POST", "/@portal/realtime/replay", "")
	if b := s.json(t); b["topic"] != "audit.project.a.b" || b["afterAuditEventId"] != "evt_2" || b["limit"] != float64(50) {
		t.Fatalf("body: %v", b)
	}

	// from/until sequence; an empty page is a non-nil empty list
	c, s = dkClient(t, 200, dkOK(replayPage(nil, false)))
	seq, until := int64(0), int64(20)
	page, kerr = c.Audit.Replay(ctx, AuditReplayOptions{FromSequence: &seq, UntilSequence: &until})
	if kerr != nil || page.Events == nil || len(page.Events) != 0 || page.HasMore {
		t.Fatalf("empty: %+v %v", page, kerr)
	}
	if b := s.json(t); b["topic"] != "audit.project.*" || b["fromSequence"] != float64(0) || b["untilSequence"] != float64(20) {
		t.Fatalf("body: %v", b)
	}
	if _, has := s.json(t)["afterAuditEventId"]; has {
		t.Fatal("afterAuditEventId must be omitted")
	}
	// afterAuditEventId wins over fromSequence: both are sent, the gateway decides
	if _, kerr = c.Audit.Replay(ctx, AuditReplayOptions{AfterAuditEventID: "evt_1", FromSequence: &seq}); kerr != nil {
		t.Fatalf("both: %v", kerr)
	}
	if _, kerr = c.Audit.Replay(ctx, AuditReplayOptions{Limit: 10001}); kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Fatal("limit over 10000 must be refused")
	}
	c, _ = dkClient(t, 400, map[string]any{"code": "K9O_INVALID_CURSOR", "message": "not in the log"})
	if _, kerr = c.Audit.Replay(ctx, AuditReplayOptions{AfterAuditEventID: "evt_zz"}); kerr == nil || kerr.Code != "K9O_INVALID_CURSOR" || kerr.Status != 400 {
		t.Fatalf("invalid cursor: %v", kerr)
	}
	c, _ = dkClient(t, 200, dkOK("nope"))
	if _, kerr = c.Audit.Replay(ctx, AuditReplayOptions{}); kerr == nil {
		t.Fatal("malformed answer must fail")
	}
}

// A captured replay answer (trimmed to two rows): sequence is a string, the
// five fields sit inside payload, outcome is null.
func TestAuditReplayRealWireAnswer(t *testing.T) {
	raw := `{"data":{"topic":"audit.project.*","events":[` +
		`{"sequence":"1","topic":"audit.project.data.write.pulse_stream.created","eventType":"data.write.pulse_stream.created","payload":{"auditEventId":"evt_UGgF77wit3Xx","type":"data.write.pulse_stream.created","actorType":"user","occurredAt":"2026-10-27T19:20:00.000Z","outcome":null},"occurredAt":"2026-10-27T19:20:00.000Z"},` +
		`{"sequence":"2","topic":"audit.project.warden.checkpoint","eventType":"warden.checkpoint","payload":{"auditEventId":"evt_Wc1","type":"warden.checkpoint","actorType":"system","occurredAt":"2026-10-27T19:21:00.000Z","outcome":null},"occurredAt":"2026-10-27T19:21:00.000Z"}],` +
		`"nextCursor":null,"hasMore":true},"meta":{}}`
	c := rampartTestClient(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw)), Header: http.Header{"Content-Type": {"application/json"}}}
	})
	page, kerr := c.Audit.Replay(context.Background(), AuditReplayOptions{FromSequence: new(int64), Limit: 3})
	if kerr != nil || len(page.Events) != 2 || !page.HasMore {
		t.Fatalf("%+v %v", page, kerr)
	}
	e := page.Events[0]
	if e.Sequence != "1" || e.AuditEventID != "evt_UGgF77wit3Xx" || e.Type != "data.write.pulse_stream.created" || e.ActorType != "user" || e.Outcome != "" || e.OccurredAt != "2026-10-27T19:20:00.000Z" {
		t.Fatalf("event: %+v", e)
	}
	if page.Events[1].Type != "warden.checkpoint" || page.Events[1].Sequence != "2" {
		t.Fatalf("checkpoint: %+v", page.Events[1])
	}
}

// ---- rampart: windows and 413 ----

func TestVerifyReceiptWindowsClaim(t *testing.T) {
	s := newSigner(t, "k1")
	input := map[string]any{"text": "a long document"}
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request"); return nil })

	spec := goodSpec(t, s, input)
	spec.claims["windows"] = 3
	spec.claims["some_future_claim"] = map[string]any{"x": []any{1, "y"}}
	claims, err := c.Rampart.VerifyReceipt(context.Background(), s.sign(spec), input, verifyOpts(s))
	if err != nil || claims.Windows != 3 {
		t.Fatalf("windows: %+v %v", claims, err)
	}

	spec = goodSpec(t, s, input)
	spec.claims["windows"] = 1
	if claims, err = c.Rampart.VerifyReceipt(context.Background(), s.sign(spec), input, verifyOpts(s)); err != nil || claims.Windows != 1 {
		t.Fatalf("one window: %+v %v", claims, err)
	}

	// a receipt without the claim still verifies and reads as one window
	claims, err = c.Rampart.VerifyReceipt(context.Background(), s.sign(goodSpec(t, s, input)), input, verifyOpts(s))
	if err != nil || claims.Windows != 1 {
		t.Fatalf("no windows claim: %+v %v", claims, err)
	}

	for _, bad := range []any{"3", 2.5, 0, -1, true} {
		spec = goodSpec(t, s, input)
		spec.claims["windows"] = bad
		claims, err = c.Rampart.VerifyReceipt(context.Background(), s.sign(spec), input, verifyOpts(s))
		wantReason(t, claims, err, ReceiptMalformed)
	}
}

func TestRampartClassifyInputTooLongIsTypedError(t *testing.T) {
	calls := 0
	c := rampartTestClient(t, func(*http.Request) *http.Response {
		calls++
		return jsonResponse(413, map[string]any{
			"code": "K9O_RAMPART_INPUT_TOO_LONG", "message": "the input needs more than 16 windows",
			"context": map[string]any{"reason": "rampart_input_too_long"},
		})
	})
	res, meta, kerr := c.Rampart.Classify(context.Background(), map[string]any{"text": strings.Repeat("word ", 9000)}, ClassifyOptions{})
	if res != nil || meta != nil || kerr == nil {
		t.Fatalf("no verdict expected: %v %v %v", res, meta, kerr)
	}
	if kerr.Code != CodeRampartInputTooLong || kerr.Status != 413 || kerr.Context["reason"] != "rampart_input_too_long" {
		t.Fatalf("kerr: %+v", kerr)
	}
	if calls != 1 {
		t.Fatalf("a 413 is not retried: %d calls", calls)
	}
	if CodeRampartInputTooLong != "K9O_RAMPART_INPUT_TOO_LONG" || CodeRampartClassifierUnavailable != "K9O_RAMPART_CLASSIFIER_UNAVAILABLE" {
		t.Fatal("constant")
	}
}
