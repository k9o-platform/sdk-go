package k9o

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Metadata is a set of columns you declare on a collection so that they can be
// filtered, searched and followed live through one API, with the same access
// rules as the data itself.
//
// Declare the columns first (Metadata.Index.Declare); Set writes declared
// columns only, and Filter, Search and Vectors.Nearest read declared columns
// only. Column names given to Filter (Where[].Field), Search (Field) and
// Vectors.Nearest (Column) are sent as "metadata.<name>"; the prefix is added
// when you leave it out. Rows come back with bare column names under Metadata.
//
// Errors arrive as *KError with the gateway's code. The ones to expect:
//   - 400 K9O_UNKNOWN_SURFACE: the surface is not collection:<name>, telemetry:<event> or storage:<bucket>.
//   - 400 K9O_METADATA_FIELD_NOT_INDEXED: the column is not declared (Context["field"]).
//   - 400 K9O_ENCRYPTED_COLUMN_OPERATION_UNSUPPORTED: an encrypted column supports only "eq" (not null) and "in", no search or nearest.
//   - 400 K9O_INVALID_FIELD_VALUE, K9O_INVALID_CURSOR: a bad body field or cursor.
//   - 403 K9O_ACCESS_DENIED, K9O_EXTENSION_PERMISSION_DENIED: no access to the surface.
//   - 404 K9O_COLLECTION_NOT_FOUND (declare on a missing collection), K9O_NOT_FOUND (set on a missing record).
//   - 413 K9O_QUERY_RESULT_TOO_LARGE: more than 10000 rows across the surfaces (Context["cap"], Context["surfaces"]).
//   - 501 K9O_NOT_IMPLEMENTED: storage: surfaces are accepted by the SDK, and the gateway answers 501 for them.

const (
	metadataPrefix     = "metadata."
	metadataMaxColumns = 64
	metadataMaxLimit   = 1000
)

var (
	metadataOps = []string{
		"eq", "neq", "gt", "gte", "lt", "lte", "contains", "startsWith", "endsWith", "in", "notIn", "and", "or", "not",
	}
	metadataMetrics     = []string{"cosine", "euclidean", "dot"}
	metadataSlices      = []string{"collection", "telemetry", "storage"}
	metadataScopeRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	metadataIdentRe     = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
	metadataTelemetryFx = []string{"createdAt", "actorId"}
)

// MetadataWhere is one condition. On a collection: surface Field is a declared
// column ("priority" or "metadata.priority"; the prefix is added when missing)
// and each field may appear once. On a telemetry: surface the fields are
// "createdAt" (with gt, gte, lt, lte) and "actorId" (with eq).
//
// Op is one of eq, neq, gt, gte, lt, lte, contains, startsWith, endsWith, in,
// notIn. "and", "or" and "not" are part of the grammar, but the gateway refuses
// them today. Value is always sent; a nil Value is sent as null (with eq or
// neq it tests for null).
type MetadataWhere struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// MetadataRow is one result row. A collection: row has RecordID; a telemetry:
// row has EventID. Metadata holds the declared columns that are present on the
// row, under their bare names (an undeclared column never appears).
type MetadataRow struct {
	Surface  string         `json:"surface"`
	Scope    string         `json:"scope"`
	RecordID string         `json:"recordId,omitempty"`
	EventID  string         `json:"eventId,omitempty"`
	Metadata map[string]any `json:"metadata"`
}

// MetadataFilterInput is the input of Metadata.Filter.
type MetadataFilterInput struct {
	// Surfaces is one or more of collection:<name>, telemetry:<event-prefix>, storage:<bucket>.
	Surfaces []string
	// Where conditions apply to every surface named.
	Where []MetadataWhere
	// Limit is rows per surface, 1 to 1000. Zero means the gateway default (100).
	Limit int
	// Cursor is meta.Cursor.Next of the previous page. Allowed with exactly one surface.
	Cursor string
}

// MetadataSearchInput is the input of Metadata.Search.
type MetadataSearchInput struct {
	// Surface is a collection:<name> surface.
	Surface string
	// Field is the declared text column: "body" or "metadata.body".
	Field string
	// Q is the words to look for. Matching is by word stem, so "finance" does not match "financial".
	Q string
	// Limit is 1 to 1000. Zero means the gateway default (50). Search has no second page.
	Limit int
}

// MetadataNearestInput is the input of Metadata.Vectors.Nearest.
type MetadataNearestInput struct {
	// Surface is a collection:<name> surface.
	Surface string
	// Column is the declared vector column: "embedding" or "metadata.embedding".
	Column string
	// Embedding is the query embedding: a non-empty slice of finite numbers.
	Embedding []float64
	// K is how many rows to return, 1 to 1000. Zero means the gateway default (10).
	K int
	// Metric is "cosine" (default), "euclidean" or "dot".
	Metric string
	// Where conditions that rows must also meet; the same grammar as Filter.
	Where []MetadataWhere
}

// MetadataChange is the payload of a metadata change event: which record
// changed. The new values are not in the event; read the record by ID.
type MetadataChange struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

// DecodeMetadataChange reads the payload of an event from Metadata.Subscribe.
func DecodeMetadataChange(ev RealtimeEvent) (MetadataChange, error) {
	var c MetadataChange
	err := json.Unmarshal(ev.Payload, &c)
	return c, err
}

// MetadataSubscribeOptions configures Metadata.Subscribe.
type MetadataSubscribeOptions struct {
	// FromSequence starts from this feed position instead of from now. Nil
	// means from now. It is ignored once the subscription has resumed by itself
	// or been resynced.
	FromSequence *int
	// OnResync is called when the gateway reports that the feed position was
	// lost; see SubscribeOptions.OnResync.
	OnResync func(ctx context.Context, info ResyncInfo)

	idleTimeout time.Duration
	retryDelay  time.Duration
}

// MetadataIndexColumn is one column to declare.
type MetadataIndexColumn struct {
	Column string `json:"column"`
	// Indexed defaults to false.
	Indexed bool `json:"indexed,omitempty"`
}

// MetadataIndexEntry is a declared column.
type MetadataIndexEntry struct {
	Column  string `json:"column"`
	Indexed bool   `json:"indexed"`
}

// MetadataIndex is a collection's declaration: its declared columns, ordered by name.
type MetadataIndex struct {
	Collection string               `json:"collection"`
	Columns    []MetadataIndexEntry `json:"columns"`
}

// MetadataNamespace exposes client.Metadata.*.
//
// Calls that return rows return them with *Meta; the next page's cursor of
// Filter is meta.Cursor.Next (nil on the last page, and always nil when more
// than one surface is named).
type MetadataNamespace struct {
	config  *httpClientConfig
	Vectors *MetadataVectorsNamespace
	Index   *MetadataIndexNamespace
}

// MetadataVectorsNamespace exposes client.Metadata.Vectors.*.
type MetadataVectorsNamespace struct {
	parent *MetadataNamespace
}

// MetadataIndexNamespace exposes client.Metadata.Index.*.
type MetadataIndexNamespace struct {
	parent *MetadataNamespace
}

func newMetadataNamespace(cfg *httpClientConfig) *MetadataNamespace {
	n := &MetadataNamespace{config: cfg}
	n.Vectors = &MetadataVectorsNamespace{parent: n}
	n.Index = &MetadataIndexNamespace{parent: n}
	return n
}

func metadataContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (n *MetadataNamespace) path(tail string) (string, *KError) {
	slug, kerr := requireProjectSlug(n.config, "metadata")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "metadata/"+tail), nil
}

func metadataSliceOf(surface string) string {
	return surface[:strings.Index(surface, ":")]
}

func metadataCheckSurface(surface, name string) *KError {
	at := strings.Index(surface, ":")
	slice, scope := "", ""
	if at >= 0 {
		slice, scope = surface[:at], surface[at+1:]
	}
	if !metadataContains(metadataSlices, slice) || !metadataScopeRe.MatchString(scope) || (slice == "collection" && !metadataIdentRe.MatchString(scope)) {
		return &KError{
			Code:    "K9O_UNKNOWN_SURFACE",
			Message: name + ` "` + surface + `" is not a surface: use collection:<name>, telemetry:<event> or storage:<bucket>`,
			Context: map[string]any{"surface": surface},
		}
	}
	return nil
}

func metadataCheckCollectionSurface(surface, name string) *KError {
	if kerr := metadataCheckSurface(surface, name); kerr != nil {
		return kerr
	}
	if metadataSliceOf(surface) != "collection" {
		return invalidField(name+" must be a collection surface (collection:<name>)", map[string]any{"surface": surface})
	}
	return nil
}

func metadataCheckLimit(value int, name string) *KError {
	if value != 0 && (value < 1 || value > metadataMaxLimit) {
		return invalidField(name+" must be a whole number from 1 to 1000", map[string]any{name: value})
	}
	return nil
}

func metadataPrefixed(name, label string) (string, *KError) {
	if name == "" || name == metadataPrefix {
		return "", invalidField(label+" must be a column name", map[string]any{"field": label})
	}
	if strings.HasPrefix(name, metadataPrefix) {
		return name, nil
	}
	return metadataPrefix + name, nil
}

func metadataWireWhere(where []MetadataWhere, telemetry bool) ([]MetadataWhere, *KError) {
	if where == nil {
		return nil, nil
	}
	out := make([]MetadataWhere, len(where))
	for i, c := range where {
		label := "where[" + strconv.Itoa(i) + "]"
		if !metadataContains(metadataOps, c.Op) {
			return nil, invalidField(label+".op must be one of: "+strings.Join(metadataOps, ", "), map[string]any{"op": c.Op})
		}
		field := c.Field
		if !(telemetry && metadataContains(metadataTelemetryFx, field)) {
			var kerr *KError
			if field, kerr = metadataPrefixed(field, label+".field"); kerr != nil {
				return nil, kerr
			}
		}
		out[i] = MetadataWhere{Field: field, Op: c.Op, Value: c.Value}
	}
	return out, nil
}

func metadataCollection(collection string) *KError {
	if !metadataIdentRe.MatchString(collection) {
		return invalidField("collection must be a collection name", map[string]any{"collection": collection})
	}
	return nil
}

// Filter returns the rows of one or more surfaces whose declared columns meet
// Where, in request order. It pages with Cursor when exactly one surface is
// named (meta.Cursor.Next, nil on the last page). More than 10000 rows in all
// answers 413 K9O_QUERY_RESULT_TOO_LARGE; a storage: surface answers 501
// K9O_NOT_IMPLEMENTED. An invalid argument is reported as a *KError before any
// request is sent.
func (n *MetadataNamespace) Filter(ctx context.Context, in MetadataFilterInput) ([]MetadataRow, *Meta, *KError) {
	p, kerr := n.path("query")
	if kerr != nil {
		return nil, nil, kerr
	}
	if len(in.Surfaces) < 1 {
		return nil, nil, invalidField("surfaces must be a non-empty slice", nil)
	}
	telemetry := false
	for i, s := range in.Surfaces {
		if kerr := metadataCheckSurface(s, "surfaces["+strconv.Itoa(i)+"]"); kerr != nil {
			return nil, nil, kerr
		}
		if metadataSliceOf(s) == "telemetry" {
			telemetry = true
		}
	}
	if kerr := metadataCheckLimit(in.Limit, "limit"); kerr != nil {
		return nil, nil, kerr
	}
	if in.Cursor != "" && len(in.Surfaces) != 1 {
		return nil, nil, invalidField("cursor is allowed only with exactly one surface", map[string]any{"field": "cursor"})
	}
	wire, kerr := metadataWireWhere(in.Where, telemetry)
	if kerr != nil {
		return nil, nil, kerr
	}
	body := map[string]any{"surfaces": in.Surfaces}
	if wire != nil {
		body["where"] = wire
	}
	if in.Limit != 0 {
		body["limit"] = in.Limit
	}
	if in.Cursor != "" {
		body["cursor"] = in.Cursor
	}
	return n.rows(ctx, p, body)
}

func (n *MetadataNamespace) rows(ctx context.Context, p string, body any) ([]MetadataRow, *Meta, *KError) {
	env, kerr := doRequest[[]MetadataRow](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: body})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Set writes declared columns of one record, for example
// Set(ctx, "posts", "p2", map[string]any{"title": "New", "priority": 9}). Keys
// are bare column names (a leading "metadata." is dropped); a column that is
// not declared answers 400 K9O_METADATA_FIELD_NOT_INDEXED. It returns the whole
// updated record, including columns that are not declared. The write runs
// through the record API's rules, so a write that needs approval is queued as
// it is there (see meta.Approval).
func (n *MetadataNamespace) Set(ctx context.Context, collection, id string, metadata map[string]any) (map[string]any, *Meta, *KError) {
	slug, kerr := requireProjectSlug(n.config, "metadata")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := metadataCollection(collection); kerr != nil {
		return nil, nil, kerr
	}
	if id == "" {
		return nil, nil, invalidField("id must be a record id", map[string]any{"id": id})
	}
	if len(metadata) == 0 {
		return nil, nil, invalidField("metadata must be a map with at least one column", nil)
	}
	bare := make(map[string]any, len(metadata))
	for k, v := range metadata {
		bare[strings.TrimPrefix(k, metadataPrefix)] = v
	}
	env, kerr := doRequest[map[string]any](ctx, n.config, &requestInit{
		Method: "PATCH",
		Path:   ProjectPath(slug, "metadata/collection/"+url.PathEscape(collection)+"/"+url.PathEscape(id)),
		Body:   map[string]any{"metadata": bare},
	})
	if kerr != nil {
		return nil, nil, kerr
	}
	return env.Data, &env.Meta, nil
}

// Search is full-text search over one declared text column of a collection. It
// returns the first page only (there is no cursor input) and no rank.
func (n *MetadataNamespace) Search(ctx context.Context, in MetadataSearchInput) ([]MetadataRow, *Meta, *KError) {
	p, kerr := n.path("search")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := metadataCheckCollectionSurface(in.Surface, "surface"); kerr != nil {
		return nil, nil, kerr
	}
	field, kerr := metadataPrefixed(in.Field, "field")
	if kerr != nil {
		return nil, nil, kerr
	}
	if in.Q == "" {
		return nil, nil, invalidField("q must be a non-empty string", nil)
	}
	if kerr := metadataCheckLimit(in.Limit, "limit"); kerr != nil {
		return nil, nil, kerr
	}
	body := map[string]any{"surface": in.Surface, "field": field, "q": in.Q}
	if in.Limit != 0 {
		body["limit"] = in.Limit
	}
	return n.rows(ctx, p, body)
}

// Nearest returns the rows whose declared vector column is nearest to
// Embedding, closest first. Rows carry no score. At most 50 rows come back
// whatever K is, until the gateway forwards a larger page size. There is no
// next page (meta.Cursor.Next is nil).
func (v *MetadataVectorsNamespace) Nearest(ctx context.Context, in MetadataNearestInput) ([]MetadataRow, *Meta, *KError) {
	n := v.parent
	p, kerr := n.path("vectors/nearest")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := metadataCheckCollectionSurface(in.Surface, "surface"); kerr != nil {
		return nil, nil, kerr
	}
	column, kerr := metadataPrefixed(in.Column, "column")
	if kerr != nil {
		return nil, nil, kerr
	}
	if len(in.Embedding) < 1 {
		return nil, nil, invalidField("embedding must be a non-empty slice of numbers", nil)
	}
	for _, x := range in.Embedding {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, nil, invalidField("embedding must contain only finite numbers", nil)
		}
	}
	if kerr := metadataCheckLimit(in.K, "k"); kerr != nil {
		return nil, nil, kerr
	}
	if in.Metric != "" && !metadataContains(metadataMetrics, in.Metric) {
		return nil, nil, invalidField("metric must be one of: "+strings.Join(metadataMetrics, ", "), map[string]any{"metric": in.Metric})
	}
	wire, kerr := metadataWireWhere(in.Where, false)
	if kerr != nil {
		return nil, nil, kerr
	}
	body := map[string]any{"surface": in.Surface, "column": column, "embedding": in.Embedding}
	if in.K != 0 {
		body["k"] = in.K
	}
	if in.Metric != "" {
		body["metric"] = in.Metric
	}
	if wire != nil {
		body["where"] = wire
	}
	return n.rows(ctx, p, body)
}

// Subscribe follows changes of a collection:<name> surface live
// (GET /@<slug>/metadata/subscribe). Each event is row_inserted, row_updated or
// row_deleted with a payload of {collection, id} only (DecodeMetadataChange), so
// read the record by id for its values. It is the change-feed reader of
// Realtime.Subscribe: it reconnects with Last-Event-ID, drops events it already
// delivered and calls OnResync when the position is lost. Changes to
// undeclared columns are reported too. Cancel ctx to stop; both channels close.
func (n *MetadataNamespace) Subscribe(ctx context.Context, surface string, opts MetadataSubscribeOptions) (<-chan RealtimeEvent, <-chan *KError) {
	fail := func(kerr *KError) (<-chan RealtimeEvent, <-chan *KError) {
		events := make(chan RealtimeEvent)
		errs := make(chan *KError, 1)
		errs <- kerr
		close(errs)
		close(events)
		return events, errs
	}
	slug, kerr := requireProjectSlug(n.config, "metadata")
	if kerr != nil {
		return fail(kerr)
	}
	if kerr := metadataCheckCollectionSurface(surface, "surface"); kerr != nil {
		return fail(kerr)
	}
	so := SubscribeOptions{OnResync: opts.OnResync, idleTimeout: opts.idleTimeout, retryDelay: opts.retryDelay}
	if opts.FromSequence != nil {
		if *opts.FromSequence < 0 {
			return fail(invalidField("FromSequence must be a whole number, 0 or more", map[string]any{"fromSequence": *opts.FromSequence}))
		}
		so.initialQuery = map[string]string{"fromSequence": strconv.Itoa(*opts.FromSequence)}
	}
	return streamFeed(ctx, n.config, &requestInit{
		Method: "GET",
		Path:   ProjectPath(slug, "metadata/subscribe"),
		Query:  map[string]string{"surface": surface},
	}, so)
}

// Get returns the columns declared on a collection, ordered by name.
func (i *MetadataIndexNamespace) Get(ctx context.Context, collection string) (*MetadataIndex, *Meta, *KError) {
	n := i.parent
	if kerr := metadataCollection(collection); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("index/" + url.PathEscape(collection))
	if kerr != nil {
		return nil, nil, kerr
	}
	env, kerr := doRequest[MetadataIndex](ctx, n.config, &requestInit{Method: "GET", Path: p})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Declare declares the columns of a collection that metadata may use. The
// columns must already exist on the collection. The list replaces the previous
// declaration (an empty list clears it); at most 64 columns, no "id", no
// duplicates. It needs the project-admin role and the migrations permission.
func (i *MetadataIndexNamespace) Declare(ctx context.Context, collection string, columns []MetadataIndexColumn) (*MetadataIndex, *Meta, *KError) {
	n := i.parent
	if kerr := metadataCollection(collection); kerr != nil {
		return nil, nil, kerr
	}
	p, kerr := n.path("index/" + url.PathEscape(collection))
	if kerr != nil {
		return nil, nil, kerr
	}
	if len(columns) > metadataMaxColumns {
		return nil, nil, invalidField("at most 64 columns can be declared", map[string]any{"count": len(columns)})
	}
	seen := map[string]bool{}
	for idx, c := range columns {
		if !metadataIdentRe.MatchString(c.Column) || c.Column == "id" {
			return nil, nil, invalidField("columns["+strconv.Itoa(idx)+"].column must be a column name other than id", map[string]any{"column": c.Column})
		}
		if seen[c.Column] {
			return nil, nil, invalidField(`column "`+c.Column+`" is listed twice`, map[string]any{"column": c.Column})
		}
		seen[c.Column] = true
	}
	if columns == nil {
		columns = []MetadataIndexColumn{}
	}
	env, kerr := doRequest[MetadataIndex](ctx, n.config, &requestInit{Method: "PUT", Path: p, Body: map[string]any{"columns": columns}})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}
