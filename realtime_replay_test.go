package k9o

import (
	"context"
	"testing"
)

func TestRealtimeReplayWire(t *testing.T) {
	ctx := context.Background()
	row := map[string]any{"sequence": "7", "topic": "audit.project.a.b", "eventType": "a.b", "payload": map[string]any{"auditEventId": "evt_7"}, "occurredAt": "2026-10-27T10:00:01.000Z"}
	c, s := dkClient(t, 200, dkOK(map[string]any{"topic": "audit.project.*", "events": []any{row}, "nextCursor": "7", "hasMore": true}))
	from, until := int64(5), int64(9)
	page, kerr := c.Realtime.Replay(ctx, "audit.project.*", RealtimeReplayOptions{FromSequence: &from, UntilSequence: &until, Limit: 50, AfterAuditEventID: "evt_6"})
	if kerr != nil || len(page.Events) != 1 || !page.HasMore || page.NextCursor != "7" || page.Topic != "audit.project.*" || page.Resync != "" {
		t.Fatalf("replay: %+v %v", page, kerr)
	}
	e := page.Events[0]
	if e.Sequence != "7" || e.Topic != "audit.project.a.b" || e.EventType != "a.b" || e.OccurredAt != "2026-10-27T10:00:01.000Z" || string(e.Payload) != `{"auditEventId":"evt_7"}` {
		t.Fatalf("event: %+v", e)
	}
	wantReq(t, s, "POST", "/@portal/realtime/replay", "")
	b := s.json(t)
	if b["topic"] != "audit.project.*" || b["fromSequence"] != float64(5) || b["untilSequence"] != float64(9) || b["limit"] != float64(50) || b["afterAuditEventId"] != "evt_6" {
		t.Fatalf("body: %v", b)
	}

	// only the topic is sent by default; no history answers an empty page with resync
	c, s = dkClient(t, 200, dkOK(map[string]any{"topic": "collections.tasks.*", "events": []any{}, "nextCursor": nil, "hasMore": false, "resync": "reread-source"}))
	page, kerr = c.Realtime.Replay(ctx, "collections.tasks.*", RealtimeReplayOptions{})
	if kerr != nil || page.Events == nil || len(page.Events) != 0 || page.Resync != "reread-source" || page.NextCursor != nil {
		t.Fatalf("empty: %+v %v", page, kerr)
	}
	b = s.json(t)
	if len(b) != 1 || b["topic"] != "collections.tasks.*" {
		t.Fatalf("minimal body: %v", b)
	}

	// a numeric sequence is kept as received
	c, _ = dkClient(t, 200, dkOK(map[string]any{"topic": "t", "events": []any{map[string]any{"sequence": 12, "topic": "t", "eventType": "x", "payload": nil, "occurredAt": "z"}}, "hasMore": false}))
	if page, kerr = c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{}); kerr != nil || page.Events[0].Sequence != "12" {
		t.Fatalf("numeric sequence: %+v %v", page, kerr)
	}
}

func TestRealtimeReplayValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	c, _ := dkClient(t, 200, dkOK(map[string]any{"events": []any{}}))
	neg := int64(-1)
	for name, call := range map[string]func() *KError{
		"empty topic": func() *KError { _, k := c.Realtime.Replay(ctx, "", RealtimeReplayOptions{}); return k },
		"long topic": func() *KError {
			_, k := c.Realtime.Replay(ctx, string(make([]byte, 201)), RealtimeReplayOptions{})
			return k
		},
		"limit high": func() *KError { _, k := c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{Limit: 10001}); return k },
		"limit neg":  func() *KError { _, k := c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{Limit: -1}); return k },
		"negative seq": func() *KError {
			_, k := c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{FromSequence: &neg})
			return k
		},
	} {
		if k := call(); k == nil || k.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Fatalf("%s: %v", name, k)
		}
	}
	if _, k := c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{Limit: 10000}); k != nil {
		t.Fatalf("limit 10000: %v", k)
	}
	c, _ = dkClient(t, 403, map[string]any{"code": "K9O_RBAC_DENY", "message": "audit access required"})
	if _, k := c.Realtime.Replay(ctx, "audit.project.*", RealtimeReplayOptions{}); k == nil || k.Code != "K9O_RBAC_DENY" || k.Status != 403 {
		t.Fatalf("403: %v", k)
	}
	c, _ = dkClient(t, 200, dkOK("nope"))
	if _, k := c.Realtime.Replay(ctx, "t", RealtimeReplayOptions{}); k == nil {
		t.Fatal("malformed answer must fail")
	}
	nc, _ := NewClient(ClientOpts{BaseURL: "https://acme.api.k9o.io"})
	if _, k := nc.Realtime.Replay(ctx, "t", RealtimeReplayOptions{}); k == nil || k.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("no project: %v", k)
	}
}
