package k9o

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditEvent is an audit event record.
type AuditEvent struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	ActorID    *string        `json:"actorId,omitempty"`
	TargetKind *string        `json:"targetKind,omitempty"`
	TargetID   *string        `json:"targetId,omitempty"`
	Payload    map[string]any `json:"payload"`
	OccurredAt string         `json:"occurredAt"`
	RequestID  *string        `json:"requestId,omitempty"`
	TraceID    *string        `json:"traceId,omitempty"`
}

// AuditQueryOpts are optional filters for AuditNamespace.Query; names follow
// the gateway's query parameters.
type AuditQueryOpts struct {
	// EventType filters by audit-event name (e.g. "data.write.access.insert").
	EventType string
	// ActorID filters by actor.
	ActorID string
	// After is an ISO 8601 lower bound: only events after this instant.
	After string
	// Before is an ISO 8601 upper bound: only events before this instant.
	Before string
	// Limit is the page size (the per-tier maximum is bounded server-side).
	Limit int
	// Cursor is the cursor from a prior response.
	Cursor string
}

func (o AuditQueryOpts) toQuery() map[string]string {
	q := map[string]string{}
	if o.EventType != "" {
		q["eventType"] = o.EventType
	}
	if o.ActorID != "" {
		q["actorId"] = o.ActorID
	}
	if o.After != "" {
		q["after"] = o.After
	}
	if o.Before != "" {
		q["before"] = o.Before
	}
	if o.Limit > 0 {
		q["limit"] = strconv.Itoa(o.Limit)
	}
	if o.Cursor != "" {
		q["cursor"] = o.Cursor
	}
	return q
}

// AuditNamespace exposes client.Audit.* (project-plane).
type AuditNamespace struct {
	config *httpClientConfig
}

// Query queries the audit log (reverse-chronological; filters AND-compose).
func (n *AuditNamespace) Query(ctx context.Context, opts AuditQueryOpts) ([]AuditEvent, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "audit")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[[]AuditEvent](ctx, n.config, &requestInit{Method: "GET", Path: ProjectPath(slug, "audit-log"), Query: opts.toQuery()})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// AuditTailEvent is one event of the project's live audit tail. It carries
// five audit fields only (read the full event from Audit.Query), plus the
// EventID of the frame it came in on.
type AuditTailEvent struct {
	// AuditEventID identifies the audit event (evt_...). Pass it to
	// AuditReplayOptions.AfterAuditEventID to continue after this event.
	AuditEventID string `json:"auditEventId"`
	// Type is the audit event type, for example "data.write.access.insert".
	Type string `json:"type"`
	// ActorType is the kind of actor behind the event.
	ActorType string `json:"actorType"`
	// OccurredAt is an ISO 8601 timestamp.
	OccurredAt string `json:"occurredAt"`
	// Outcome is the result of the audited action; empty when the gateway
	// sends null.
	Outcome string `json:"outcome"`
	// EventID is the feed's own id for the frame (the publisher's id). It is
	// not the AuditEventID. Empty on events read from Audit.Replay.
	EventID string `json:"-"`
}

// auditPayload is the object under "payload" in a tail frame and a replay row.
type auditPayload struct {
	AuditEventID string  `json:"auditEventId"`
	Type         string  `json:"type"`
	ActorType    string  `json:"actorType"`
	OccurredAt   string  `json:"occurredAt"`
	Outcome      *string `json:"outcome"`
}

func (p auditPayload) event() AuditTailEvent {
	ev := AuditTailEvent{AuditEventID: p.AuditEventID, Type: p.Type, ActorType: p.ActorType, OccurredAt: p.OccurredAt}
	if p.Outcome != nil {
		ev.Outcome = *p.Outcome
	}
	return ev
}

// auditWireFrame is the data object of a tail frame:
// {"topic","eventType","payload":{...},"occurredAt","eventId"}.
type auditWireFrame struct {
	Topic      string       `json:"topic"`
	EventType  string       `json:"eventType"`
	Payload    auditPayload `json:"payload"`
	OccurredAt string       `json:"occurredAt"`
	EventID    string       `json:"eventId"`
}

// AuditReplayEvent is an audit event read from the audit log by Audit.Replay:
// the same five fields as AuditTailEvent, flattened from the row's payload,
// plus its position in the log.
type AuditReplayEvent struct {
	AuditTailEvent
	// Sequence is the event's position in the log. The gateway sends it as a
	// string (a 64-bit integer); it is kept as received.
	Sequence string `json:"sequence"`
	// Topic is the topic the event was published on, for example
	// "audit.project.data.write.pulse_stream.created".
	Topic string `json:"topic"`
}

// UnmarshalJSON reads a replay row {"sequence","topic","eventType","payload":{...},"occurredAt"}
// and flattens the payload. A number is accepted for sequence.
func (e *AuditReplayEvent) UnmarshalJSON(data []byte) error {
	var row struct {
		Sequence   json.RawMessage `json:"sequence"`
		Topic      string          `json:"topic"`
		EventType  string          `json:"eventType"`
		Payload    auditPayload    `json:"payload"`
		OccurredAt string          `json:"occurredAt"`
	}
	if err := json.Unmarshal(data, &row); err != nil {
		return err
	}
	ev := row.Payload.event()
	if ev.Type == "" {
		ev.Type = row.EventType
	}
	if ev.OccurredAt == "" {
		ev.OccurredAt = row.OccurredAt
	}
	e.AuditTailEvent = ev
	e.Topic = row.Topic
	e.Sequence = ""
	if len(row.Sequence) > 0 && string(row.Sequence) != "null" {
		var str string
		if json.Unmarshal(row.Sequence, &str) == nil {
			e.Sequence = str
		} else {
			e.Sequence = string(row.Sequence)
		}
	}
	return nil
}

// AuditReplayPage is the answer to Audit.Replay.
type AuditReplayPage struct {
	// Topic is the topic that was read.
	Topic string
	// Events are in log order, flattened like tail events.
	Events []AuditReplayEvent
	// HasMore is true when the log holds more events after this page.
	HasMore bool
	// NextCursor is the gateway's cursor for the next page, as received (nil
	// when it sends none). The request has no cursor parameter: for the next
	// page pass the last event's AuditEventID as AfterAuditEventID.
	NextCursor any
}

// AuditTailOptions configures Audit.Tail.
type AuditTailOptions struct {
	// Types limits the tail to these audit event types. One type subscribes to
	// audit.project.<type>; none subscribes to audit.project.*; several
	// subscribe to audit.project.* and drop other types in the SDK (an exact
	// match on Type).
	Types []string

	// OnResync is called when the gateway reports that the position was lost
	// (action "replay-ledger"). The SDK then catches up from the audit log on
	// its own (Audit.Replay after the last event it saw) before it continues,
	// so the callback is informational. Optional.
	OnResync func(ctx context.Context, info ResyncInfo)

	idleTimeout time.Duration
	retryDelay  time.Duration
}

// AuditReplayOptions configures Audit.Replay.
type AuditReplayOptions struct {
	// Topic is the audit topic: "audit.project.*" (the default) or "audit.project.<type>".
	Topic string
	// AfterAuditEventID returns the events after this audit event (evt_...).
	// It wins over FromSequence. An id that is not in the log answers 400
	// K9O_INVALID_CURSOR.
	AfterAuditEventID string
	// FromSequence returns the events from this position of the log.
	FromSequence *int64
	// UntilSequence stops at this position.
	UntilSequence *int64
	// Limit is the page size, 1 to 10000. Zero (not set) sends 1000; a
	// negative value or one above 10000 is refused with K9O_INVALID_FIELD_VALUE.
	Limit int
}

const auditTailTopic = "audit.project."

const (
	auditReplayDefaultLimit = 1000
	auditReplayMaxLimit     = 10000
)

// auditTailState is the delivery state of one Tail. mu guards everything; the
// feed goroutine (live frames) and the catch-up goroutine both use it.
type auditTailState struct {
	mu        sync.Mutex
	runMu     sync.Mutex // one catch-up at a time
	want      map[string]bool
	delivered *feedDedupe // audit ids already handed out
	// last is the AuditEventID of the newest event read, delivered or dropped
	// by the type filter; a catch-up starts after it.
	last string
	// pending counts catch-ups that have not finished; while it is above zero
	// live frames wait in buffer.
	pending int
	buffer  []AuditTailEvent
}

// emitLocked hands one event out once. mu must be held.
func (s *auditTailState) emitLocked(ev AuditTailEvent, emit func(AuditTailEvent) bool) bool {
	s.last = ev.AuditEventID
	if s.want != nil && !s.want[ev.Type] {
		return true
	}
	if !s.delivered.add(ev.AuditEventID) {
		return true
	}
	return emit(ev)
}

// deliver takes a live frame: held back during a catch-up, handed out otherwise.
func (s *auditTailState) deliver(ev AuditTailEvent, emit func(AuditTailEvent) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending > 0 {
		s.buffer = append(s.buffer, ev)
		return true
	}
	return s.emitLocked(ev, emit)
}

// startCatchUp is called once the fresh connection is open (frames already
// wait in the buffer). It reads the audit log after the last event delivered,
// page by page, hands those events out, then drains the buffer. It does not
// block. With no event delivered yet there is nothing to catch up from and the
// live stream (which starts at the live head) goes on as it is.
func (s *auditTailState) startCatchUp(ctx context.Context, n *AuditNamespace, topic string, emit func(AuditTailEvent) bool, fail func(*KError)) {
	s.mu.Lock()
	if s.last == "" {
		s.mu.Unlock()
		return
	}
	s.pending++
	s.mu.Unlock()
	go func() {
		s.runMu.Lock()
		defer s.runMu.Unlock()
		kerr := s.replayAfterLast(ctx, n, topic, emit)
		s.mu.Lock()
		s.pending--
		if s.pending == 0 {
			buf := s.buffer
			s.buffer = nil
			for _, ev := range buf {
				if !s.emitLocked(ev, emit) {
					break
				}
			}
		}
		s.mu.Unlock()
		if kerr != nil {
			fail(kerr)
		}
	}()
}

func (s *auditTailState) replayAfterLast(ctx context.Context, n *AuditNamespace, topic string, emit func(AuditTailEvent) bool) *KError {
	s.mu.Lock()
	after := s.last
	s.mu.Unlock()
	for {
		page, kerr := n.Replay(ctx, AuditReplayOptions{Topic: topic, AfterAuditEventID: after})
		if kerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return kerr
		}
		if len(page.Events) == 0 || page.Events[len(page.Events)-1].AuditEventID == after {
			return nil
		}
		for _, ev := range page.Events {
			after = ev.AuditEventID
			s.mu.Lock()
			ok := s.emitLocked(ev.AuditTailEvent, emit)
			s.mu.Unlock()
			if !ok {
				return nil
			}
		}
		if !page.HasMore {
			return nil
		}
	}
}

// Tail follows the project's audit events live (GET /@<slug>/realtime/subscribe
// with topic audit.project.*). It is the change-feed reader of
// Realtime.Subscribe: it reconnects with Last-Event-ID (the frame's SSE id),
// drops events it already delivered (by the frame's eventId), ignores
// heartbeats and replaces a connection that goes quiet. Each event holds
// AuditEventID, Type, ActorType, OccurredAt and Outcome, flattened from the
// frame's payload; read the full row with Audit.Query.
//
// When the position can no longer be resumed the gateway sends a resync frame
// with action "replay-ledger". The SDK then reconnects without a position
// and holds the frames that arrive, reads the audit log after the
// AuditEventID of the last event it delivered (Audit.Replay, page by page),
// delivers what it finds, then the held frames, then the live ones, each
// audit event once. The tail excludes
// warden.checkpoint events; the replay includes them (they are delivered only
// on that path, and are dropped when Types is set to others).
//
// Who may tail: ORG_OWNER, ORG_ADMIN, PROJECT_ADMIN and PROJECT_AUDITOR,
// re-checked every 60 seconds; extension tokens cannot. Cancel ctx to stop;
// both channels close.
func (n *AuditNamespace) Tail(ctx context.Context, opts AuditTailOptions) (<-chan AuditTailEvent, <-chan *KError) {
	fail := func(kerr *KError) (<-chan AuditTailEvent, <-chan *KError) {
		events := make(chan AuditTailEvent)
		errs := make(chan *KError, 1)
		errs <- kerr
		close(errs)
		close(events)
		return events, errs
	}
	slug, kerr := requireProjectSlug(n.config, "audit")
	if kerr != nil {
		return fail(kerr)
	}
	topic := auditTailTopic + "*"
	var want map[string]bool
	for _, t := range opts.Types {
		if strings.TrimSpace(t) == "" || strings.ContainsAny(t, " \t\r\n") {
			return fail(invalidField("Types must be audit event types without spaces", map[string]any{"field": "types"}))
		}
	}
	switch len(opts.Types) {
	case 0:
	case 1:
		topic = auditTailTopic + opts.Types[0]
	default:
		want = map[string]bool{}
		for _, t := range opts.Types {
			want[t] = true
		}
	}

	st := &auditTailState{delivered: newFeedDedupe(), want: want}
	hooks := feedHooks[AuditTailEvent]{
		decode: func(name, sseID, payload string) (AuditTailEvent, string, bool) {
			var f auditWireFrame
			if json.Unmarshal([]byte(payload), &f) != nil {
				return AuditTailEvent{}, "", false
			}
			ev := f.Payload.event()
			if ev.Type == "" {
				ev.Type = f.EventType
			}
			if ev.Type == "" {
				ev.Type = name
			}
			if ev.OccurredAt == "" {
				ev.OccurredAt = f.OccurredAt
			}
			ev.EventID = f.EventID
			if ev.EventID == "" {
				ev.EventID = sseID
			}
			if ev.AuditEventID == "" || ev.Type == "" {
				return ev, "", false
			}
			return ev, ev.EventID, true
		},
		deliver: st.deliver,
		catchUp: func(ctx context.Context, emit func(AuditTailEvent) bool, fail func(*KError)) {
			st.startCatchUp(ctx, n, topic, emit, fail)
		},
	}
	so := SubscribeOptions{OnResync: opts.OnResync, idleTimeout: opts.idleTimeout, retryDelay: opts.retryDelay}
	return streamFeedOf(ctx, n.config, &requestInit{
		Method: "GET",
		Path:   ProjectPath(slug, "realtime/subscribe"),
		Query:  map[string]string{"topic": topic},
	}, so, hooks)
}

// Replay reads audit events from the audit log in order (POST
// /@<slug>/realtime/replay), warden.checkpoint events included. Pass
// AfterAuditEventID (the last AuditEventID you received) or FromSequence.
// It needs the same access as Audit.Query. At most 10000 events come back per
// call; while HasMore is true, call again with the last AuditEventID as
// AfterAuditEventID.
func (n *AuditNamespace) Replay(ctx context.Context, opts AuditReplayOptions) (*AuditReplayPage, *KError) {
	slug, kerr := requireProjectSlug(n.config, "audit")
	if kerr != nil {
		return nil, kerr
	}
	topic := opts.Topic
	if topic == "" {
		topic = auditTailTopic + "*"
	}
	if opts.Limit < 0 || opts.Limit > auditReplayMaxLimit {
		return nil, invalidField("Limit must be 1 to 10000", map[string]any{"field": "limit"})
	}
	limit := opts.Limit
	if limit == 0 {
		limit = auditReplayDefaultLimit
	}
	if opts.FromSequence != nil && *opts.FromSequence < 0 || opts.UntilSequence != nil && *opts.UntilSequence < 0 {
		return nil, invalidField("sequences must be 0 or more", nil)
	}
	body := map[string]any{"topic": topic}
	if opts.AfterAuditEventID != "" {
		body["afterAuditEventId"] = opts.AfterAuditEventID
	}
	if opts.FromSequence != nil {
		body["fromSequence"] = *opts.FromSequence
	}
	if opts.UntilSequence != nil {
		body["untilSequence"] = *opts.UntilSequence
	}
	body["limit"] = limit
	env, kerr := doRequest[json.RawMessage](ctx, n.config, &requestInit{Method: "POST", Path: ProjectPath(slug, "realtime/replay"), Body: body})
	if kerr != nil {
		return nil, kerr
	}
	page, ok := decodeReplayPage(env.Data)
	if !ok {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: "malformed replay answer: expected {topic, events, nextCursor, hasMore}", Status: 200}
	}
	return page, nil
}

// decodeReplayPage reads the replay answer {"topic","events":[...],"nextCursor","hasMore"}.
func decodeReplayPage(raw json.RawMessage) (*AuditReplayPage, bool) {
	page := &AuditReplayPage{Events: []AuditReplayEvent{}}
	if len(raw) == 0 || string(raw) == "null" {
		return page, true
	}
	var obj struct {
		Topic      string             `json:"topic"`
		Events     []AuditReplayEvent `json:"events"`
		NextCursor any                `json:"nextCursor"`
		HasMore    bool               `json:"hasMore"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	page.Topic, page.NextCursor, page.HasMore = obj.Topic, obj.NextCursor, obj.HasMore
	if obj.Events != nil {
		page.Events = obj.Events
	}
	return page, true
}
