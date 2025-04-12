package elasticsearch

import (
	"encoding/json"
)

// --- Aggregation Structures ---

// AggregationBucket represents a single bucket in an aggregation result.
// It now includes an optional field to hold nested top_hits results for source fields.
type AggregationBucket struct {
	Key      interface{}            `json:"key"`       // The bucket key (string, number, bool, etc.)
	DocCount int64                  `json:"doc_count"` // Count of documents in the bucket
	// SourceDoc holds the result of the "source_doc" top_hits sub-aggregation, if requested.
	SourceDoc *TopHitsAggregationResult `json:"source_doc,omitempty"`
}

// TermsAggregationResult represents the primary result of a terms aggregation.
type TermsAggregationResult struct {
	DocCountErrorUpperBound int64               `json:"doc_count_error_upper_bound"`
	SumOtherDocCount        int64               `json:"sum_other_doc_count"`
	Buckets                 []AggregationBucket `json:"buckets"` // Changed to use AggregationBucket which includes SourceDoc
}

// TopHitsAggregationResult holds the results of a top_hits sub-aggregation.
type TopHitsAggregationResult struct {
	Hits HitsInfo `json:"hits"` // Embedded HitsInfo structure
}

// Aggregations represents the raw "aggregations" part of an Elasticsearch response.
// We use RawMessage to delay parsing until we know the specific aggregation type.
type Aggregations map[string]json.RawMessage

// --- General Search Response Structures ---

// SearchResponse represents the relevant parts of an Elasticsearch search response.
type SearchResponse struct {
	Took         int          `json:"took"`      // Time in milliseconds
	TimedOut     bool         `json:"timed_out"` // Whether the query timed out
	Shards       ShardsInfo   `json:"_shards"`   // Information about shard execution
	Hits         HitsInfo     `json:"hits"`      // Document hits (for direct searches)
	Aggregations Aggregations `json:"aggregations,omitempty"` // Aggregation results
	Error        interface{}  `json:"error,omitempty"` // Holds error details if the request failed at ES level
}

// ShardsInfo provides information about the shards involved in the search.
type ShardsInfo struct {
	Total      int `json:"total"`      // Total shards queried
	Successful int `json:"successful"` // Shards that succeeded
	Skipped    int `json:"skipped"`    // Shards that were skipped
	Failed     int `json:"failed"`     // Shards that failed
	// Failures   []ShardFailure `json:"failures,omitempty"` // Detailed failure info if needed
}

// HitsInfo provides information about the search hits (documents found).
type HitsInfo struct {
	Total    TotalHits `json:"total"`           // Total number of matching documents
	MaxScore *float64  `json:"max_score"`       // Maximum score of matching documents (can be null)
	Hits     []DocHit  `json:"hits"`            // The actual document hits
}

// TotalHits holds the total number of documents matching the query.
type TotalHits struct {
	Value    int64  `json:"value"`    // The total count
	Relation string `json:"relation"` // "eq" (exact) or "gte" (lower bound)
}

// DocHit represents a single document hit in the search results.
type DocHit struct {
	Index  string                 `json:"_index"`          // Index the document belongs to
	ID     string                 `json:"_id"`             // Document ID
	Score  *float64               `json:"_score"`          // Relevance score (can be null)
	Source map[string]interface{} `json:"_source"`         // The actual source document content
	// Fields map[string]interface{} `json:"fields,omitempty"` // If specific fields were requested via "fields" API
}