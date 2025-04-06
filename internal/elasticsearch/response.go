package elasticsearch

import (
	"encoding/json"
)

// AggregationBucket represents a single bucket in a terms aggregation result.
type AggregationBucket struct {
	Key      interface{} `json:"key"`
	DocCount int64       `json:"doc_count"`
}

// TermsAggregationResult represents the result of a terms aggregation.
type TermsAggregationResult struct {
	DocCountErrorUpperBound int                 `json:"doc_count_error_upper_bound"`
	SumOtherDocCount        int64               `json:"sum_other_doc_count"`
	Buckets                 []AggregationBucket `json:"buckets"`
}

// Aggregations represents the "aggregations" part of an Elasticsearch response.
type Aggregations map[string]json.RawMessage

// SearchResponse represents the relevant parts of an Elasticsearch search response.
type SearchResponse struct {
	Took         int          `json:"took"`
	TimedOut     bool         `json:"timed_out"`
	Shards       ShardsInfo   `json:"_shards"`
	Hits         HitsInfo     `json:"hits"`
	Aggregations Aggregations `json:"aggregations"`
}

// ShardsInfo provides information about the shards involved in the search.
type ShardsInfo struct {
	Total      int `json:"total"`
	Successful int `json:"successful"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
}

// HitsInfo provides basic information about the search hits.
type HitsInfo struct {
	Total    TotalHits `json:"total"`
	MaxScore float64   `json:"max_score"`
}

// TotalHits holds the total number of documents matching the query.
type TotalHits struct {
	Value    int64  `json:"value"`
	Relation string `json:"relation"`
}