package k9o

import (
	"context"
	"math"
	"strings"
	"testing"
)

var nearestLive = map[string]any{
	"data": map[string]any{
		"results": []any{
			map[string]any{"id": "p1", "score": 1, "_source": map[string]any{"handle": "probe", "chunkIndex": 0}, "contentHash": nil, "text": "x axis", "metadata": map[string]any{"lang": "en"}, "model": nil, "chunk": 0},
			map[string]any{"id": "p2", "score": 0.8, "_source": map[string]any{"handle": "probe", "chunkIndex": 1}, "contentHash": "h2", "text": nil, "metadata": map[string]any{"model": "m1"}, "model": "m1", "chunk": 1},
		},
		"collection": "_vec_example",
	},
	"meta": map[string]any{},
}

func TestVectorsNearestWire(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, nearestLive})

	res, _, kerr := c.Vectors.Nearest(ctx, "_vec_example", []float64{1, 0, 0}, NearestOptions{TopK: 2})
	if kerr != nil || res.Collection != "_vec_example" || len(res.Results) != 2 {
		t.Fatalf("Nearest: %+v %v", res, kerr)
	}
	wantCall(t, "nearest", calls[0], "POST", "/@portal/vectors/_vec_example/search", map[string]string{}, `{"queryVector":[1,0,0],"topK":2}`)
	a, b := res.Results[0], res.Results[1]
	if a.ID != "p1" || a.Score != 1 || a.ContentHash != nil || *a.Text != "x axis" || a.Model != nil || *a.Chunk != 0 || a.Source.Handle != "probe" || a.Metadata["lang"] != "en" {
		t.Fatalf("result 0: %+v", a)
	}
	if b.ID != "p2" || b.Score != 0.8 || *b.ContentHash != "h2" || b.Text != nil || *b.Model != "m1" || *b.Chunk != 1 || b.Source.ChunkIndex != 1 {
		t.Fatalf("result 1: %+v", b)
	}

	calls = nil
	minScore := 0.5
	c.Vectors.Nearest(ctx, "_vec_example", []float64{1, 0.5}, NearestOptions{TopK: 5, MinScore: &minScore, Metric: "dot", Filters: map[string]any{"metadata.lang": "en"}})
	wantCall(t, "nearest full", calls[0], "POST", "/@portal/vectors/_vec_example/search", map[string]string{},
		`{"queryVector":[1,0.5],"topK":5,"minScore":0.5,"metric":"dot","filters":{"metadata.lang":"en"}}`)

	calls = nil
	zero := 0.0
	c.Vectors.Nearest(ctx, "_vec_example", []float64{1}, NearestOptions{MinScore: &zero})
	wantCall(t, "nearest zero minScore", calls[0], "POST", "/@portal/vectors/_vec_example/search", map[string]string{}, `{"queryVector":[1],"minScore":0}`)
}

func TestVectorsUpsertWire(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{200, map[string]any{"data": map[string]any{"upserted": 3, "collection": "_vec_example"}, "meta": map[string]any{}}})

	res, _, kerr := c.Vectors.Upsert(ctx, "_vec_example", []VectorRow{
		{ID: "go-1", Vector: []float64{0, 0, 1}, Source: VectorSource{Handle: "probe", ChunkIndex: 0}, SubjectIDs: []string{"eu_YmGE3Z6nkv5i"}},
		{Vector: []float64{1, 0, 0}, Source: VectorSource{Handle: "probe", ChunkIndex: 1}, ContentHash: "h", Text: "x", Metadata: map[string]any{"lang": "en"}},
	})
	if kerr != nil || res.Upserted != 3 || res.Collection != "_vec_example" {
		t.Fatalf("Upsert: %+v %v", res, kerr)
	}
	wantCall(t, "upsert", calls[0], "POST", "/@portal/vectors/_vec_example/upsert", map[string]string{},
		`{"rows":[{"id":"go-1","vector":[0,0,1],"_source":{"handle":"probe","chunkIndex":0},"subjectIds":["eu_YmGE3Z6nkv5i"]},`+
			`{"vector":[1,0,0],"_source":{"handle":"probe","chunkIndex":1},"contentHash":"h","text":"x","metadata":{"lang":"en"}}]}`)
}

func TestVectorsValidationSendsNothing(t *testing.T) {
	ctx := context.Background()
	row := VectorRow{Vector: []float64{1}, Source: VectorSource{Handle: "h"}}
	many := make([]VectorRow, 1001)
	for i := range many {
		many[i] = row
	}
	nan := math.NaN()
	cases := map[string]func(c *Client) *KError{
		"nearest collection without underscore": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "notes", []float64{1}, NearestOptions{})
			return k
		},
		"nearest empty vector": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", nil, NearestOptions{})
			return k
		},
		"nearest vector too long": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", make([]float64, 16001), NearestOptions{})
			return k
		},
		"nearest NaN": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{1, nan}, NearestOptions{})
			return k
		},
		"nearest Inf": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{math.Inf(1)}, NearestOptions{})
			return k
		},
		"nearest topK negative": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{1}, NearestOptions{TopK: -1})
			return k
		},
		"nearest topK above 1000": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{1}, NearestOptions{TopK: 1001})
			return k
		},
		"nearest bad metric": func(c *Client) *KError {
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{1}, NearestOptions{Metric: "manhattan"})
			return k
		},
		"nearest NaN minScore": func(c *Client) *KError {
			m := nan
			_, _, k := c.Vectors.Nearest(ctx, "_n", []float64{1}, NearestOptions{MinScore: &m})
			return k
		},
		"upsert collection without underscore": func(c *Client) *KError {
			_, _, k := c.Vectors.Upsert(ctx, "notes", []VectorRow{row})
			return k
		},
		"upsert no rows": func(c *Client) *KError {
			_, _, k := c.Vectors.Upsert(ctx, "_n", nil)
			return k
		},
		"upsert 1001 rows": func(c *Client) *KError {
			_, _, k := c.Vectors.Upsert(ctx, "_n", many)
			return k
		},
		"upsert row with empty vector": func(c *Client) *KError {
			_, _, k := c.Vectors.Upsert(ctx, "_n", []VectorRow{row, {Source: VectorSource{Handle: "h"}}})
			return k
		},
		"upsert row with NaN": func(c *Client) *KError {
			_, _, k := c.Vectors.Upsert(ctx, "_n", []VectorRow{{Vector: []float64{nan}}})
			return k
		},
	}
	for name, call := range cases {
		var calls []streamsCall
		c := workloadClient(t, &calls, fakeReply{200, nearestLive})
		wantInvalid(t, name, call(c), calls)
	}
}

func TestVectorsErrorsDecode(t *testing.T) {
	ctx := context.Background()
	var calls []streamsCall
	c := workloadClient(t, &calls, fakeReply{404, map[string]any{
		"code": "K9O_COLLECTION_NOT_FOUND", "message": "derived vector collection '_vec_example' does not exist", "context": map[string]any{},
	}})
	_, _, kerr := c.Vectors.Nearest(ctx, "_vec_example", []float64{1, 0, 0}, NearestOptions{})
	if kerr == nil || kerr.Code != "K9O_COLLECTION_NOT_FOUND" || !strings.Contains(kerr.Message, "does not exist") {
		t.Fatalf("404: %v", kerr)
	}

	calls = nil
	c = workloadClient(t, &calls, fakeReply{400, map[string]any{
		"code": "K9O_INVALID_VECTOR_DIMENSION", "message": "query vector dimension does not match collection '_vec_example'",
		"context": map[string]any{"collection": "_vec_example", "queryDim": 2},
	}})
	_, _, kerr = c.Vectors.Nearest(ctx, "_vec_example", []float64{1, 0}, NearestOptions{})
	if kerr == nil || kerr.Code != "K9O_INVALID_VECTOR_DIMENSION" || kerr.Context["collection"] != "_vec_example" || kerr.Context["queryDim"] != float64(2) {
		t.Fatalf("400: %+v", kerr)
	}
}

func TestVectorsRequireProjectSlug(t *testing.T) {
	c, kerr := NewClient(ClientOpts{BaseURL: "https://acme.api.k9o.io"})
	if kerr != nil {
		t.Fatal(kerr)
	}
	if _, _, kerr := c.Vectors.Nearest(context.Background(), "_n", []float64{1}, NearestOptions{}); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("want K9O_MISSING_PROJECT_CONTEXT, got %v", kerr)
	}
}
