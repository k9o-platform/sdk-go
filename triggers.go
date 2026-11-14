package k9o

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
)

// FunctionTrigger is an event trigger: it runs the function when a record
// event happens on a collection.
type FunctionTrigger struct {
	// ID is a UUID.
	ID           string  `json:"id"`
	FunctionName string  `json:"functionName"`
	Collection   *string `json:"collection"`
	// Events lists "insert", "update", "delete" and "upsert".
	Events []string `json:"events"`
	// Filter is a predicate over the record, or nil to fire on every matching event.
	Filter           map[string]any `json:"filter"`
	Enabled          bool           `json:"enabled"`
	LogOkInvocations bool           `json:"logOkInvocations"`
	CreatedAt        string         `json:"createdAt"`
}

// CreateTriggerInput is the body of Functions.Triggers.Create.
type CreateTriggerInput struct {
	// Collection is an existing collection: ^[a-zA-Z_][a-zA-Z0-9_]*$, 1 to 128 characters.
	Collection string `json:"collection"`
	// Events is 1 to 4 of "insert", "update", "delete", "upsert".
	Events []string `json:"events"`
	// Filter fires the trigger only when the record matches; nil fires on every event.
	Filter map[string]any `json:"filter,omitempty"`
	// Enabled defaults to true when nil.
	Enabled *bool `json:"enabled,omitempty"`
	// LogOkInvocations records successful invocations too (default false: only failures are logged).
	LogOkInvocations bool `json:"logOkInvocations,omitempty"`
}

// UpdateTriggerInput holds the fields to change; send at least one. The
// collection cannot be changed.
type UpdateTriggerInput struct {
	// Events replaces the whole list (1 to 4 of "insert", "update", "delete", "upsert").
	Events []string
	// Filter replaces the filter.
	Filter map[string]any
	// ClearFilter removes the filter, so the trigger fires on every event. Ignored when Filter is set.
	ClearFilter      bool
	Enabled          *bool
	LogOkInvocations *bool
}

func (u UpdateTriggerInput) wire() map[string]any {
	body := map[string]any{}
	if u.Events != nil {
		body["events"] = u.Events
	}
	if u.Filter != nil {
		body["filter"] = u.Filter
	} else if u.ClearFilter {
		body["filter"] = nil
	}
	if u.Enabled != nil {
		body["enabled"] = *u.Enabled
	}
	if u.LogOkInvocations != nil {
		body["logOkInvocations"] = *u.LogOkInvocations
	}
	return body
}

// MarshalJSON sends only the fields that are set; ClearFilter sends "filter": null.
func (u UpdateTriggerInput) MarshalJSON() ([]byte, error) { return json.Marshal(u.wire()) }

var (
	triggerCollectionRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	triggerEvents       = map[string]bool{"insert": true, "update": true, "delete": true, "upsert": true}
)

func checkTriggerEvents(events []string) *KError {
	if len(events) < 1 || len(events) > 4 {
		return invalidField("events must list 1 to 4 of insert, update, delete, upsert", map[string]any{"events": events})
	}
	for _, e := range events {
		if !triggerEvents[e] {
			return invalidField("events must list 1 to 4 of insert, update, delete, upsert", map[string]any{"events": events})
		}
	}
	return nil
}

// FunctionTriggersNamespace exposes client.Functions.Triggers.*: run a function
// when a record is inserted, updated, deleted or upserted in a collection.
// Needs the project admin role. For triggers on a timetable see client.Schedules.
type FunctionTriggersNamespace struct {
	config *httpClientConfig
}

func (n *FunctionTriggersNamespace) path(name, tail string) (string, *KError) {
	if kerr := requireID(name, "function name"); kerr != nil {
		return "", kerr
	}
	slug, kerr := requireProjectSlug(n.config, "functions")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "functions/"+url.PathEscape(name)+"/triggers"+tail), nil
}

// List returns every trigger of the function, newest first: GET
// /@<slug>/functions/<name>/triggers. It is not paginated.
func (n *FunctionTriggersNamespace) List(ctx context.Context, name string) ([]FunctionTrigger, *Meta, *KError) {
	p, kerr := n.path(name, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[[]FunctionTrigger](ctx, n.config, &requestInit{Method: "GET", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Create creates an event trigger: POST /@<slug>/functions/<name>/triggers
// (answers 201). The SDK checks Collection and Events before sending.
func (n *FunctionTriggersNamespace) Create(ctx context.Context, name string, input CreateTriggerInput) (*FunctionTrigger, *Meta, *KError) {
	if len(input.Collection) > 128 || !triggerCollectionRE.MatchString(input.Collection) {
		return nil, nil, invalidField("collection must match ^[a-zA-Z_][a-zA-Z0-9_]*$ (1 to 128 characters)", map[string]any{"collection": input.Collection})
	}
	if kerr := checkTriggerEvents(input.Events); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path(name, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[FunctionTrigger](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Update changes a trigger: PATCH /@<slug>/functions/<name>/triggers/<id>.
// Send at least one of Events, Filter (or ClearFilter), Enabled, LogOkInvocations.
func (n *FunctionTriggersNamespace) Update(ctx context.Context, name, triggerID string, patch UpdateTriggerInput) (*FunctionTrigger, *Meta, *KError) {
	if kerr := requireID(triggerID, "trigger"); kerr != nil {
		return nil, nil, kerr
	}
	if len(patch.wire()) == 0 {
		return nil, nil, invalidField("patch needs at least one of: events, filter, enabled, logOkInvocations", nil)
	}
	if patch.Events != nil {
		if kerr := checkTriggerEvents(patch.Events); kerr != nil {
			return nil, nil, kerr
		}
	}
	p, kerr := n.path(name, "/"+url.PathEscape(triggerID))
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[FunctionTrigger](ctx, n.config, &requestInit{Method: "PATCH", Path: p, Body: patch})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Delete deletes a trigger: DELETE /@<slug>/functions/<name>/triggers/<id>. The
// gateway answers 204 with no body, so there is no result; deleting the same
// trigger again is a 404 K9O_TRIGGER_NOT_FOUND.
func (n *FunctionTriggersNamespace) Delete(ctx context.Context, name, triggerID string) *KError {
	if kerr := requireID(triggerID, "trigger"); kerr != nil {
		return kerr
	}
	p, kerr := n.path(name, "/"+url.PathEscape(triggerID))
	if kerr != nil {
		return kerr
	}
	_, kerr = doRequest[any](ctx, n.config, &requestInit{Method: "DELETE", Path: p})
	return kerr
}
