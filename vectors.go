package k9o

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxVectorLength = 16000
	maxVectorRows   = 1000
)

// VectorSource says where a vector came from: a source handle and the index
// of the chunk within it.
type VectorSource struct {
	Handle     string `json:"handle"`
	ChunkIndex int    `json:"chunkIndex"`
}

// NearestOptions tune Vectors.Nearest. Zero values mean the defaults: TopK 10,
// Metric "cosine", no score threshold and no filters.
type NearestOptions struct {
	// TopK is the number of results to return, 1 to 1000.
	TopK int `json:"topK,omitempty"`
	// MinScore keeps only results whose Score is at least this. Nil means no threshold.
	MinScore *float64 `json:"minScore,omitempty"`
	// Metric is "cosine" (default), "euclidean" or "dot".
	Metric string `json:"metric,omitempty"`
	// Filters are equality checks on "id", "contentHash", "source.<key>" and
	// "metadata.<key>"; every one must match. Values are a string, a number or a bool.
	Filters map[string]any `json:"filters,omitempty"`
}

// NearestVector is one search result.
type NearestVector struct {
	ID string `json:"id"`
	// Score is higher for a closer match. cosine: 1 minus the distance;
	// euclidean: 1 / (1 + distance); dot: the inner product.
	Score       float64        `json:"score"`
	Source      VectorSource   `json:"_source"`
	ContentHash *string        `json:"contentHash"`
	Text        *string        `json:"text"`
	Metadata    map[string]any `json:"metadata"`
	// Model is metadata.model, when set.
	Model *string `json:"model"`
	// Chunk is the source's chunk index, when set.
	Chunk *int `json:"chunk"`
}

// NearestResult is the answer to Vectors.Nearest.
type NearestResult struct {
	// Results are closest first.
	Results    []NearestVector `json:"results"`
	Collection string          `json:"collection"`
}

// VectorRow is one row for Vectors.Upsert.
type VectorRow struct {
	// ID is 1 to 256 characters. Empty means the gateway generates one.
	// Upserting an existing ID overwrites that row.
	ID string `json:"id,omitempty"`
	// Vector is 1 to 16000 numbers; every row in one request has the same length.
	Vector []float64    `json:"vector"`
	Source VectorSource `json:"_source"`
	// ContentHash is at most 128 characters.
	ContentHash string `json:"contentHash,omitempty"`
	// Text is at most 100000 characters. It is stored sealed when the row belongs to a subject.
	Text string `json:"text,omitempty"`
	// Metadata defaults to {}. Keep only values you filter on here; they are stored in plain text.
	Metadata map[string]any `json:"metadata,omitempty"`
	// SubjectIDs is the one end user the row belongs to (["eu_..."]). Required on a
	// per-subject collection (the default for a new collection); refused on a shared one.
	SubjectIDs []string `json:"subjectIds,omitempty"`
}

// UpsertResult is the answer to Vectors.Upsert.
type UpsertResult struct {
	Upserted   int    `json:"upserted"`
	Collection string `json:"collection"`
}

// VectorsNamespace exposes client.Vectors.*: collections of your own
// embeddings over /@<slug>/vectors/<collection>. Both calls send the vector
// in the request body, so they take real embeddings (up to 16000 numbers). A
// collection name must start with "_". The first Upsert creates the collection
// and fixes its dimension; searching one that was never upserted answers 404
// K9O_COLLECTION_NOT_FOUND.
type VectorsNamespace struct {
	config *httpClientConfig
}

func (n *VectorsNamespace) path(collection, tail string) (string, *KError) {
	if !strings.HasPrefix(collection, "_") {
		return "", invalidField("a vector collection name must start with _", map[string]any{"collection": collection})
	}
	slug, kerr := requireProjectSlug(n.config, "vectors")
	if kerr != nil {
		return "", kerr
	}
	return ProjectPath(slug, "vectors/"+url.PathEscape(collection)+"/"+tail), nil
}

func checkVector(vector []float64, name string) *KError {
	if len(vector) < 1 || len(vector) > maxVectorLength {
		return invalidField(name+" must be 1 to 16000 numbers", nil)
	}
	for _, v := range vector {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return invalidField(name+" must contain only finite numbers", nil)
		}
	}
	return nil
}

// Nearest returns the rows nearest to queryVector, closest first. It answers
// 404 K9O_COLLECTION_NOT_FOUND when the collection was never upserted and 400
// K9O_INVALID_VECTOR_DIMENSION when the query length differs from the stored one.
// An invalid argument is reported as K9O_INVALID_FIELD_VALUE before any request.
func (n *VectorsNamespace) Nearest(ctx context.Context, collection string, queryVector []float64, opts NearestOptions) (*NearestResult, *Meta, *KError) {
	p, kerr := n.path(collection, "search")
	if kerr != nil {
		return nil, nil, kerr
	}
	if kerr := checkVector(queryVector, "queryVector"); kerr != nil {
		return nil, nil, kerr
	}
	if opts.TopK != 0 && (opts.TopK < 1 || opts.TopK > 1000) {
		return nil, nil, invalidField("topK must be a whole number from 1 to 1000", map[string]any{"topK": opts.TopK})
	}
	if opts.MinScore != nil && (math.IsNaN(*opts.MinScore) || math.IsInf(*opts.MinScore, 0)) {
		return nil, nil, invalidField("minScore must be a number", nil)
	}
	if opts.Metric != "" && opts.Metric != "cosine" && opts.Metric != "euclidean" && opts.Metric != "dot" {
		return nil, nil, invalidField("metric must be one of: cosine, euclidean, dot", map[string]any{"metric": opts.Metric})
	}
	body := struct {
		QueryVector []float64 `json:"queryVector"`
		NearestOptions
	}{queryVector, opts}
	env, kerr := doRequest[NearestResult](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: body})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}

// Upsert inserts or overwrites 1 to 1000 rows in one request. The first call
// creates the collection. Keep each request under the 2 MiB body limit.
func (n *VectorsNamespace) Upsert(ctx context.Context, collection string, rows []VectorRow) (*UpsertResult, *Meta, *KError) {
	p, kerr := n.path(collection, "upsert")
	if kerr != nil {
		return nil, nil, kerr
	}
	if len(rows) < 1 || len(rows) > maxVectorRows {
		return nil, nil, invalidField("rows must be 1 to 1000 rows", nil)
	}
	for i, row := range rows {
		if kerr := checkVector(row.Vector, "rows["+strconv.Itoa(i)+"].vector"); kerr != nil {
			return nil, nil, kerr
		}
	}
	env, kerr := doRequest[UpsertResult](ctx, n.config, &requestInit{Method: "POST", Path: p, Body: map[string]any{"rows": rows}})
	if kerr != nil {
		return nil, nil, kerr
	}
	return &env.Data, &env.Meta, nil
}
