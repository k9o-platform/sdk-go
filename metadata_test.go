package k9o

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const metadataQueryResp = `{"data":[{"surface":"collection:posts","scope":"posts","recordId":"p1","metadata":{"title":"Quarterly report","tags":["pinned","finance"],"priority":3,"embedding":"[1,0,0]"}},{"surface":"telemetry:collection.metadata_index_declared","scope":"collection.metadata_index_declared","eventId":"aud_1","metadata":{"id":"aud_1","type":"collection.metadata_index_declared","actorId":"usr_1","occurredAt":"2026-10-26T20:00:00.000Z"}}],"meta":{"requestId":"r1","cursor":{"next":"cur_2"}}}`

func TestMetadataFilterWireAndPrefix(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, metadataQueryResp)
	rows, meta, kerr := c.Metadata.Filter(context.Background(), MetadataFilterInput{
		Surfaces: []string{"collection:posts"},
		Where: []MetadataWhere{
			{Field: "priority", Op: "gte", Value: 2},
			{Field: "metadata.title", Op: "startsWith", Value: "Q"},
			{Field: "deletedAt", Op: "eq", Value: nil},
		},
		Limit:  25,
		Cursor: "cur_1",
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	r := seen[0]
	if r.method != "POST" || r.path != "/@portal/metadata/query" || len(r.query) != 0 {
		t.Fatalf("request: %+v", r)
	}
	want := map[string]any{
		"surfaces": []any{"collection:posts"},
		"where": []any{
			map[string]any{"field": "metadata.priority", "op": "gte", "value": float64(2)},
			map[string]any{"field": "metadata.title", "op": "startsWith", "value": "Q"},
			map[string]any{"field": "metadata.deletedAt", "op": "eq", "value": nil},
		},
		"limit": float64(25), "cursor": "cur_1",
	}
	if !reflect.DeepEqual(r.body, want) {
		t.Fatalf("body: %#v", r.body)
	}
	if len(rows) != 2 || rows[0].RecordID != "p1" || rows[0].Metadata["priority"] != float64(3) || rows[1].EventID != "aud_1" || rows[1].RecordID != "" {
		t.Fatalf("rows: %+v", rows)
	}
	if meta.Cursor == nil || meta.Cursor.Next == nil || *meta.Cursor.Next != "cur_2" {
		t.Fatalf("cursor: %+v", meta)
	}
}

func TestMetadataFilterTelemetryFieldsStayBare(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":[],"meta":{"cursor":{"next":null}}}`)
	_, meta, kerr := c.Metadata.Filter(context.Background(), MetadataFilterInput{
		Surfaces: []string{"telemetry:collection."},
		Where:    []MetadataWhere{{Field: "createdAt", Op: "gt", Value: "2026-10-26T00:00:00Z"}, {Field: "actorId", Op: "eq", Value: "usr_1"}},
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	w := seen[0].body["where"].([]any)
	if w[0].(map[string]any)["field"] != "createdAt" || w[1].(map[string]any)["field"] != "actorId" {
		t.Fatalf("where: %#v", w)
	}
	if _, has := seen[0].body["limit"]; has {
		t.Fatalf("limit must be absent: %v", seen[0].body)
	}
	if meta.Cursor == nil || meta.Cursor.Next != nil {
		t.Fatalf("cursor: %+v", meta.Cursor)
	}

	// With a collection surface alongside, createdAt is a column name and is prefixed only when no telemetry surface is present.
	seen = nil
	if _, _, kerr := c.Metadata.Filter(context.Background(), MetadataFilterInput{
		Surfaces: []string{"collection:posts"}, Where: []MetadataWhere{{Field: "createdAt", Op: "gt", Value: "x"}},
	}); kerr != nil {
		t.Fatal(kerr)
	}
	if seen[0].body["where"].([]any)[0].(map[string]any)["field"] != "metadata.createdAt" {
		t.Fatalf("where: %#v", seen[0].body["where"])
	}
}

func TestMetadataValidationSendsNothing(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":[],"meta":{}}`)
	ctx := context.Background()
	many := make([]MetadataIndexColumn, 65)
	for i := range many {
		many[i] = MetadataIndexColumn{Column: "c" + strings.Repeat("x", i)}
	}
	cases := []struct {
		name string
		code string
		run  func() *KError
	}{
		{"no surfaces", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{})
			return k
		}},
		{"bad slice", "K9O_UNKNOWN_SURFACE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"table:posts"}})
			return k
		}},
		{"no colon", "K9O_UNKNOWN_SURFACE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"posts"}})
			return k
		}},
		{"bad collection scope", "K9O_UNKNOWN_SURFACE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:a-b"}})
			return k
		}},
		{"unknown op", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:posts"}, Where: []MetadataWhere{{Field: "a", Op: "like"}}})
			return k
		}},
		{"limit 0 ok but 1001 not", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:posts"}, Limit: 1001})
			return k
		}},
		{"negative limit", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:posts"}, Limit: -1})
			return k
		}},
		{"cursor with two surfaces", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:a", "collection:b"}, Cursor: "x"})
			return k
		}},
		{"set bad collection", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Set(ctx, "a/b", "p1", map[string]any{"x": 1})
			return k
		}},
		{"set empty id", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Set(ctx, "posts", "", map[string]any{"x": 1})
			return k
		}},
		{"set empty map", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Set(ctx, "posts", "p1", map[string]any{})
			return k
		}},
		{"search telemetry surface", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Search(ctx, MetadataSearchInput{Surface: "telemetry:x", Field: "body", Q: "a"})
			return k
		}},
		{"search empty q", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Search(ctx, MetadataSearchInput{Surface: "collection:posts", Field: "body"})
			return k
		}},
		{"search empty field", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Search(ctx, MetadataSearchInput{Surface: "collection:posts", Q: "a"})
			return k
		}},
		{"nearest empty embedding", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Vectors.Nearest(ctx, MetadataNearestInput{Surface: "collection:posts", Column: "e"})
			return k
		}},
		{"nearest NaN", "K9O_INVALID_FIELD_VALUE", func() *KError {
			nan := 0.0
			nan = nan / nan
			_, _, k := c.Metadata.Vectors.Nearest(ctx, MetadataNearestInput{Surface: "collection:posts", Column: "e", Embedding: []float64{1, nan}})
			return k
		}},
		{"nearest bad metric", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Vectors.Nearest(ctx, MetadataNearestInput{Surface: "collection:posts", Column: "e", Embedding: []float64{1}, Metric: "l1"})
			return k
		}},
		{"nearest k too big", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Vectors.Nearest(ctx, MetadataNearestInput{Surface: "collection:posts", Column: "e", Embedding: []float64{1}, K: 1001})
			return k
		}},
		{"declare 65 columns", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Index.Declare(ctx, "posts", many)
			return k
		}},
		{"declare id", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Index.Declare(ctx, "posts", []MetadataIndexColumn{{Column: "id"}})
			return k
		}},
		{"declare duplicate", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Index.Declare(ctx, "posts", []MetadataIndexColumn{{Column: "a"}, {Column: "a"}})
			return k
		}},
		{"index get bad name", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, _, k := c.Metadata.Index.Get(ctx, "1x")
			return k
		}},
		{"subscribe telemetry", "K9O_INVALID_FIELD_VALUE", func() *KError {
			_, errs := c.Metadata.Subscribe(ctx, "telemetry:x", MetadataSubscribeOptions{})
			return <-errs
		}},
		{"subscribe negative fromSequence", "K9O_INVALID_FIELD_VALUE", func() *KError {
			neg := -1
			_, errs := c.Metadata.Subscribe(ctx, "collection:posts", MetadataSubscribeOptions{FromSequence: &neg})
			return <-errs
		}},
	}
	for _, tc := range cases {
		if k := tc.run(); k == nil || k.Code != tc.code {
			t.Errorf("%s: got %v, want %s", tc.name, k, tc.code)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("validation sent %d requests: %+v", len(seen), seen)
	}
}

func TestMetadataNeedsProject(t *testing.T) {
	c, kerr := NewClient(ClientOpts{BaseURL: "https://x.api.k9o.io", APIKey: "k"})
	if kerr != nil {
		t.Skip(kerr)
	}
	ctx := context.Background()
	if _, _, k := c.Metadata.Filter(ctx, MetadataFilterInput{Surfaces: []string{"collection:posts"}}); k == nil || k.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("got %v", k)
	}
	if _, _, k := c.Metadata.Set(ctx, "posts", "p1", map[string]any{"a": 1}); k == nil || k.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("got %v", k)
	}
}

func TestMetadataSet(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":{"id":"p2","title":"Team offsite 2027","priority":9,"secret":"s2"},"meta":{"requestId":"r1"}}`)
	rec, _, kerr := c.Metadata.Set(context.Background(), "posts", "p 2", map[string]any{"metadata.title": "Team offsite 2027", "priority": 9})
	if kerr != nil {
		t.Fatal(kerr)
	}
	r := seen[0]
	if r.method != "PATCH" || r.path != "/@portal/metadata/collection/posts/p 2" {
		t.Fatalf("request: %+v", r)
	}
	if !reflect.DeepEqual(r.body, map[string]any{"metadata": map[string]any{"title": "Team offsite 2027", "priority": float64(9)}}) {
		t.Fatalf("body: %#v", r.body)
	}
	if rec["id"] != "p2" || rec["secret"] != "s2" {
		t.Fatalf("record: %+v", rec)
	}
}

func TestMetadataSearch(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":[{"surface":"collection:posts","scope":"posts","recordId":"p3","metadata":{"title":"Budget draft"}}],"meta":{"cursor":{"next":null}}}`)
	rows, meta, kerr := c.Metadata.Search(context.Background(), MetadataSearchInput{Surface: "collection:posts", Field: "body", Q: "finance", Limit: 5})
	if kerr != nil {
		t.Fatal(kerr)
	}
	r := seen[0]
	if r.method != "POST" || r.path != "/@portal/metadata/search" ||
		!reflect.DeepEqual(r.body, map[string]any{"surface": "collection:posts", "field": "metadata.body", "q": "finance", "limit": float64(5)}) {
		t.Fatalf("request: %+v", r)
	}
	if len(rows) != 1 || rows[0].RecordID != "p3" || meta.Cursor == nil || meta.Cursor.Next != nil {
		t.Fatalf("rows=%+v meta=%+v", rows, meta)
	}
}

func TestMetadataNearest(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":[{"surface":"collection:posts","scope":"posts","recordId":"p1","metadata":{"embedding":"[1,0,0]"}}],"meta":{"cursor":{"next":null}}}`)
	rows, _, kerr := c.Metadata.Vectors.Nearest(context.Background(), MetadataNearestInput{
		Surface: "collection:posts", Column: "embedding", Embedding: []float64{1, 0, 0}, K: 3, Metric: "dot",
		Where: []MetadataWhere{{Field: "priority", Op: "gt", Value: 1}},
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	r := seen[0]
	want := map[string]any{
		"surface": "collection:posts", "column": "metadata.embedding", "embedding": []any{float64(1), float64(0), float64(0)},
		"k": float64(3), "metric": "dot",
		"where": []any{map[string]any{"field": "metadata.priority", "op": "gt", "value": float64(1)}},
	}
	if r.method != "POST" || r.path != "/@portal/metadata/vectors/nearest" || !reflect.DeepEqual(r.body, want) {
		t.Fatalf("request: %+v", r)
	}
	if len(rows) != 1 || rows[0].RecordID != "p1" {
		t.Fatalf("rows: %+v", rows)
	}

	// Defaults: k, metric and where are left out.
	seen = nil
	if _, _, kerr := c.Metadata.Vectors.Nearest(context.Background(), MetadataNearestInput{Surface: "collection:posts", Column: "metadata.embedding", Embedding: []float64{0.5}}); kerr != nil {
		t.Fatal(kerr)
	}
	if !reflect.DeepEqual(seen[0].body, map[string]any{"surface": "collection:posts", "column": "metadata.embedding", "embedding": []any{0.5}}) {
		t.Fatalf("body: %#v", seen[0].body)
	}
}

func TestMetadataIndex(t *testing.T) {
	var seen []seenReq
	resp := `{"data":{"collection":"posts","columns":[{"column":"body","indexed":false},{"column":"tags","indexed":true}]},"meta":{}}`
	c := queryClient(t, &seen, resp)
	ctx := context.Background()
	idx, _, kerr := c.Metadata.Index.Declare(ctx, "posts", []MetadataIndexColumn{{Column: "body"}, {Column: "tags", Indexed: true}})
	if kerr != nil {
		t.Fatal(kerr)
	}
	want := map[string]any{"columns": []any{map[string]any{"column": "body"}, map[string]any{"column": "tags", "indexed": true}}}
	if seen[0].method != "PUT" || seen[0].path != "/@portal/metadata/index/posts" || !reflect.DeepEqual(seen[0].body, want) {
		t.Fatalf("declare: %+v", seen[0])
	}
	if idx.Collection != "posts" || len(idx.Columns) != 2 || !idx.Columns[1].Indexed || idx.Columns[0].Indexed {
		t.Fatalf("index: %+v", idx)
	}

	// An empty declaration clears it and sends [] (not null).
	seen = nil
	if _, _, kerr := c.Metadata.Index.Declare(ctx, "posts", nil); kerr != nil {
		t.Fatal(kerr)
	}
	if cols, ok := seen[0].body["columns"].([]any); !ok || len(cols) != 0 {
		t.Fatalf("clear body: %#v", seen[0].body)
	}

	seen = nil
	if _, _, kerr := c.Metadata.Index.Get(ctx, "posts"); kerr != nil {
		t.Fatal(kerr)
	}
	if seen[0].method != "GET" || seen[0].path != "/@portal/metadata/index/posts" || seen[0].body != nil {
		t.Fatalf("get: %+v", seen[0])
	}
}

func TestMetadataErrorsDecode(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
		key    string
	}{
		{400, `{"code":"K9O_METADATA_FIELD_NOT_INDEXED","message":"not declared","context":{"surface":"collection:posts","field":"metadata.nope"}}`, "K9O_METADATA_FIELD_NOT_INDEXED", "field"},
		{400, `{"code":"K9O_UNKNOWN_SURFACE","message":"unknown","context":{"surface":"x"}}`, "K9O_UNKNOWN_SURFACE", "surface"},
		{400, `{"code":"K9O_ENCRYPTED_COLUMN_OPERATION_UNSUPPORTED","message":"enc","context":{"column":"secret","op":"gt"}}`, "K9O_ENCRYPTED_COLUMN_OPERATION_UNSUPPORTED", "column"},
		{403, `{"code":"K9O_EXTENSION_PERMISSION_DENIED","message":"no grant","context":{"domain":"documents","action":"read","qualifier":"posts"}}`, "K9O_EXTENSION_PERMISSION_DENIED", "domain"},
		{413, `{"code":"K9O_QUERY_RESULT_TOO_LARGE","message":"too large","context":{"cap":10000}}`, "K9O_QUERY_RESULT_TOO_LARGE", "cap"},
		{501, `{"code":"K9O_NOT_IMPLEMENTED","message":"storage","context":{"surface":"storage:files"}}`, "K9O_NOT_IMPLEMENTED", "surface"},
	}
	for _, tc := range cases {
		status, body := tc.status, tc.body
		c := p6Client(t, func(req *http.Request) *http.Response {
			var v any
			_ = json.Unmarshal([]byte(body), &v)
			return jsonResponse(status, v)
		})
		_, _, kerr := c.Metadata.Filter(context.Background(), MetadataFilterInput{Surfaces: []string{"collection:posts"}})
		if kerr == nil || kerr.Code != tc.code || kerr.Status != tc.status || kerr.Context[tc.key] == nil {
			t.Errorf("%d %s: got %+v", tc.status, tc.code, kerr)
		}
	}
}

func metaFrame(id, typ, rec string) string {
	return "id: " + id + "\nevent: " + typ + "\ndata: {\"topic\":\"collections.posts." + typ + "\",\"eventType\":\"" + typ + "\",\"payload\":{\"collection\":\"posts\",\"id\":\"" + rec + "\"},\"occurredAt\":\"2026-10-27T10:20:38.969Z\"}\n\n"
}

func TestMetadataSubscribeReconnectDedupeAndFromSequence(t *testing.T) {
	var lock sync.Mutex
	var mu struct {
		paths   []string
		queries []map[string]string
	}
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		lock.Lock()
		mu.queries = append(mu.queries, q)
		mu.paths = append(mu.paths, r.URL.Path)
		lock.Unlock()
		switch n {
		case 0:
			sendSSE(w, "retry: 5\n:ok\n\n"+metaFrame("c1", "row_inserted", "p1")+metaFrame("c2", "row_updated", "p2"))
		default: // resends c2, then a new one
			sendSSE(w, metaFrame("c2", "row_updated", "p2")+metaFrame("c3", "row_deleted", "p1"))
			<-r.Context().Done()
		}
	})
	seq := 7
	opts := MetadataSubscribeOptions{FromSequence: &seq, idleTimeout: 300 * time.Millisecond, retryDelay: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Metadata.Subscribe(ctx, "collection:posts", opts)
	a, b, c := recv(t, events), recv(t, events), recv(t, events)
	if a.EventID != "c1" || b.EventID != "c2" || c.EventID != "c3" || c.EventType != "row_deleted" {
		t.Fatalf("got %+v %+v %+v", a, b, c)
	}
	ch, err := DecodeMetadataChange(c)
	if err != nil || ch.Collection != "posts" || ch.ID != "p1" {
		t.Fatalf("change: %+v %v", ch, err)
	}
	ids := fs.ids()
	if len(ids) < 2 || ids[0] != "" || ids[1] != "c2" {
		t.Fatalf("Last-Event-ID per connection = %q", ids)
	}
	lock.Lock()
	defer lock.Unlock()
	if mu.paths[0] != "/@portal/metadata/subscribe" || mu.queries[0]["surface"] != "collection:posts" || mu.queries[0]["fromSequence"] != "7" {
		t.Fatalf("first connection: %v %v", mu.paths[0], mu.queries[0])
	}
	if mu.queries[1]["surface"] != "collection:posts" || mu.queries[1]["fromSequence"] != "" {
		t.Fatalf("fromSequence must be sent on the first connection only: %v", mu.queries[1])
	}
}

func TestMetadataSubscribeResyncDropsFromSequence(t *testing.T) {
	var lock sync.Mutex
	var queries []map[string]string
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		lock.Lock()
		queries = append(queries, q)
		lock.Unlock()
		switch n {
		case 0:
			sendSSE(w, "event: resync\ndata: {\"reason\":\"cursor-evicted\",\"action\":\"reread-source\"}\n\n")
		default:
			sendSSE(w, metaFrame("c1", "row_inserted", "p1"))
			<-r.Context().Done()
		}
	})
	seq := 3
	var got []ResyncInfo
	opts := MetadataSubscribeOptions{FromSequence: &seq, idleTimeout: 300 * time.Millisecond, retryDelay: 5 * time.Millisecond,
		OnResync: func(ctx context.Context, info ResyncInfo) { got = append(got, info) }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Metadata.Subscribe(ctx, "collection:posts", opts)
	if ev := recv(t, events); ev.EventID != "c1" {
		t.Fatalf("event: %+v", ev)
	}
	if len(got) != 1 || got[0].Reason != "cursor-evicted" {
		t.Fatalf("OnResync = %+v", got)
	}
	lock.Lock()
	defer lock.Unlock()
	if queries[0]["fromSequence"] != "3" || queries[1]["fromSequence"] != "" {
		t.Fatalf("queries: %v", queries)
	}
}

func TestMetadataSubscribeClientErrorOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":"K9O_UNKNOWN_SURFACE","message":"unknown surface","context":{"surface":"collection:zz"}}`))
	}))
	defer srv.Close()
	c, kerr := NewClient(ClientOpts{BaseURL: srv.URL, ProjectSlug: "portal", APIKey: "k"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	events, errs := c.Metadata.Subscribe(context.Background(), "collection:zz", MetadataSubscribeOptions{retryDelay: 5 * time.Millisecond})
	if k := <-errs; k == nil || k.Code != "K9O_UNKNOWN_SURFACE" {
		t.Fatalf("got %v", k)
	}
	if _, ok := <-events; ok {
		t.Fatal("events should be closed")
	}
}
