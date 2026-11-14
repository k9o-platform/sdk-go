package k9o

import (
	"context"
	"net/url"
	"strconv"
)

// Schedule is a scheduled trigger: it runs a function on a cron timetable.
type Schedule struct {
	// ID is a UUID.
	ID           string `json:"id"`
	FunctionName string `json:"functionName"`
	// ScheduleExpression is a five-field cron. An alias such as "@daily" is stored and returned expanded.
	ScheduleExpression string `json:"scheduleExpression"`
	ScheduleTimezone   string `json:"scheduleTimezone"`
	// CatchupPolicy is "skip", "run_once" or "run_all".
	CatchupPolicy string  `json:"catchupPolicy"`
	Collection    *string `json:"collection"`
	Enabled       bool    `json:"enabled"`
	// Status is "active" when Enabled, otherwise "paused".
	Status           string  `json:"status"`
	LogOkInvocations bool    `json:"logOkInvocations"`
	CreatedAt        string  `json:"createdAt"`
	LastInvocationAt *string `json:"lastInvocationAt"`
	NextInvocationAt *string `json:"nextInvocationAt"`
}

// ScheduleRun is one run of a schedule.
type ScheduleRun struct {
	ID           string  `json:"id"`
	TriggerID    string  `json:"triggerId"`
	FunctionName string  `json:"functionName"`
	Collection   *string `json:"collection"`
	Event        string  `json:"event"`
	Status       string  `json:"status"`
	AttemptCount int     `json:"attemptCount"`
	ErrorDetail  *string `json:"errorDetail"`
	ErrorOutput  *string `json:"errorOutput"`
	RecordID     *string `json:"recordId"`
	TriggeredAt  string  `json:"triggeredAt"`
	CompletedAt  *string `json:"completedAt"`
}

// ListSchedulesOptions filters Schedules.List. A zero Limit uses the gateway
// default (50).
type ListSchedulesOptions struct {
	// Status is "active" or "paused".
	Status string
	// FunctionName limits the list to one function's schedules (sent as function_name).
	FunctionName string
	// Limit is 1 to 200.
	Limit int
	// Cursor is Meta.Cursor.Next of the previous page.
	Cursor string
}

// ListScheduleRunsOptions pages Schedules.Runs.
type ListScheduleRunsOptions struct {
	// Limit is 1 to 200.
	Limit  int
	Cursor string
}

// CreateScheduleInput is the body of Schedules.Create.
type CreateScheduleInput struct {
	// FunctionName is an existing function that is not retired.
	FunctionName string `json:"functionName"`
	// ScheduleExpression is a five-field cron (minute 0-59, hour 0-23, day 1-31,
	// month 1-12, weekday 0-6) or one of @yearly, @annually, @monthly, @weekly,
	// @daily, @midnight, @hourly.
	ScheduleExpression string `json:"scheduleExpression"`
	// ScheduleTimezone is "UTC" (default) or "Region/City".
	ScheduleTimezone string `json:"scheduleTimezone,omitempty"`
	// CatchupPolicy is "skip" (default), "run_once" or "run_all". "run_all"
	// is available on every plan.
	CatchupPolicy string `json:"catchupPolicy,omitempty"`
	// Collection is an existing collection, passed to the function with each run.
	Collection string `json:"collection,omitempty"`
	// Enabled defaults to true when nil.
	Enabled *bool `json:"enabled,omitempty"`
	// LogOkInvocations records successful runs too (default false).
	LogOkInvocations bool `json:"logOkInvocations,omitempty"`
}

// UpdateScheduleInput holds the fields to change; send at least one. The
// function cannot be changed.
type UpdateScheduleInput struct {
	ScheduleExpression string `json:"scheduleExpression,omitempty"`
	ScheduleTimezone   string `json:"scheduleTimezone,omitempty"`
	CatchupPolicy      string `json:"catchupPolicy,omitempty"`
	// Status "paused" stops the schedule and "active" resumes it.
	Status           string `json:"status,omitempty"`
	Enabled          *bool  `json:"enabled,omitempty"`
	LogOkInvocations *bool  `json:"logOkInvocations,omitempty"`
}

// SchedulesNamespace exposes client.Schedules.*: scheduled triggers over
// /@<slug>/forge/schedules. A schedule interval under 5 minutes is rejected with 400 K9O_INVALID_FIELD_VALUE (field schedule_expression), on every plan.
type SchedulesNamespace struct {
	config *httpClientConfig
}

func (n *SchedulesNamespace) path(tail string) (string, *KError) {
	slug, kerr := requireProjectSlug(n.config, "schedules")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "forge/schedules"+tail), nil
}

func (n *SchedulesNamespace) onePath(id, tail string) (string, *KError) {
	if kerr := requireID(id, "schedule"); kerr != nil {
		return "", kerr
	}
	return n.path("/" + url.PathEscape(id) + tail)
}

// List returns schedules, newest first, one page at a time. Pass
// Meta.Cursor.Next of one page as Cursor of the next, until it is nil.
func (n *SchedulesNamespace) List(ctx context.Context, opts ListSchedulesOptions) ([]Schedule, *Meta, *KError) {
	if kerr := checkLimit(opts.Limit); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.Status != "" {
		q["status"] = opts.Status
	}
	if opts.FunctionName != "" {
		q["function_name"] = opts.FunctionName
	}
	if opts.Limit != 0 {
		q["limit"] = strconv.Itoa(opts.Limit)
	}
	if opts.Cursor != "" {
		q["cursor"] = opts.Cursor
	}
	env, kerr := doRequest[[]Schedule](ctx, n.config, &requestInit{Method: "GET", Path: p, Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Get returns one schedule.
func (n *SchedulesNamespace) Get(ctx context.Context, id string) (*Schedule, *Meta, *KError) {
	p, kerr := n.onePath(id, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[Schedule](ctx, n.config, &requestInit{Method: "GET", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Create creates a schedule (answers 201).
func (n *SchedulesNamespace) Create(ctx context.Context, input CreateScheduleInput) (*Schedule, *Meta, *KError) {
	p, kerr := n.path("")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[Schedule](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: input})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Update changes a schedule. Send at least one field.
func (n *SchedulesNamespace) Update(ctx context.Context, id string, patch UpdateScheduleInput) (*Schedule, *Meta, *KError) {
	if patch == (UpdateScheduleInput{}) {
		return nil, nil, invalidField("patch needs at least one of: scheduleExpression, scheduleTimezone, catchupPolicy, status, enabled, logOkInvocations", nil)
	}
	p, kerr := n.onePath(id, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[Schedule](ctx, n.config, &requestInit{Method: "PATCH", Path: p, Body: patch})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Delete deletes a schedule. The gateway answers 204 with no body, so there is
// no result.
func (n *SchedulesNamespace) Delete(ctx context.Context, id string) *KError {
	p, kerr := n.onePath(id, "")
	if kerr != nil {
		return kerr
	}
	_, kerr = doRequest[any](ctx, n.config, &requestInit{Method: "DELETE", Path: p})
	return kerr
}

// Runs returns the schedule's runs, newest first, one page at a time.
func (n *SchedulesNamespace) Runs(ctx context.Context, id string, opts ListScheduleRunsOptions) ([]ScheduleRun, *Meta, *KError) {
	if kerr := checkLimit(opts.Limit); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.onePath(id, "/runs")
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
	env, kerr := doRequest[[]ScheduleRun](ctx, n.config, &requestInit{Method: "GET", Path: p, Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// RunNow starts one run outside the timetable (answers 202). The result is the
// body the gateway sent, undecoded.
func (n *SchedulesNamespace) RunNow(ctx context.Context, id string) (map[string]any, *Meta, *KError) {
	p, kerr := n.onePath(id, "/run-now")
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[map[string]any](ctx, n.config, &requestInit{Method: "POST", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}
