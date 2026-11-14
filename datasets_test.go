package k9o

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type dkSeen struct {
	method, path, rawQuery, ctype string
	body                          []byte
	header                        http.Header
}

func (s dkSeen) json(t *testing.T) map[string]any {
	t.Helper()
	m := map[string]any{}
	if len(s.body) == 0 {
		return m
	}
	if err := json.Unmarshal(s.body, &m); err != nil {
		t.Fatalf("body is not a JSON object: %q", s.body)
	}
	return m
}

// dkClient records the last request and answers with the given status and body.
func dkClient(t *testing.T, status int, body any) (*Client, *dkSeen) {
	t.Helper()
	seen := &dkSeen{}
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		seen.method, seen.path, seen.rawQuery = req.Method, req.URL.Path, req.URL.RawQuery
		seen.ctype, seen.header = req.Header.Get("Content-Type"), req.Header.Clone()
		if req.Body != nil {
			seen.body, _ = io.ReadAll(req.Body)
		}
		return jsonResponse(status, body)
	})
	return c, seen
}

func dkOK(data any) map[string]any { return map[string]any{"data": data, "meta": map[string]any{}} }

func wantReq(t *testing.T, s *dkSeen, method, path, query string) {
	t.Helper()
	if s.method != method || s.path != path || s.rawQuery != query {
		t.Fatalf("got %s %s?%s, want %s %s?%s", s.method, s.path, s.rawQuery, method, path, query)
	}
}

func TestAnalyticsCRUDWire(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK([]any{map[string]any{"slug": "orders"}}))
	list, _, kerr := c.Analytics.List(ctx)
	if kerr != nil || len(list) != 1 || list[0]["slug"] != "orders" {
		t.Fatalf("list: %v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/analytics", "")

	c, s = dkClient(t, 200, dkOK(nil))
	if list, _, kerr = c.Analytics.List(ctx); kerr != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list must be a non-nil empty slice: %v %v", list, kerr)
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"slug": "orders", "residencyClass": "shared"}))
	got, _, kerr := c.Analytics.Create(ctx, "orders", DatasetCreateInput{
		DisplayName:    "Orders",
		Columns:        []DatasetColumn{{Name: "city", Type: "string"}, {Name: "placed_at", Type: "timestamp"}},
		PartitionBy:    []string{"placed_at:day"},
		ResidencyClass: "shared",
		StorageTarget:  &DatasetStorageTarget{Kind: "byos", BindingID: "bnd_1"},
	})
	if kerr != nil || got["slug"] != "orders" {
		t.Fatalf("create: %v %v", got, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders", "")
	b := s.json(t)
	if b["displayName"] != "Orders" || b["residencyClass"] != "shared" || len(b["columns"].([]any)) != 2 ||
		b["partitionBy"].([]any)[0] != "placed_at:day" || b["storageTarget"].(map[string]any)["bindingId"] != "bnd_1" {
		t.Fatalf("create body: %v", b)
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"slug": "orders"}))
	if _, _, kerr = c.Analytics.Get(ctx, "orders"); kerr != nil {
		t.Fatal(kerr)
	}
	wantReq(t, s, "GET", "/@portal/analytics/orders", "")

	c, s = dkClient(t, 200, dkOK(map[string]any{"slug": "orders", "status": "retired"}))
	del, _, kerr := c.Analytics.Delete(ctx, "orders")
	if kerr != nil || del.Slug != "orders" || del.Status != "retired" {
		t.Fatalf("delete: %v %v", del, kerr)
	}
	wantReq(t, s, "DELETE", "/@portal/analytics/orders", "")
}

func TestAnalyticsValidationBeforeSend(t *testing.T) {
	ctx := context.Background()
	c := rampartTestClient(t, func(*http.Request) *http.Response { t.Fatal("no request expected"); return nil })
	cols := []DatasetColumn{{Name: "a", Type: "string"}}
	cases := map[string]*KError{}
	_, _, cases["empty slug"] = c.Analytics.Get(ctx, "")
	_, _, cases["no name"] = c.Analytics.Create(ctx, "x", DatasetCreateInput{Columns: cols, ResidencyClass: "shared"})
	_, _, cases["no columns"] = c.Analytics.Create(ctx, "x", DatasetCreateInput{DisplayName: "X", ResidencyClass: "shared"})
	_, _, cases["bad residency"] = c.Analytics.Create(ctx, "x", DatasetCreateInput{DisplayName: "X", Columns: cols, ResidencyClass: "eu"})
	_, _, cases["byos no binding"] = c.Analytics.Create(ctx, "x", DatasetCreateInput{DisplayName: "X", Columns: cols, ResidencyClass: "shared", StorageTarget: &DatasetStorageTarget{Kind: "byos"}})
	_, _, cases["query no sql"] = c.Analytics.Query(ctx, " ", []string{"a"})
	_, _, cases["query no datasets"] = c.Analytics.Query(ctx, "select 1", nil)
	_, _, cases["materialize none"] = c.Analytics.Materialize(ctx, "x", DatasetMaterializeInput{})
	_, _, cases["materialize both"] = c.Analytics.Materialize(ctx, "x", DatasetMaterializeInput{FromCollection: "a", FromQuery: "b"})
	_, _, cases["write onError"] = c.Analytics.Write(ctx, "x", []any{}, DatasetWriteOptions{OnError: "ignore"})
	_, _, cases["upload format"] = c.Analytics.Upload(ctx, "x", "a,b", DatasetUploadOptions{})
	_, _, cases["upload type"] = c.Analytics.Upload(ctx, "x", 42, DatasetUploadOptions{Format: "csv"})
	_, _, cases["upload big"] = c.Analytics.Upload(ctx, "x", make([]byte, 25<<20+1), DatasetUploadOptions{Format: "csv"})
	_, _, cases["schedule interval"] = c.Analytics.Schedules.Create(ctx, "x", ScheduleCreateInput{FromCollection: "a", IntervalSeconds: 59})
	_, _, cases["schedule days"] = c.Analytics.Schedules.Create(ctx, "x", ScheduleCreateInput{FromCollection: "a", IntervalSeconds: 60, ReattestIntervalDays: 10})
	_, _, cases["schedule delete id"] = c.Analytics.Schedules.Delete(ctx, "x", "")
	_, _, cases["rollup window"] = c.Analytics.Rollups.Create(ctx, "x", RollupCreateInput{Slug: "r", Source: "s", Measures: []RollupMeasure{{Name: "n", Agg: "count"}}, WindowSeconds: 30, NonPersonal: true})
	_, _, cases["rollup neither"] = c.Analytics.Rollups.Create(ctx, "x", RollupCreateInput{Slug: "r", Source: "s", Measures: []RollupMeasure{{Name: "n", Agg: "count"}}, WindowSeconds: 60})
	_, _, cases["rollup both"] = c.Analytics.Rollups.Create(ctx, "x", RollupCreateInput{Slug: "r", Source: "s", Measures: []RollupMeasure{{Name: "n", Agg: "count"}}, WindowSeconds: 60, NonPersonal: true, SubjectField: "uid"})
	_, _, cases["rollup measures"] = c.Analytics.Rollups.Create(ctx, "x", RollupCreateInput{Slug: "r", Source: "s", WindowSeconds: 60, NonPersonal: true})
	_, _, cases["reattest days"] = c.Analytics.Rollups.Reattest(ctx, "x", "rol_1", ReattestOptions{ReattestIntervalDays: 14})
	_, _, cases["rollup read slug"] = c.Rollups.Read(ctx, "", RollupReadOptions{})
	for name, kerr := range cases {
		if kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
			t.Errorf("%s: want K9O_INVALID_FIELD_VALUE, got %v", name, kerr)
		}
	}
	// a client with no project
	nc, _ := NewClient(ClientOpts{BaseURL: "https://acme.api.k9o.io"})
	if _, _, kerr := nc.Analytics.List(ctx); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("no project: %v", kerr)
	}
	if _, _, kerr := nc.Keys.List(ctx, KeysListOptions{}); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("no project (keys): %v", kerr)
	}
}

func TestAnalyticsQueryReadWriteMaterialize(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK([]any{map[string]any{"city": "Berlin", "n": 3}}))
	rows, _, kerr := c.Analytics.Query(ctx, "SELECT city, count(*) AS n FROM dataset.orders GROUP BY city", []string{"orders"})
	if kerr != nil || len(rows) != 1 || rows[0]["city"] != "Berlin" {
		t.Fatalf("query: %v %v", rows, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/query", "")
	if b := s.json(t); !strings.HasPrefix(b["sql"].(string), "SELECT city") || b["datasets"].([]any)[0] != "orders" {
		t.Fatalf("query body: %v", b)
	}

	// an object holding the rows is read too
	c, _ = dkClient(t, 200, dkOK(map[string]any{"rows": []any{map[string]any{"a": 1}}}))
	if rows, _, kerr = c.Analytics.Query(ctx, "select 1", []string{"orders"}); kerr != nil || len(rows) != 1 {
		t.Fatalf("rows object: %v %v", rows, kerr)
	}

	// read: cursor in, cursor and schema out
	env := map[string]any{
		"data": []any{map[string]any{"_k9o_row_id": "r1", "city": "Berlin"}},
		"meta": map[string]any{"cursor": map[string]any{"next": "cur_2"}, "schema": []any{map[string]any{"name": "city", "type": "string"}}},
	}
	c, s = dkClient(t, 200, env)
	rows, meta, kerr := c.Analytics.Read(ctx, "orders", DatasetReadOptions{Limit: 100, Cursor: "cur_1", PartitionFilter: map[string]any{"region": "de01"}})
	if kerr != nil || rows[0]["_k9o_row_id"] != "r1" || meta.Cursor == nil || *meta.Cursor.Next != "cur_2" || !strings.Contains(string(meta.Schema), `"city"`) {
		t.Fatalf("read: %v %+v %v", rows, meta, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/read", "")
	if b := s.json(t); b["cursor"] != "cur_1" || b["limit"] != float64(100) || b["partitionFilter"].(map[string]any)["region"] != "de01" {
		t.Fatalf("read body: %v", b)
	}
	c, s = dkClient(t, 200, env)
	c.Analytics.Read(ctx, "orders", DatasetReadOptions{})
	if string(s.body) != "{}" {
		t.Fatalf("empty read body: %q", s.body)
	}

	// write: array, NDJSON, onError
	c, s = dkClient(t, 200, dkOK(map[string]any{"ingested": 2, "skipped": 0}))
	res, _, kerr := c.Analytics.Write(ctx, "orders", []map[string]any{{"city": "Berlin"}, {"city": "San Francisco"}}, DatasetWriteOptions{OnError: "reject"})
	if kerr != nil || res["ingested"] != float64(2) {
		t.Fatalf("write: %v %v", res, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/write", "onError=reject")
	if s.ctype != "application/json" || !strings.HasPrefix(string(s.body), `[{"city":"Berlin"}`) {
		t.Fatalf("write body: %s %q", s.ctype, s.body)
	}
	c, s = dkClient(t, 200, dkOK(map[string]any{"ingested": 2}))
	c.Analytics.Write(ctx, "orders", "{\"city\":\"Berlin\"}\n{\"city\":\"Tom\"}\n", DatasetWriteOptions{})
	wantReq(t, s, "POST", "/@portal/analytics/orders/write", "")
	if s.ctype != "application/x-ndjson" || string(s.body) != "{\"city\":\"Berlin\"}\n{\"city\":\"Tom\"}\n" {
		t.Fatalf("ndjson body: %s %q", s.ctype, s.body)
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"rowsRead": 5, "ingested": 5, "rejected": 0}))
	if res, _, kerr = c.Analytics.Materialize(ctx, "orders", DatasetMaterializeInput{FromCollection: "posts"}); kerr != nil || res["ingested"] != float64(5) {
		t.Fatalf("materialize: %v %v", res, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/materialize", "")
	if b := s.json(t); b["fromCollection"] != "posts" || len(b) != 1 {
		t.Fatalf("materialize body: %v", b)
	}
}

func TestAnalyticsUpload(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK(map[string]any{"ingested": 2}))
	yes := true
	_, _, kerr := c.Analytics.Upload(ctx, "orders", "city,n\nBerlin,3\n", DatasetUploadOptions{Format: "csv", DryRun: true, OnError: "reject", Delimiter: ",", Encoding: "utf-8", HasHeader: &yes})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if s.method != "POST" || s.path != "/@portal/analytics/orders/upload" {
		t.Fatalf("upload: %s %s", s.method, s.path)
	}
	for _, want := range []string{"format=csv", "dryRun=true", "onError=reject", "delimiter=%2C", "encoding=utf-8", "hasHeader=true"} {
		if !strings.Contains(s.rawQuery, want) {
			t.Errorf("query %q lacks %s", s.rawQuery, want)
		}
	}
	if s.ctype != "text/csv" || string(s.body) != "city,n\nBerlin,3\n" {
		t.Fatalf("raw upload: %s %q", s.ctype, s.body)
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"ingested": 1}))
	c.Analytics.Upload(ctx, "orders", strings.NewReader(`[{"a":1}]`), DatasetUploadOptions{Format: "json"})
	wantReq(t, s, "POST", "/@portal/analytics/orders/upload", "format=json")
	if s.ctype != "application/json" || string(s.body) != `[{"a":1}]` {
		t.Fatalf("reader upload: %s %q", s.ctype, s.body)
	}

	// manifest: multipart with file and manifest parts
	c, s = dkClient(t, 200, dkOK(map[string]any{"ingested": 1}))
	manifest := map[string]any{"displayName": "Orders", "residencyClass": "shared"}
	if _, _, kerr = c.Analytics.Upload(ctx, "orders", []byte("a\n1\n"), DatasetUploadOptions{Format: "csv", Manifest: manifest}); kerr != nil {
		t.Fatal(kerr)
	}
	mt, params, err := mime.ParseMediaType(s.ctype)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type: %s", s.ctype)
	}
	mr := multipart.NewReader(strings.NewReader(string(s.body)), params["boundary"])
	parts := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		parts[p.FormName()] = string(b)
		if p.FormName() == "file" && (p.FileName() != "upload.csv" || p.Header.Get("Content-Type") != "text/csv") {
			t.Errorf("file part: %s %s", p.FileName(), p.Header.Get("Content-Type"))
		}
	}
	if parts["file"] != "a\n1\n" || !strings.Contains(parts["manifest"], `"displayName":"Orders"`) {
		t.Fatalf("parts: %v", parts)
	}
}

func TestAnalyticsDownloadOriginal(t *testing.T) {
	ctx := context.Background()
	var path, accept string
	c := rampartTestClient(t, func(req *http.Request) *http.Response {
		path, accept = req.URL.Path, req.Header.Get("Accept")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/csv"}}, Body: io.NopCloser(strings.NewReader("city\nBerlin\n\x00\xff"))}
	})
	d, kerr := c.Analytics.DownloadOriginal(ctx, "orders")
	if kerr != nil || d.ContentType != "text/csv" || string(d.Data) != "city\nBerlin\n\x00\xff" {
		t.Fatalf("download: %+v %v", d, kerr)
	}
	if path != "/@portal/analytics/orders/upload/original" || accept != "*/*" {
		t.Fatalf("download request: %s %s", path, accept)
	}

	c, _ = dkClient(t, 404, map[string]any{"code": "K9O_NOT_FOUND", "message": "no stored upload"})
	if d, kerr = c.Analytics.DownloadOriginal(ctx, "orders"); d != nil || kerr == nil || kerr.Code != "K9O_NOT_FOUND" || kerr.Status != 404 {
		t.Fatalf("404: %v %v", d, kerr)
	}
	c, _ = dkClient(t, 503, map[string]any{"code": "K9O_ANALYTICS_UNAVAILABLE", "message": "not provisioned"})
	if _, kerr = c.Analytics.DownloadOriginal(ctx, "orders"); kerr == nil || kerr.Code != "K9O_ANALYTICS_UNAVAILABLE" {
		t.Fatalf("503: %v", kerr)
	}
}

func TestRollupsAndSchedulesWire(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK(map[string]any{"id": "rol_1", "slug": "sales_by_city"}))
	_, _, kerr := c.Analytics.Rollups.Create(ctx, "orders", RollupCreateInput{
		Slug: "sales_by_city", Source: "orders_stream", Dimensions: []string{"city"},
		Measures:      []RollupMeasure{{Name: "orders", Agg: "count"}, {Name: "revenue", Agg: "sum", Column: "amount"}},
		WindowSeconds: 300, ReattestIntervalDays: 30, NonPersonal: true,
	})
	if kerr != nil {
		t.Fatal(kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/rollups", "")
	b := s.json(t)
	if b["slug"] != "sales_by_city" || b["windowSeconds"] != float64(300) || b["reattestIntervalDays"] != float64(30) || b["nonPersonal"] != true ||
		len(b["measures"].([]any)) != 2 || b["measures"].([]any)[1].(map[string]any)["column"] != "amount" {
		t.Fatalf("rollup body: %v", b)
	}
	if _, has := b["subjectField"]; has {
		t.Fatal("subjectField must be omitted")
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "rol_2"}))
	c.Analytics.Rollups.Create(ctx, "orders", RollupCreateInput{Slug: "r", Source: "s", Measures: []RollupMeasure{{Name: "n", Agg: "count"}}, WindowSeconds: 60, SubjectField: "customer_id"})
	if b = s.json(t); b["subjectField"] != "customer_id" {
		t.Fatalf("subject body: %v", b)
	} else if _, has := b["nonPersonal"]; has {
		t.Fatal("nonPersonal must be omitted")
	}

	c, s = dkClient(t, 200, dkOK([]any{map[string]any{"id": "rol_1", "attestationStatus": "expiring_soon"}}))
	list, _, kerr := c.Analytics.Rollups.List(ctx, "orders")
	if kerr != nil || list[0]["attestationStatus"] != "expiring_soon" {
		t.Fatalf("rollups list: %v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/analytics/orders/rollups", "")

	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "rol_1", "deleted": true}))
	del, _, kerr := c.Analytics.Rollups.Delete(ctx, "orders", "rol_1")
	if kerr != nil || del.ID != "rol_1" || !del.Deleted {
		t.Fatalf("rollup delete: %v %v", del, kerr)
	}
	wantReq(t, s, "DELETE", "/@portal/analytics/orders/rollups/rol_1", "")

	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "rol_1", "expiresAt": "2027-01-27T00:00:00Z"}))
	got, _, kerr := c.Analytics.Rollups.Reattest(ctx, "orders", "rol_1", ReattestOptions{ReattestIntervalDays: 365})
	if kerr != nil || got["expiresAt"] == nil {
		t.Fatalf("rollup reattest: %v %v", got, kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/rollups/rol_1/reattest", "")
	if b = s.json(t); b["reattestIntervalDays"] != float64(365) {
		t.Fatalf("reattest body: %v", b)
	}
	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "rol_1"}))
	c.Analytics.Rollups.Reattest(ctx, "orders", "rol_1", ReattestOptions{})
	if string(s.body) != "{}" {
		t.Fatalf("reattest default body: %q", s.body)
	}

	// schedules
	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "sch_1"}))
	if _, _, kerr = c.Analytics.Schedules.Create(ctx, "orders", ScheduleCreateInput{FromCollection: "posts", IntervalSeconds: 3600, ReattestIntervalDays: 7}); kerr != nil {
		t.Fatal(kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/schedules", "")
	if b = s.json(t); b["fromCollection"] != "posts" || b["intervalSeconds"] != float64(3600) || b["reattestIntervalDays"] != float64(7) {
		t.Fatalf("schedule body: %v", b)
	}
	c, s = dkClient(t, 200, dkOK([]any{map[string]any{"id": "sch_1", "nextRunAt": "2026-10-27T11:00:00Z"}}))
	list, _, kerr = c.Analytics.Schedules.List(ctx, "orders")
	if kerr != nil || list[0]["nextRunAt"] == nil {
		t.Fatalf("schedules list: %v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/analytics/orders/schedules", "")
	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "sch_1", "deleted": true}))
	if del, _, kerr = c.Analytics.Schedules.Delete(ctx, "orders", "sch_1"); kerr != nil || !del.Deleted {
		t.Fatalf("schedule delete: %v %v", del, kerr)
	}
	wantReq(t, s, "DELETE", "/@portal/analytics/orders/schedules/sch_1", "")
	c, s = dkClient(t, 200, dkOK(map[string]any{"id": "sch_1"}))
	if _, _, kerr = c.Analytics.Schedules.Reattest(ctx, "orders", "sch_1", ReattestOptions{ReattestIntervalDays: 90}); kerr != nil {
		t.Fatal(kerr)
	}
	wantReq(t, s, "POST", "/@portal/analytics/orders/schedules/sch_1/reattest", "")
}

// Reattest is human only; the SDK does not special-case the refusal.
func TestReattestHumanOnlyIsPassedThrough(t *testing.T) {
	c, _ := dkClient(t, 403, map[string]any{"code": "K9O_RBAC_DENY", "message": "only a person can re-attest", "context": map[string]any{"reason": "human_only"}})
	for name, call := range map[string]func() *KError{
		"rollup": func() *KError {
			_, _, k := c.Analytics.Rollups.Reattest(context.Background(), "orders", "rol_1", ReattestOptions{})
			return k
		},
		"schedule": func() *KError {
			_, _, k := c.Analytics.Schedules.Reattest(context.Background(), "orders", "sch_1", ReattestOptions{})
			return k
		},
	} {
		kerr := call()
		if kerr == nil || kerr.Code != "K9O_RBAC_DENY" || kerr.Status != 403 || kerr.Context["reason"] != "human_only" {
			t.Errorf("%s: %v", name, kerr)
		}
	}
}

func TestProjectRollupsReadWire(t *testing.T) {
	ctx := context.Background()
	c, s := dkClient(t, 200, dkOK([]any{map[string]any{"slug": "sales_by_city"}}))
	list, _, kerr := c.Rollups.List(ctx)
	if kerr != nil || list[0]["slug"] != "sales_by_city" {
		t.Fatalf("list: %v %v", list, kerr)
	}
	wantReq(t, s, "GET", "/@portal/rollups", "")

	// the list may also arrive as an object holding it
	c, _ = dkClient(t, 200, dkOK(map[string]any{"rollups": []any{map[string]any{"slug": "a"}, map[string]any{"slug": "b"}}}))
	if list, _, kerr = c.Rollups.List(ctx); kerr != nil || len(list) != 2 || list[1]["slug"] != "b" {
		t.Fatalf("object list: %v %v", list, kerr)
	}
	c, _ = dkClient(t, 200, dkOK(map[string]any{}))
	if list, _, kerr = c.Rollups.List(ctx); kerr != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty object: %v %v", list, kerr)
	}
	c, _ = dkClient(t, 200, dkOK("nope"))
	if _, _, kerr = c.Rollups.List(ctx); kerr == nil || kerr.Code != "K9O_INTERNAL_ERROR" {
		t.Fatalf("malformed list: %v", kerr)
	}

	c, s = dkClient(t, 200, dkOK(map[string]any{"cells": []any{map[string]any{"city": "Berlin", "orders": 3}, map[string]any{"status": "suppressed", "reason": "cohort_below_k"}}, "coverage": map[string]any{"complete": true}}))
	got, _, kerr := c.Rollups.Read(ctx, "sales_by_city", RollupReadOptions{From: "2026-10-26T00:00:00Z", To: "2026-10-27T00:00:00Z", Dims: []string{"city", "device"}})
	if kerr != nil || len(got["cells"].([]any)) != 2 || got["coverage"].(map[string]any)["complete"] != true {
		t.Fatalf("read: %v %v", got, kerr)
	}
	if s.method != "GET" || s.path != "/@portal/rollups/sales_by_city/read" {
		t.Fatalf("read request: %s %s", s.method, s.path)
	}
	for _, want := range []string{"from=2026-10-26T00%3A00%3A00Z", "to=2026-10-27T00%3A00%3A00Z", "dims=city%2Cdevice"} {
		if !strings.Contains(s.rawQuery, want) {
			t.Errorf("query %q lacks %s", s.rawQuery, want)
		}
	}
	c, s = dkClient(t, 409, map[string]any{"code": "K9O_STATE_CONFLICT", "message": "cohort marker missing"})
	if _, _, kerr = c.Rollups.Read(ctx, "sales_by_city", RollupReadOptions{}); kerr == nil || kerr.Code != "K9O_STATE_CONFLICT" || kerr.Status != 409 {
		t.Fatalf("409: %v", kerr)
	}
	if s.rawQuery != "" {
		t.Fatalf("no options means no query: %q", s.rawQuery)
	}
}

// sseServer answers each GET with the scripted frames for that connection.
type sseServer struct {
	mu      sync.Mutex
	lastIDs []string
	queries []string
	posts   []map[string]any
	script  func(n int, w http.ResponseWriter, r *http.Request)
	replay  func(body map[string]any) any
	srv     *httptest.Server
}

func newSSEServer(t *testing.T, script func(n int, w http.ResponseWriter, r *http.Request)) *sseServer {
	s := &sseServer{script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			return
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			m := map[string]any{}
			json.Unmarshal(b, &m)
			s.mu.Lock()
			s.posts = append(s.posts, m)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(dkOK(s.replay(m)))
			return
		}
		s.mu.Lock()
		s.lastIDs = append(s.lastIDs, r.Header.Get("Last-Event-ID"))
		s.queries = append(s.queries, r.URL.RawQuery)
		n := len(s.lastIDs) - 1
		s.mu.Unlock()
		s.script(n, w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sseServer) conns() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lastIDs...), append([]string(nil), s.queries...)
}

func (s *sseServer) client(t *testing.T) *Client {
	c, kerr := NewClient(ClientOpts{BaseURL: s.srv.URL, ProjectSlug: "portal", APIKey: "k"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	return c
}

// cellFrame is a rollup frame in the gateway's real shape. The SSE id is the
// resume cursor ("seq_" + eventID), which is not the eventId.
func cellFrame(eventID, payload string) string {
	return fmt.Sprintf("id: seq_%s\nevent: cell\ndata: {\"topic\":\"rollups.sales_by_city\",\"eventType\":\"cell\",\"payload\":%s,\"occurredAt\":\"2026-10-27T19:40:00.000Z\",\"eventId\":\"%s\"}\n\n", eventID, payload, eventID)
}

const (
	cellFinal      = `{"cellKey":"c1","bucket":"2026-10-27T19:00:00.000Z","dims":{"city":"Berlin"},"final":true,"measures":{"orders":3}}`
	cellSuppressed = `{"cellKey":"c2","bucket":"2026-10-27T19:00:00.000Z","dims":{},"final":true,"status":"suppressed","reason":"cohort_below_k"}`
)

func TestRollupsSubscribe(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/@portal/rollups/sales_by_city/subscribe" {
			t.Errorf("path: %s", r.URL.Path)
		}
		switch n {
		case 0:
			sendSSE(w, ": hb\n\n"+cellFrame("evt_1", cellFinal)+cellFrame("evt_2", cellSuppressed))
		default:
			// a resume re-sends the last seconds: evt_2 again
			sendSSE(w, cellFrame("evt_2", cellSuppressed)+cellFrame("evt_3", `{"cellKey":"c3","bucket":"b","dims":{"city":"San Francisco"},"final":true,"measures":{"orders":1}}`))
			<-r.Context().Done()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	from := int64(7)
	events, errs := srv.client(t).Rollups.Subscribe(ctx, "sales_by_city", RollupSubscribeOptions{FromSequence: &from, retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second})
	var got []RollupCellEvent
	for len(got) < 3 {
		select {
		case ev := <-events:
			got = append(got, ev)
		case kerr := <-errs:
			t.Fatalf("error: %v", kerr)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out")
		}
	}
	if got[0].EventID != "evt_1" || got[1].EventID != "evt_2" || got[2].EventID != "evt_3" {
		t.Fatalf("ids (duplicate must be dropped): %v %v %v", got[0].EventID, got[1].EventID, got[2].EventID)
	}
	c := got[0]
	if c.Name != "cell" || c.Topic != "rollups.sales_by_city" || c.CellKey != "c1" || c.Bucket != "2026-10-27T19:00:00.000Z" || c.Dims["city"] != "Berlin" || !c.Final ||
		c.Measures["orders"] != float64(3) || c.Suppressed() || c.OccurredAt != "2026-10-27T19:40:00.000Z" || !strings.Contains(string(c.Data), "Berlin") {
		t.Fatalf("cell: %+v", c)
	}
	sp := got[1]
	if !sp.Suppressed() || sp.Status != "suppressed" || sp.Reason != "cohort_below_k" || len(sp.Measures) != 0 || sp.CellKey != "c2" {
		t.Fatalf("suppressed: %+v", sp)
	}
	last, queries := srv.conns()
	if last[0] != "" || last[1] != "seq_evt_2" {
		t.Fatalf("Last-Event-ID is the SSE cursor: %v", last)
	}
	if queries[0] != "fromSequence=7" || queries[1] != "" {
		t.Fatalf("fromSequence is for the first connection only: %v", queries)
	}
}

func TestRollupsSubscribeResyncRereadSource(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			sendSSE(w, cellFrame("evt_1", cellFinal)+"event: resync\ndata: {\"reason\":\"cursor-unknown\",\"action\":\"reread-source\"}\n\n")
			return
		}
		sendSSE(w, cellFrame("evt_9", cellFinal))
		<-r.Context().Done()
	})
	var info ResyncInfo
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := srv.client(t).Rollups.Subscribe(ctx, "sales_by_city", RollupSubscribeOptions{retryDelay: 5 * time.Millisecond, idleTimeout: 2 * time.Second,
		OnResync: func(_ context.Context, i ResyncInfo) { info = i }})
	for _, want := range []string{"evt_1", "evt_9"} {
		select {
		case ev := <-events:
			if ev.EventID != want {
				t.Fatalf("%s != %s", ev.EventID, want)
			}
		case kerr := <-errs:
			t.Fatal(kerr)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out")
		}
	}
	if info.Action != "reread-source" || info.Reason != "cursor-unknown" {
		t.Fatalf("OnResync: %+v", info)
	}
	if last, _ := srv.conns(); len(last) != 2 || last[1] != "" {
		t.Fatalf("a rollup resync starts fresh: %v", last)
	}
}

func TestRollupsSubscribeErrors(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]any{"code": "K9O_SUBSCRIPTION_LIMIT_EXCEEDED", "message": "too many streams"})
	})
	events, errs := srv.client(t).Rollups.Subscribe(context.Background(), "r", RollupSubscribeOptions{})
	if kerr := <-errs; kerr == nil || kerr.Code != "K9O_SUBSCRIPTION_LIMIT_EXCEEDED" || kerr.Status != 429 {
		t.Fatalf("429: %v", kerr)
	}
	if _, open := <-events; open {
		t.Fatal("events must close")
	}
	neg := int64(-1)
	_, errs = srv.client(t).Rollups.Subscribe(context.Background(), "r", RollupSubscribeOptions{FromSequence: &neg})
	if kerr := <-errs; kerr == nil || kerr.Code != "K9O_INVALID_FIELD_VALUE" {
		t.Fatalf("negative: %v", kerr)
	}
}
