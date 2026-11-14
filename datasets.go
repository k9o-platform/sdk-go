package k9o

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	datasetMaxUploadBytes = 25 << 20
	datasetMaxColumns     = 512
	datasetMinSeconds     = 60
)

var datasetReattestDays = map[int]bool{7: true, 30: true, 90: true, 365: true}

// Dataset answers are returned as map[string]any: the SDK passes the gateway's
// fields through as received.

// DatasetColumn is one column of a dataset. Type is "string", "int32",
// "int64", "float64", "bool", "date", "timestamp", "json", "bytes" or
// "decimal(p,s)".
type DatasetColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// DatasetStorageTarget says where a dataset's files live: Kind "managed" or
// Kind "byos" with the BindingID of your storage binding.
type DatasetStorageTarget struct {
	Kind      string `json:"kind"`
	BindingID string `json:"bindingId,omitempty"`
}

// DatasetCreateInput is the body of Analytics.Create.
type DatasetCreateInput struct {
	DisplayName string          `json:"displayName"`
	Columns     []DatasetColumn `json:"columns"`
	// PartitionBy lists partition keys (up to 16). A timestamp key needs a
	// transform such as "created_at:day"; "region" is added automatically.
	PartitionBy []string `json:"partitionBy,omitempty"`
	// ResidencyClass is "per-subject" or "shared".
	ResidencyClass string `json:"residencyClass"`
	// StorageTarget is optional; nil means managed storage.
	StorageTarget *DatasetStorageTarget `json:"storageTarget,omitempty"`
}

// DatasetDeleted is the answer to Analytics.Delete.
type DatasetDeleted struct {
	Slug   string `json:"slug"`
	Status string `json:"status"` // "retired"
}

// Deleted is the answer to deleting a rollup or a schedule.
type Deleted struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// DatasetWriteOptions configures Analytics.Write.
type DatasetWriteOptions struct {
	// OnError is "skip" (default: bad rows are skipped and reported) or
	// "reject" (a bad row refuses the whole write, 400 K9O_INGEST_SCHEMA_MISMATCH).
	OnError string
}

// DatasetUploadOptions configures Analytics.Upload.
type DatasetUploadOptions struct {
	// Format is "csv" or "json" (required).
	Format string
	// DryRun returns the detected columns and a 50-row preview without writing.
	DryRun bool
	// OnError is "skip" (default) or "reject".
	OnError string
	// Delimiter, Encoding and HasHeader apply to CSV.
	Delimiter string
	Encoding  string
	HasHeader *bool
	// Manifest, when set, creates the dataset from this manifest if the slug
	// does not exist yet and loads the file in the same call. It is sent with
	// the file as multipart/form-data. Needs the datasets:create permission.
	Manifest any
	// FileName names the file part of a multipart upload (default "upload.csv" or "upload.json").
	FileName string
}

// DatasetDownload is the original file of an upload.
type DatasetDownload struct {
	Data        []byte
	ContentType string // text/csv, application/json or application/octet-stream
}

// DatasetReadOptions configures Analytics.Read.
type DatasetReadOptions struct {
	// PartitionFilter constrains declared partition keys.
	PartitionFilter map[string]any
	// Cursor continues a read: pass Meta.Cursor.Next of the previous page.
	Cursor string
	// Limit is the page size (clamped to a page maximum).
	Limit int
}

// DatasetMaterializeInput names exactly one source for Analytics.Materialize.
type DatasetMaterializeInput struct {
	// FromCollection is a collection name.
	FromCollection string `json:"fromCollection,omitempty"`
	// FromQuery is a saved Studio query id that reads a single collection.
	FromQuery string `json:"fromQuery,omitempty"`
}

// ScheduleCreateInput is the body of Analytics.Schedules.Create.
type ScheduleCreateInput struct {
	FromCollection string `json:"fromCollection"`
	// IntervalSeconds is at least 60.
	IntervalSeconds int `json:"intervalSeconds"`
	// ReattestIntervalDays is 7, 30, 90 or 365; 0 leaves the default.
	ReattestIntervalDays int `json:"reattestIntervalDays,omitempty"`
}

// RollupMeasure is one measure of a rollup. Agg is "count", or "last", "sum",
// "avg", "min", "max", "count_distinct", "p50", "p95" or "p99" over Column.
type RollupMeasure struct {
	Name   string `json:"name"`
	Agg    string `json:"agg"`
	Column string `json:"column,omitempty"`
}

// RollupCreateInput is the body of Analytics.Rollups.Create. Set exactly one
// of SubjectField (per-subject data, suppressed below the cohort threshold)
// and NonPersonal.
type RollupCreateInput struct {
	Slug          string          `json:"slug"`
	Source        string          `json:"source"`
	Dimensions    []string        `json:"dimensions,omitempty"`
	Measures      []RollupMeasure `json:"measures"`
	WindowSeconds int             `json:"windowSeconds"`
	// ReattestIntervalDays is 7, 30, 90 or 365; 0 means the default, 90.
	ReattestIntervalDays int    `json:"reattestIntervalDays,omitempty"`
	SubjectField         string `json:"subjectField,omitempty"`
	NonPersonal          bool   `json:"nonPersonal,omitempty"`
}

// ReattestOptions configures a reattest call.
type ReattestOptions struct {
	// ReattestIntervalDays is a new interval: 7, 30, 90 or 365; 0 keeps the current one.
	ReattestIntervalDays int
}

// RollupReadOptions configures Rollups.Read.
type RollupReadOptions struct {
	// From and To are ISO 8601; the default is the last 24 hours (at most 50000 cells).
	From string
	To   string
	// Dims is a projection of the dimensions; only count, sum, min and max measures combine.
	Dims []string
}

// RollupCellEvent is one event of a rollup feed: a closed cell, or a cell held
// back because too few subjects contribute to it.
type RollupCellEvent struct {
	// EventID is the frame's eventId. A resume re-sends the last few seconds,
	// so de-duplicate by it if you store cells (the SDK already drops repeats).
	EventID string
	// Name is the SSE event name, "cell".
	Name string
	// Topic is "rollups.<slug>".
	Topic string
	// OccurredAt is an ISO 8601 timestamp.
	OccurredAt string

	// CellKey identifies the cell.
	CellKey string
	// Bucket is the time bucket of the cell, as received.
	Bucket any
	// Dims holds the dimension values; a suppressed cell has none.
	Dims map[string]any
	// Final is true for a closed cell.
	Final bool
	// Measures holds the measure values by name; a suppressed cell has none.
	Measures map[string]any
	// Status is "suppressed" for a cell below the cohort threshold, else empty.
	Status string
	// Reason says why a cell is suppressed, for example "cohort_below_k".
	Reason string

	// Data is the frame's payload as received.
	Data json.RawMessage
}

// Suppressed reports whether the cell was held back (Status "suppressed").
// Its Measures and Dims are empty; the Reason says why.
func (e RollupCellEvent) Suppressed() bool { return e.Status == "suppressed" }

// rollupFrame is the data object of a rollup frame:
// {"topic","eventType":"cell","payload":{...},"occurredAt","eventId"}.
type rollupFrame struct {
	Topic      string          `json:"topic"`
	EventType  string          `json:"eventType"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurredAt"`
	EventID    string          `json:"eventId"`
}

func decodeRollupFrame(name, sseID, payload string) (RollupCellEvent, string, bool) {
	var f rollupFrame
	if json.Unmarshal([]byte(payload), &f) != nil || len(f.Payload) == 0 {
		return RollupCellEvent{}, "", false
	}
	var p struct {
		CellKey  string         `json:"cellKey"`
		Bucket   any            `json:"bucket"`
		Dims     map[string]any `json:"dims"`
		Final    bool           `json:"final"`
		Measures map[string]any `json:"measures"`
		Status   string         `json:"status"`
		Reason   string         `json:"reason"`
	}
	if json.Unmarshal(f.Payload, &p) != nil {
		return RollupCellEvent{}, "", false
	}
	ev := RollupCellEvent{
		EventID: f.EventID, Name: name, Topic: f.Topic, OccurredAt: f.OccurredAt,
		CellKey: p.CellKey, Bucket: p.Bucket, Dims: p.Dims, Final: p.Final, Measures: p.Measures,
		Status: p.Status, Reason: p.Reason, Data: f.Payload,
	}
	if ev.Name == "" {
		ev.Name = f.EventType
	}
	if ev.EventID == "" {
		ev.EventID = sseID
	}
	return ev, ev.EventID, true
}

// RollupSubscribeOptions configures Rollups.Subscribe.
type RollupSubscribeOptions struct {
	// FromSequence starts the first connection at this position.
	FromSequence *int64
	// OnResync runs on a resync frame: the position can no longer be resumed
	// (action "reread-source"). Re-read the rollup with Rollups.Read inside it;
	// the feed then continues.
	OnResync func(ctx context.Context, info ResyncInfo)

	idleTimeout, retryDelay time.Duration
}

// AnalyticsNamespace exposes client.Analytics.*: datasets, SQL over datasets,
// rollups and schedules.
type AnalyticsNamespace struct {
	config    *httpClientConfig
	Rollups   *DatasetRollupsNamespace
	Schedules *DatasetSchedulesNamespace
}

// DatasetRollupsNamespace exposes client.Analytics.Rollups.*.
type DatasetRollupsNamespace struct{ parent *AnalyticsNamespace }

// DatasetSchedulesNamespace exposes client.Analytics.Schedules.*.
type DatasetSchedulesNamespace struct{ parent *AnalyticsNamespace }

// RollupsNamespace exposes client.Rollups.*: project-wide rollup reads.
type RollupsNamespace struct {
	config *httpClientConfig
}

func newAnalyticsNamespace(cfg *httpClientConfig) *AnalyticsNamespace {
	a := &AnalyticsNamespace{config: cfg}
	a.Rollups = &DatasetRollupsNamespace{parent: a}
	a.Schedules = &DatasetSchedulesNamespace{parent: a}
	return a
}

func (n *AnalyticsNamespace) path(sub string) (string, *KError) {
	slug, kerr := requireProjectSlug(n.config, "analytics")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "analytics"+sub), nil
}

func (n *AnalyticsNamespace) datasetPath(dataset, sub string) (string, *KError) {
	if kerr := requireID(dataset, "dataset slug"); kerr != nil {
		return "", kerr
	}
	return n.path("/" + url.PathEscape(dataset) + sub)
}

func call[T any](ctx context.Context, cfg *httpClientConfig, init *requestInit) (*T, *Meta, *KError) {
	env, kerr := doRequest[T](ctx, cfg, init)
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

type anyMap = map[string]any

// List lists the project's active datasets, newest first (GET /@<slug>/analytics).
func (n *AnalyticsNamespace) List(ctx context.Context) ([]map[string]any, *Meta, *KError) {
	p, kerr := n.path("")
	if kerr != nil {
		return nil, nil, kerr
	}
	return listOf(ctx, n.config, &requestInit{Method: "GET", Path: p}, "datasets")
}

// Query runs one read-only SQL SELECT over datasets (POST /@<slug>/analytics/query).
// Reference each dataset as dataset.<slug>; datasets lists the slugs the query
// reads (1 to 64). It needs a project role of admin, author or editor and the
// datasets:query permission.
func (n *AnalyticsNamespace) Query(ctx context.Context, sql string, datasets []string) ([]map[string]any, *Meta, *KError) {
	p, kerr := n.path("/query")
	if kerr != nil {
		return nil, nil, kerr
	}
	if strings.TrimSpace(sql) == "" || len(sql) > 100000 {
		return nil, nil, invalidField("sql must be 1 to 100000 characters", map[string]any{"field": "sql"})
	}
	if len(datasets) < 1 || len(datasets) > 64 {
		return nil, nil, invalidField("datasets must list 1 to 64 dataset slugs", map[string]any{"field": "datasets"})
	}
	return rows(ctx, n.config, &requestInit{Method: "POST", Path: p, Body: map[string]any{"sql": sql, "datasets": datasets}})
}

// rows decodes an answer that is a list of rows, or an object holding the list
// under "rows".
func rows(ctx context.Context, cfg *httpClientConfig, init *requestInit) ([]map[string]any, *Meta, *KError) {
	return listOf(ctx, cfg, init, "rows")
}

// listOf decodes an answer that is a JSON list of objects, or an object that
// holds the list under key (or under "items"). A missing list is empty.
func listOf(ctx context.Context, cfg *httpClientConfig, init *requestInit, key string) ([]map[string]any, *Meta, *KError) {
	env, kerr := doRequest[json.RawMessage](ctx, cfg, init)
	if kerr != nil {
		return nil, nil, kerr
	}
	raw := env.Data
	if len(raw) == 0 || string(raw) == "null" {
		return []anyMap{}, &env.Meta, nil
	}
	var list []anyMap
	if json.Unmarshal(raw, &list) != nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: "malformed answer: expected a list", Status: 200}
		}
		list = nil
		for _, k := range []string{key, "items"} {
			if r, ok := obj[k]; ok && json.Unmarshal(r, &list) == nil {
				break
			}
		}
	}
	if list == nil {
		list = []anyMap{}
	}
	return list, &env.Meta, nil
}

// Create creates a dataset at slug (POST /@<slug>/analytics/<dataset>). A slug
// is never reissued, even after the dataset is deleted. A project holds at most
// 200 datasets (429 K9O_DATASET_LIMIT_EXCEEDED).
func (n *AnalyticsNamespace) Create(ctx context.Context, dataset string, in DatasetCreateInput) (map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, nil, invalidField("DisplayName must not be empty", map[string]any{"field": "displayName"})
	}
	if len(in.Columns) < 1 || len(in.Columns) > datasetMaxColumns {
		return nil, nil, invalidField("Columns must hold 1 to 512 columns", map[string]any{"field": "columns"})
	}
	if in.ResidencyClass != "per-subject" && in.ResidencyClass != "shared" {
		return nil, nil, invalidField(`ResidencyClass must be "per-subject" or "shared"`, map[string]any{"field": "residencyClass"})
	}
	if t := in.StorageTarget; t != nil && (t.Kind != "managed" && t.Kind != "byos" || t.Kind == "byos" && t.BindingID == "") {
		return nil, nil, invalidField(`StorageTarget is {Kind: "managed"} or {Kind: "byos", BindingID}`, map[string]any{"field": "storageTarget"})
	}
	d, m, kerr := call[anyMap](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: in})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// Get returns one active dataset (GET /@<slug>/analytics/<dataset>).
func (n *AnalyticsNamespace) Get(ctx context.Context, dataset string) (map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	d, m, kerr := call[anyMap](ctx, n.config, &requestInit{Method: "GET", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// Delete retires a dataset (DELETE /@<slug>/analytics/<dataset>): it reads as
// absent from then on and its slug is never reissued.
func (n *AnalyticsNamespace) Delete(ctx context.Context, dataset string) (*DatasetDeleted, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "")
	if kerr != nil {
		return nil, nil, kerr
	}
	return call[DatasetDeleted](ctx, n.config, &requestInit{Method: "DELETE", Path: p})
}

// Materialize copies rows into the dataset from exactly one source
// (POST .../materialize). The source is read with your own row-level access.
func (n *AnalyticsNamespace) Materialize(ctx context.Context, dataset string, in DatasetMaterializeInput) (map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "/materialize")
	if kerr != nil {
		return nil, nil, kerr
	}
	if (in.FromCollection == "") == (in.FromQuery == "") {
		return nil, nil, invalidField("set exactly one of FromCollection and FromQuery", nil)
	}
	d, m, kerr := call[anyMap](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: in})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// Read pages through a dataset's rows (POST .../read). Each row carries a
// stable "_k9o_row_id". Meta.Cursor.Next continues (pass it as Cursor); the
// first page's Meta.Schema holds the columns.
func (n *AnalyticsNamespace) Read(ctx context.Context, dataset string, opts DatasetReadOptions) ([]map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "/read")
	if kerr != nil {
		return nil, nil, kerr
	}
	if opts.Limit < 0 {
		return nil, nil, invalidField("Limit must be 1 or more", map[string]any{"field": "limit"})
	}
	body := map[string]any{}
	if opts.PartitionFilter != nil {
		body["partitionFilter"] = opts.PartitionFilter
	}
	if opts.Cursor != "" {
		body["cursor"] = opts.Cursor
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	return rows(ctx, n.config, &requestInit{Method: "POST", Path: p, Body: body})
}

// Write appends rows (POST .../write). rows is a slice of objects (sent as a
// JSON array), or a string or []byte of newline-delimited JSON (sent as is).
// Each row is checked against the dataset's schema. The answer holds the
// counts: ingested, skipped, up to 100 skippedDetails, partitions and bytes.
func (n *AnalyticsNamespace) Write(ctx context.Context, dataset string, rows any, opts DatasetWriteOptions) (map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "/write")
	if kerr != nil {
		return nil, nil, kerr
	}
	if opts.OnError != "" && opts.OnError != "skip" && opts.OnError != "reject" {
		return nil, nil, invalidField(`OnError is "skip" or "reject"`, map[string]any{"field": "onError"})
	}
	init := &requestInit{Method: "POST", Path: p}
	if opts.OnError != "" {
		init.Query = map[string]string{"onError": opts.OnError}
	}
	switch r := rows.(type) {
	case string:
		init.RawBody, init.RawContentType = []byte(r), "application/x-ndjson"
	case []byte:
		init.RawBody, init.RawContentType = r, "application/x-ndjson"
	default:
		init.Body = rows
	}
	d, m, kerr := call[anyMap](ctx, n.config, init)
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// Upload loads a CSV or JSON file, up to 25 MiB (POST .../upload). data is a
// string, []byte or io.Reader. Without a Manifest the file is the raw body;
// with one it is sent as multipart/form-data (parts "file" and "manifest") and
// a slug that does not exist yet is created from it. The original file is kept
// (Analytics.DownloadOriginal). DryRun returns the detected columns and a
// 50-row preview without writing.
func (n *AnalyticsNamespace) Upload(ctx context.Context, dataset string, data any, opts DatasetUploadOptions) (map[string]any, *Meta, *KError) {
	p, kerr := n.datasetPath(dataset, "/upload")
	if kerr != nil {
		return nil, nil, kerr
	}
	if opts.Format != "csv" && opts.Format != "json" {
		return nil, nil, invalidField(`Format is required: "csv" or "json"`, map[string]any{"field": "format"})
	}
	if opts.OnError != "" && opts.OnError != "skip" && opts.OnError != "reject" {
		return nil, nil, invalidField(`OnError is "skip" or "reject"`, map[string]any{"field": "onError"})
	}
	var payload []byte
	switch d := data.(type) {
	case string:
		payload = []byte(d)
	case []byte:
		payload = d
	case io.Reader:
		b, err := io.ReadAll(io.LimitReader(d, datasetMaxUploadBytes+1))
		if err != nil {
			return nil, nil, invalidField(fmt.Sprintf("failed to read upload data: %v", err), map[string]any{"field": "data"})
		}
		payload = b
	default:
		return nil, nil, invalidField("upload data must be a string, []byte or io.Reader", map[string]any{"field": "data"})
	}
	if len(payload) > datasetMaxUploadBytes {
		return nil, nil, invalidField("the file is over 25 MiB", map[string]any{"field": "data"})
	}
	q := map[string]string{"format": opts.Format}
	if opts.DryRun {
		q["dryRun"] = "true"
	}
	if opts.OnError != "" {
		q["onError"] = opts.OnError
	}
	if opts.Delimiter != "" {
		q["delimiter"] = opts.Delimiter
	}
	if opts.Encoding != "" {
		q["encoding"] = opts.Encoding
	}
	if opts.HasHeader != nil {
		q["hasHeader"] = strconv.FormatBool(*opts.HasHeader)
	}
	ct := "text/csv"
	if opts.Format == "json" {
		ct = "application/json"
	}
	init := &requestInit{Method: "POST", Path: p, Query: q}
	if opts.Manifest == nil {
		init.RawBody, init.RawContentType = payload, ct
		if init.RawBody == nil {
			init.RawBody = []byte{}
		}
	} else {
		name := opts.FileName
		if name == "" {
			name = "upload." + opts.Format
		}
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(name)))
		hdr.Set("Content-Type", ct)
		fw, err := w.CreatePart(hdr)
		if err == nil {
			_, err = fw.Write(payload)
		}
		if err == nil {
			var mb []byte
			if mb, err = json.Marshal(opts.Manifest); err == nil {
				err = w.WriteField("manifest", string(mb))
			}
		}
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			return nil, nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to build multipart body: %v", err)}
		}
		init.RawBody, init.RawContentType = buf.Bytes(), w.FormDataContentType()
	}
	d, m, kerr := call[anyMap](ctx, n.config, init)
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// DownloadOriginal returns the original file of the dataset's stored upload
// (GET .../upload/original): the bytes and their content type, not a JSON
// envelope. 404 K9O_NOT_FOUND when none is stored.
func (n *AnalyticsNamespace) DownloadOriginal(ctx context.Context, dataset string) (*DatasetDownload, *KError) {
	p, kerr := n.datasetPath(dataset, "/upload/original")
	if kerr != nil {
		return nil, kerr
	}
	urlStr := strings.TrimRight(n.config.resolveOrigin(ctx), "/") + p
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to build request: %v", err)}
	}
	req.Header = n.config.buildHeaders(&requestInit{})
	req.Header.Set("Accept", "*/*")
	hc := n.config.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &KError{
			Code:    "K9O_UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("network error reaching %s: %v", urlStr, err),
			Context: map[string]any{"url": urlStr},
		}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to read response body from %s: %v", urlStr, err), Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var wire KErrorWire
		if json.Unmarshal(b, &wire) == nil && wire.Code != "" && wire.Message != "" {
			ke := kerrorFromWire(&wire, resp.StatusCode)
			if ke.RetryAfterSec == nil {
				ke.RetryAfterSec = retryAfterHeader(resp.Header)
			}
			return nil, withRequestID(ke, resp.Header)
		}
		return nil, withRequestID(&KError{
			Code:    "K9O_INTERNAL_ERROR",
			Message: fmt.Sprintf("upstream returned %d with non-KError body", resp.StatusCode),
			Context: map[string]any{"url": urlStr, "status": resp.StatusCode},
			Status:  resp.StatusCode,
		}, resp.Header)
	}
	return &DatasetDownload{Data: b, ContentType: resp.Header.Get("Content-Type")}, nil
}

func validReattest(days int) *KError {
	if days != 0 && !datasetReattestDays[days] {
		return invalidField("ReattestIntervalDays is 7, 30, 90 or 365", map[string]any{"field": "reattestIntervalDays"})
	}
	return nil
}

func reattestBody(o ReattestOptions) map[string]any {
	b := map[string]any{}
	if o.ReattestIntervalDays != 0 {
		b["reattestIntervalDays"] = o.ReattestIntervalDays
	}
	return b
}

// Create creates a recurring materialization from a collection into the
// dataset (POST .../schedules). Each run reads the collection with its
// creator's current access. The schedule pauses when its attestation lapses
// (7 to 365 days) unless a person re-attests it: see Reattest.
func (s *DatasetSchedulesNamespace) Create(ctx context.Context, dataset string, in ScheduleCreateInput) (map[string]any, *Meta, *KError) {
	p, kerr := s.parent.datasetPath(dataset, "/schedules")
	if kerr != nil {
		return nil, nil, kerr
	}
	if in.FromCollection == "" {
		return nil, nil, invalidField("FromCollection must not be empty", map[string]any{"field": "fromCollection"})
	}
	if in.IntervalSeconds < datasetMinSeconds {
		return nil, nil, invalidField("IntervalSeconds must be at least 60", map[string]any{"field": "intervalSeconds"})
	}
	if kerr := validReattest(in.ReattestIntervalDays); kerr != nil {
		return nil, nil, kerr
	}
	d, m, kerr := call[anyMap](ctx, s.parent.config, &requestInit{Method: "POST", Path: p, Body: in})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// List returns the dataset's schedules with lastRunAt, lastRunStatus,
// nextRunAt and attestedAt, expiresAt and attestationStatus.
func (s *DatasetSchedulesNamespace) List(ctx context.Context, dataset string) ([]map[string]any, *Meta, *KError) {
	p, kerr := s.parent.datasetPath(dataset, "/schedules")
	if kerr != nil {
		return nil, nil, kerr
	}
	return listOf(ctx, s.parent.config, &requestInit{Method: "GET", Path: p}, "schedules")
}

// Delete deletes a schedule.
func (s *DatasetSchedulesNamespace) Delete(ctx context.Context, dataset, scheduleID string) (*Deleted, *Meta, *KError) {
	p, kerr := s.parent.childPath(dataset, "/schedules/", scheduleID, "schedule id", "")
	if kerr != nil {
		return nil, nil, kerr
	}
	return call[Deleted](ctx, s.parent.config, &requestInit{Method: "DELETE", Path: p})
}

// Reattest confirms the schedule is still wanted: its attestation restarts
// now, optionally with a new interval, and a paused schedule resumes. Only a
// person can re-attest: a service, workload or extension token gets 403
// K9O_RBAC_DENY with Context["reason"] "human_only" (a personal access token
// acts as its person).
func (s *DatasetSchedulesNamespace) Reattest(ctx context.Context, dataset, scheduleID string, opts ReattestOptions) (map[string]any, *Meta, *KError) {
	p, kerr := s.parent.childPath(dataset, "/schedules/", scheduleID, "schedule id", "/reattest")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := validReattest(opts.ReattestIntervalDays); kerr != nil {
		return nil, nil, kerr
	}
	d, m, kerr := call[anyMap](ctx, s.parent.config, &requestInit{Method: "POST", Path: p, Body: reattestBody(opts)})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

func (n *AnalyticsNamespace) childPath(dataset, mid, id, what, tail string) (string, *KError) {
	if kerr := requireID(id, what); kerr != nil {
		return "", kerr
	}
	return n.datasetPath(dataset, mid+url.PathEscape(id)+tail)
}

// Create creates a continuous rollup that folds a stream into the dataset over
// fixed windows (POST .../rollups). Set exactly one of SubjectField and
// NonPersonal. The rollup pauses when its attestation lapses (7 to 365 days,
// default 90) unless a person re-attests it: see Reattest.
func (r *DatasetRollupsNamespace) Create(ctx context.Context, dataset string, in RollupCreateInput) (map[string]any, *Meta, *KError) {
	p, kerr := r.parent.datasetPath(dataset, "/rollups")
	if kerr != nil {
		return nil, nil, kerr
	}
	if in.Slug == "" || len(in.Slug) > 63 {
		return nil, nil, invalidField("Slug must be 1 to 63 characters", map[string]any{"field": "slug"})
	}
	if in.Source == "" {
		return nil, nil, invalidField("Source must not be empty", map[string]any{"field": "source"})
	}
	if len(in.Measures) < 1 || len(in.Measures) > 64 {
		return nil, nil, invalidField("Measures must hold 1 to 64 measures", map[string]any{"field": "measures"})
	}
	if len(in.Dimensions) > 16 {
		return nil, nil, invalidField("Dimensions holds at most 16 entries", map[string]any{"field": "dimensions"})
	}
	if in.WindowSeconds < datasetMinSeconds {
		return nil, nil, invalidField("WindowSeconds must be at least 60", map[string]any{"field": "windowSeconds"})
	}
	if (in.SubjectField == "") == !in.NonPersonal {
		return nil, nil, invalidField("set exactly one of SubjectField and NonPersonal", nil)
	}
	if kerr := validReattest(in.ReattestIntervalDays); kerr != nil {
		return nil, nil, kerr
	}
	d, m, kerr := call[anyMap](ctx, r.parent.config, &requestInit{Method: "POST", Path: p, Body: in})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// List returns the dataset's rollups with attestedAt, expiresAt and
// attestationStatus ("active", "expiring_soon" or "expired"; an expired rollup
// is paused, not deleted).
func (r *DatasetRollupsNamespace) List(ctx context.Context, dataset string) ([]map[string]any, *Meta, *KError) {
	p, kerr := r.parent.datasetPath(dataset, "/rollups")
	if kerr != nil {
		return nil, nil, kerr
	}
	return listOf(ctx, r.parent.config, &requestInit{Method: "GET", Path: p}, "rollups")
}

// Delete deletes a rollup.
func (r *DatasetRollupsNamespace) Delete(ctx context.Context, dataset, rollupID string) (*Deleted, *Meta, *KError) {
	p, kerr := r.parent.childPath(dataset, "/rollups/", rollupID, "rollup id", "")
	if kerr != nil {
		return nil, nil, kerr
	}
	return call[Deleted](ctx, r.parent.config, &requestInit{Method: "DELETE", Path: p})
}

// Reattest confirms the rollup's configuration is still wanted: its attestation
// restarts now, optionally with a new interval, and an expired rollup resumes.
// Only a person can re-attest: a service, workload or extension token gets 403
// K9O_RBAC_DENY with Context["reason"] "human_only" (a personal access token
// acts as its person).
func (r *DatasetRollupsNamespace) Reattest(ctx context.Context, dataset, rollupID string, opts ReattestOptions) (map[string]any, *Meta, *KError) {
	p, kerr := r.parent.childPath(dataset, "/rollups/", rollupID, "rollup id", "/reattest")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := validReattest(opts.ReattestIntervalDays); kerr != nil {
		return nil, nil, kerr
	}
	d, m, kerr := call[anyMap](ctx, r.parent.config, &requestInit{Method: "POST", Path: p, Body: reattestBody(opts)})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

func (n *RollupsNamespace) path(sub string) (string, *KError) {
	slug, kerr := requireProjectSlug(n.config, "rollups")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "rollups"+sub), nil
}

// List returns every rollup in the project whose dataset you can read, with
// its read and subscribe endpoints (GET /@<slug>/rollups).
func (n *RollupsNamespace) List(ctx context.Context) ([]map[string]any, *Meta, *KError) {
	p, kerr := n.path("")
	if kerr != nil {
		return nil, nil, kerr
	}
	return listOf(ctx, n.config, &requestInit{Method: "GET", Path: p}, "rollups")
}

// Read returns a rollup's cells for a time range (GET /@<slug>/rollups/<slug>/read).
// A cell below the cohort threshold comes back as {status: "suppressed",
// reason: "cohort_below_k"}; "coverage" in the answer reports the range, the
// last fold and whether it is complete.
func (n *RollupsNamespace) Read(ctx context.Context, rollupSlug string, opts RollupReadOptions) (map[string]any, *Meta, *KError) {
	if kerr := requireID(rollupSlug, "rollup slug"); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("/" + url.PathEscape(rollupSlug) + "/read")
	if kerr != nil {
		return nil, nil, kerr
	}
	q := map[string]string{}
	if opts.From != "" {
		q["from"] = opts.From
	}
	if opts.To != "" {
		q["to"] = opts.To
	}
	if len(opts.Dims) > 0 {
		q["dims"] = strings.Join(opts.Dims, ",")
	}
	d, m, kerr := call[anyMap](ctx, n.config, &requestInit{Method: "GET", Path: p, Query: q})
	if kerr != nil {
		return nil, nil, kerr
	}
	return *d, m, nil
}

// Subscribe follows a rollup live (GET /@<slug>/rollups/<slug>/subscribe): one
// RollupCellEvent per cell, the payload of each "cell" frame decoded. It is the
// change-feed reader of Realtime.Subscribe: it reconnects with Last-Event-ID
// (the frame's SSE id, the resume cursor), drops events it already delivered
// (a resume re-sends the last 5 seconds; the frame's eventId is the key),
// ignores heartbeats and replaces a connection that goes quiet. The gateway
// closes a connection after at most 300 seconds; the SDK reconnects. A
// position that can no longer be resumed (resync, action "reread-source")
// calls OnResync (re-read with Read); the feed then continues. The stream ends
// when the access token expires. At most a fixed number of streams may be open
// per caller (429 K9O_SUBSCRIPTION_LIMIT_EXCEEDED). Cancel ctx to stop; both
// channels close.
func (n *RollupsNamespace) Subscribe(ctx context.Context, rollupSlug string, opts RollupSubscribeOptions) (<-chan RollupCellEvent, <-chan *KError) {
	fail := func(kerr *KError) (<-chan RollupCellEvent, <-chan *KError) {
		events := make(chan RollupCellEvent)
		errs := make(chan *KError, 1)
		errs <- kerr
		close(errs)
		close(events)
		return events, errs
	}
	if kerr := requireID(rollupSlug, "rollup slug"); kerr != nil {
		return fail(kerr)
	}
	p, kerr := n.path("/" + url.PathEscape(rollupSlug) + "/subscribe")
	if kerr != nil {
		return fail(kerr)
	}
	so := SubscribeOptions{OnResync: opts.OnResync, idleTimeout: opts.idleTimeout, retryDelay: opts.retryDelay}
	if opts.FromSequence != nil {
		if *opts.FromSequence < 0 {
			return fail(invalidField("FromSequence must be 0 or more", map[string]any{"field": "fromSequence"}))
		}
		so.initialQuery = map[string]string{"fromSequence": strconv.FormatInt(*opts.FromSequence, 10)}
	}
	hooks := feedHooks[RollupCellEvent]{decode: decodeRollupFrame}
	return streamFeedOf(ctx, n.config, &requestInit{Method: "GET", Path: p}, so, hooks)
}
