package k9o

import (
	"context"
	"encoding/json"
	"time"
)

// RealtimeEvent is a single event delivered on a subscription.
type RealtimeEvent struct {
	Topic      string          `json:"topic"`
	EventType  string          `json:"eventType"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurredAt"`
	// EventID identifies the event (evt_...). Delivery is at-least-once, so
	// de-duplicate by EventID if you also store events.
	EventID string `json:"eventId"`
	// Sequence is the event's position in the log. It is set on events that
	// Replay returns (a string, as the gateway sends it; a number is accepted)
	// and empty on events from Subscribe.
	Sequence string `json:"-"`
}

// UnmarshalJSON reads a frame or a replay row. A replay row also carries
// "sequence", which is kept as received.
func (e *RealtimeEvent) UnmarshalJSON(data []byte) error {
	var row struct {
		Topic      string          `json:"topic"`
		EventType  string          `json:"eventType"`
		Payload    json.RawMessage `json:"payload"`
		OccurredAt string          `json:"occurredAt"`
		EventID    string          `json:"eventId"`
		Sequence   json.RawMessage `json:"sequence"`
	}
	if err := json.Unmarshal(data, &row); err != nil {
		return err
	}
	*e = RealtimeEvent{Topic: row.Topic, EventType: row.EventType, Payload: row.Payload, OccurredAt: row.OccurredAt, EventID: row.EventID}
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

// ResyncInfo describes a resync control frame: the feed position was lost.
type ResyncInfo struct {
	Reason string `json:"reason"` // for example "cursor-unknown"
	Action string `json:"action"` // for example "reread-source"
}

// SubscribeOptions configures Subscribe.
type SubscribeOptions struct {
	// Filter narrows the subscription (it composes with RLS: the filter narrows,
	// RLS gates). Sent as the JSON `filter` query parameter.
	Filter map[string]any

	// OnResync is called when the gateway reports that the feed position was
	// lost. Re-read current state through the data API inside it; the
	// subscription then continues on a fresh connection that carries no
	// Last-Event-ID. The callback runs on the subscription goroutine, so events
	// resume after it returns. Optional.
	OnResync func(ctx context.Context, info ResyncInfo)

	initialQuery map[string]string // extra query sent only until the feed has a position

	idleTimeout time.Duration // no bytes for this long: reconnect (default 45s)
	retryDelay  time.Duration // default reconnect delay (default 1s)
}

// realtimeTopics provides typed builders for the 6 closed topic families.
type realtimeTopics struct{}

// RealtimeTopic is the package-level entry to the topic builders.
var RealtimeTopic = realtimeTopics{}

func (realtimeTopics) Collection(collection string) string { return "collections." + collection + ".*" }
func (realtimeTopics) WorkflowRuns() string                { return "studio.workflows.runs.*" }
func (realtimeTopics) QueryExecuted(queryID string) string {
	return "studio.queries.executed." + queryID
}
func (realtimeTopics) DashboardRefresh(dashboardID string) string {
	return "studio.dashboards.refresh." + dashboardID
}
func (realtimeTopics) FunctionInvocations(functionID string) string {
	return "forge.functions.invocations." + functionID
}
func (realtimeTopics) AuditOrg(eventFamily string) string { return "audit.org." + eventFamily }

// RealtimeNamespace exposes client.Realtime.*.
type RealtimeNamespace struct {
	config *httpClientConfig
}

// Subscribe subscribes to a topic over SSE (GET /@<slug>/realtime/subscribe),
// fanning RealtimeEvents onto the returned channel. Errors arrive on the errs
// channel. Cancel via ctx; both channels close on termination.
//
// The subscription resumes by itself: when the stream ends (the gateway closes
// it after at most 300 seconds) or the connection drops, it reconnects with
// the Last-Event-ID of the last event delivered, drops events it already
// delivered, and treats 45 seconds without bytes as a dead connection. A 4xx
// answer is sent once on errs and ends the subscription; 5xx answers and
// network errors are retried with a capped backoff. After a resync frame the
// OnResync callback runs and the subscription continues without a position.
func (n *RealtimeNamespace) Subscribe(ctx context.Context, topic string, opts SubscribeOptions) (<-chan RealtimeEvent, <-chan *KError) {
	slug, kerr := requireProjectSlug(n.config, "realtime")
	if kerr != nil {
		events := make(chan RealtimeEvent)
		errs := make(chan *KError, 1)
		errs <- kerr
		close(errs)
		close(events)
		return events, errs
	}

	q := map[string]string{"topic": topic}
	if opts.Filter != nil {
		if b, err := json.Marshal(opts.Filter); err == nil {
			q["filter"] = string(b)
		}
	}
	return streamFeed(ctx, n.config, &requestInit{
		Method: "GET",
		Path:   ProjectPath(slug, "realtime/subscribe"),
		Query:  q,
	}, opts)
}

// RealtimeReplayOptions configures Replay.
type RealtimeReplayOptions struct {
	// FromSequence returns the events from this position of the log.
	FromSequence *int64
	// UntilSequence stops at this position.
	UntilSequence *int64
	// Limit is the page size, 1 to 10000. Zero (not set) leaves it to the
	// gateway; a negative value or one above 10000 is refused with
	// K9O_INVALID_FIELD_VALUE before sending.
	Limit int
	// AfterAuditEventID continues after this audit event (evt_...). It applies
	// to "audit.project.*" topics only.
	AfterAuditEventID string
}

// RealtimeReplayPage is the answer to Replay.
type RealtimeReplayPage struct {
	Topic string
	// Events are the replayed events in order; never nil.
	Events []RealtimeEvent
	// NextCursor is kept as received (nil when there is none).
	NextCursor any
	// HasMore is true when another call returns more events.
	HasMore bool
	// Resync is "reread-source" when the topic has no replayable history: the
	// gateway answers an empty page and you re-read current state through the
	// data API. Empty otherwise.
	Resync string
}

const realtimeReplayMaxLimit = 10000

// Replay reads events of a topic in order (POST /@<slug>/realtime/replay),
// the catch-up for Subscribe. topic is required. At most 10000 events come
// back per call; while HasMore is true, call again with FromSequence set past
// the last event's Sequence. A topic without durable history answers an empty
// page with Resync "reread-source". "audit.project.*" topics need the same
// access as Audit.Query.
func (n *RealtimeNamespace) Replay(ctx context.Context, topic string, opts RealtimeReplayOptions) (*RealtimeReplayPage, *KError) {
	slug, kerr := requireProjectSlug(n.config, "realtime")
	if kerr != nil {
		return nil, kerr
	}
	if topic == "" || len(topic) > 200 {
		return nil, invalidField("topic must be 1 to 200 characters", map[string]any{"field": "topic"})
	}
	if opts.Limit < 0 || opts.Limit > realtimeReplayMaxLimit {
		return nil, invalidField("Limit must be 1 to 10000", map[string]any{"field": "limit"})
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
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	env, kerr := doRequest[json.RawMessage](ctx, n.config, &requestInit{Method: "POST", Path: ProjectPath(slug, "realtime/replay"), Body: body})
	if kerr != nil {
		return nil, kerr
	}
	page := &RealtimeReplayPage{Events: []RealtimeEvent{}}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return page, nil
	}
	var obj struct {
		Topic      string          `json:"topic"`
		Events     []RealtimeEvent `json:"events"`
		NextCursor any             `json:"nextCursor"`
		HasMore    bool            `json:"hasMore"`
		Resync     string          `json:"resync"`
	}
	if err := json.Unmarshal(env.Data, &obj); err != nil {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: "malformed replay answer: expected {topic, events, nextCursor, hasMore}", Status: 200}
	}
	page.Topic, page.NextCursor, page.HasMore, page.Resync = obj.Topic, obj.NextCursor, obj.HasMore, obj.Resync
	if obj.Events != nil {
		page.Events = obj.Events
	}
	return page, nil
}
