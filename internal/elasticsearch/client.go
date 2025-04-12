package elasticsearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/AlectoTheFirst/uex/internal/config"
)

const (
	// AggregationNameForKey is the key used within the Elasticsearch query's "aggs" section
	// for the primary terms aggregation requested by the exporter.
	AggregationNameForKey = "uex_term_aggregation"
	// TopHitsAggregationNameForKey is the key for the nested top_hits aggregation
	// used to retrieve source fields within buckets.
	TopHitsAggregationNameForKey = "source_doc"
)

// Client wraps the official Elasticsearch client.
type Client struct {
	esClient *elasticsearch.Client
	Cfg      *config.ConnectionConfig
}

// NewClient creates and configures a new Elasticsearch client wrapper.
func NewClient(cfg *config.ConnectionConfig) (*Client, error) {
	// Basic validation is done in LoadConnectionConfig, assume cfg is usable here.
	logCfg := *cfg // Copy config for logging to avoid modifying original
	if logCfg.Password != "" {
		logCfg.Password = "[masked]"
	}
	if logCfg.APIKey != "" {
		logCfg.APIKey = "[masked]"
	}
	slog.Debug("Initializing Elasticsearch client with configuration", "config", logCfg)

	// Configure the underlying Elasticsearch client
	esCfg := elasticsearch.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
		CloudID:   cfg.CloudID,
		// Consider adding CACert loading logic if cfg.CACertPath is used
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.InsecureSkipVerify,
				// MinVersion: tls.VersionTLS12, // Consider setting minimum TLS version
			},
			MaxIdleConnsPerHost:   10, // Reasonable default
			ResponseHeaderTimeout: cfg.Timeout,
			// DialContext defaults are usually fine
		},
		// Consider enabling retry mechanism
		// RetryOnStatus: []int{502, 503, 504, 429},
		// MaxRetries: 3,
	}

	es, err := elasticsearch.NewClient(esCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating Elasticsearch client: %w", err)
	}
	slog.Info("Elasticsearch client created")

	client := &Client{esClient: es, Cfg: cfg}

	if cfg.HealthCheck {
		slog.Info("Performing Elasticsearch health check...")
		err := client.checkConnection(context.Background()) // Use a background context for startup check
		if err != nil {
			// Add more context to the error message
			return nil, fmt.Errorf("Elasticsearch health check failed: %w", err)
		}
		slog.Info("Elasticsearch health check successful")
	} else {
		slog.Info("Skipping Elasticsearch health check on startup")
	}

	return client, nil
}

// checkConnection performs a basic check to ensure connectivity and authentication work.
func (c *Client) checkConnection(ctx context.Context) error {
	// Use a basic search query with size=0 as a connectivity check.
	var sizeZero int = 0

	req := esapi.SearchRequest{
		Index:          []string{},
		Size:           &sizeZero, // Size is *int, so pointer is correct here
		TrackTotalHits: false,     // Pass boolean directly
		Timeout:        c.Cfg.Timeout,
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.Cfg.Timeout)
	defer cancel()

	res, err := req.Do(reqCtx, c.esClient)
	if err != nil {
		// Provide more context for network-level errors
		return fmt.Errorf("connectivity check request execution failed: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		// Attempt to decode the error body for more specific info
		var errResp SearchResponse // Use SearchResponse to potentially capture ES error structure
		bodyBytes, _ := io.ReadAll(res.Body) // Read body for detailed error message
		// Try unmarshaling into our error structure
		_ = json.Unmarshal(bodyBytes, &errResp) // Ignore unmarshal error, focus on HTTP status and body

		// Construct detailed error message
		errMsg := fmt.Sprintf("connectivity check failed: status=%s", res.Status())
		if errResp.Error != nil {
			errMsg = fmt.Sprintf("%s, error=%v", errMsg, errResp.Error)
		} else if len(bodyBytes) > 0 {
			// Fallback to raw response body if specific error structure not found/parsed
			errMsg = fmt.Sprintf("%s, response_body=%s", errMsg, string(bodyBytes))
		}
		return fmt.Errorf(errMsg)
	}

	slog.Debug("Elasticsearch connectivity check successful", "status", res.Status())
	return nil
}


// GetESClient returns the underlying raw Elasticsearch client if needed for advanced operations.
func (c *Client) GetESClient() *elasticsearch.Client {
	return c.esClient
}

// RunQuery executes a query defined by MetricConfig against Elasticsearch.
// It handles both terms aggregations and direct search queries.
func (c *Client) RunQuery(ctx context.Context, metricCfg *config.MetricConfig) (*SearchResponse, error) {
	queryJSON, err := buildQuery(metricCfg)
	if err != nil {
		return nil, fmt.Errorf("error building query for metric %q: %w", metricCfg.Name, err)
	}

	slog.Debug("Executing Elasticsearch query", "metric", metricCfg.Name, "query", string(queryJSON))

	// Use a context with the configured timeout for this specific request
	reqCtx, cancel := context.WithTimeout(ctx, c.Cfg.Timeout)
	defer cancel()

	// Determine base parameters for the search request
	searchReq := esapi.SearchRequest{
		Index:   metricCfg.Query.Indices,
		Body:    bytes.NewReader(queryJSON),
		Timeout: c.Cfg.Timeout, // Set timeout on the request itself
	}

	// Adjust parameters based on query type (aggregation vs direct search)
	if metricCfg.Query.TermField != "" {
		// For aggregations, we don't need hits, set size=0
		var sizeZero int = 0
		searchReq.Size = &sizeZero // Size is *int, pointer is correct
		searchReq.TrackTotalHits = false     // Pass boolean directly
	} else {
		// For direct searches, use configured size and track total hits
		var sizeVar int = metricCfg.Query.Size
		searchReq.Size = &sizeVar // Size is *int, pointer is correct
		searchReq.TrackTotalHits = true      // Pass boolean directly
	}


	res, err := searchReq.Do(reqCtx, c.esClient)
	if err != nil {
		// Network errors, context cancellation etc.
		return nil, fmt.Errorf("error executing search request for metric %q: %w", metricCfg.Name, err)
	}
	defer res.Body.Close() // Ensure body is always closed

	// Decode the response body into our struct
	var searchResp SearchResponse
	// Read the body first to allow re-reading in case of error decoding
	bodyBytes, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		slog.Error("Failed to read Elasticsearch response body", "metric", metricCfg.Name, "error", readErr, "status_code", res.StatusCode)
		// Proceed to check IsError based on status code, but response data is lost
	}

	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
			slog.Error("Failed to decode Elasticsearch response body", "metric", metricCfg.Name, "error", err, "status_code", res.StatusCode, "body", string(bodyBytes))
			// Fall through to check IsError based on status code, searchResp might be partially populated or zeroed
		}
	} else {
		slog.Warn("Elasticsearch response body was empty", "metric", metricCfg.Name, "status_code", res.StatusCode)
	}


	// Check for HTTP-level errors and errors reported in the response body
	if res.IsError() {
		errMsg := fmt.Sprintf("Elasticsearch query failed for metric %q: status=%s", metricCfg.Name, res.Status())
		if searchResp.Error != nil {
			// If we successfully decoded an error structure from ES
			errMsg = fmt.Sprintf("%s, error=%v", errMsg, searchResp.Error)
		} else if len(bodyBytes) > 0 {
			// Fallback to raw response body
			errMsg = fmt.Sprintf("%s, response_body=%s", errMsg, string(bodyBytes))
		}
		return nil, fmt.Errorf(errMsg)
	}

	// Log non-fatal issues like timeouts or shard failures
	if searchResp.TimedOut {
		slog.Warn("Elasticsearch query timed out", "metric", metricCfg.Name)
		// Continue processing partial results if available
	}
	if searchResp.Shards.Failed > 0 {
		slog.Warn("Elasticsearch query encountered shard failures",
			"metric", metricCfg.Name,
			"total", searchResp.Shards.Total,
			"successful", searchResp.Shards.Successful,
			"skipped", searchResp.Shards.Skipped,
			"failed", searchResp.Shards.Failed,
			// Consider logging detailed failures if needed: searchResp.Shards.Failures
		)
		// Continue processing partial results
	}

	slog.Debug("Elasticsearch query executed successfully", "metric", metricCfg.Name, "took_ms", searchResp.Took)
	return &searchResp, nil
}


// buildQuery constructs the Elasticsearch query body based on the MetricConfig.
func buildQuery(metricCfg *config.MetricConfig) ([]byte, error) {
	// --- Build the Filter part ("query" section in ES) ---
	var queryFilter map[string]interface{} // Represents the content of the "query" field in the final ES query
	if metricCfg.Query.FilterQuery != "" {
		// User provided raw JSON filter query
		slog.Debug("Using raw JSON filter_query", "metric", metricCfg.Name)
		// Use strings.NewReader here
		decoder := json.NewDecoder(strings.NewReader(metricCfg.Query.FilterQuery))
		decoder.UseNumber() // Preserve number precision
		if err := decoder.Decode(&queryFilter); err != nil {
			return nil, fmt.Errorf("invalid filter_query JSON for metric %q: %w", metricCfg.Name, err)
		}
	} else if metricCfg.Query.FilterQueryString != "" {
		// User provided Lucene query string filter
		slog.Debug("Using query_string filter_query_string", "metric", metricCfg.Name)
		queryFilter = map[string]interface{}{
			"query_string": map[string]interface{}{
				"query": metricCfg.Query.FilterQueryString,
				// Consider adding default_operator, analyze_wildcard etc. if needed
			},
		}
	} else {
		// No filter specified, use "match_all" by default if filtering is needed conceptually,
		// or simply omit the "query" part if no documents need filtering before aggregation/search.
		// Omitting "query" is equivalent to match_all for aggregations.
		slog.Debug("No filter query specified, effectively using match_all", "metric", metricCfg.Name)
		// queryFilter = map[string]interface{}{"match_all": map[string]interface{}{}} // Explicit match_all
	}


	// --- Start building the full query structure ---
	fullQuery := map[string]interface{}{}

	// Add the filter ("query" part) if it was defined
	if queryFilter != nil {
		fullQuery["query"] = queryFilter
	}

	// --- Determine if it's an Aggregation or Direct Search ---
	isAggregation := metricCfg.Query.TermField != ""

	if isAggregation {
		// --- Build Aggregation part ("aggs" section) ---
		slog.Debug("Building terms aggregation query", "metric", metricCfg.Name, "term_field", metricCfg.Query.TermField)

		// Configure the core terms aggregation
		termsAggConfig := map[string]interface{}{
			"field": metricCfg.Query.TermField,
			"size":  metricCfg.Query.Size,
		}
		if metricCfg.Query.Missing != nil {
			termsAggConfig["missing"] = *metricCfg.Query.Missing
		}
		if metricCfg.Query.MinDocCount != nil {
			termsAggConfig["min_doc_count"] = *metricCfg.Query.MinDocCount
		} else {
			// Explicitly set default min_doc_count if needed, ES defaults to 1 anyway
			// termsAggConfig["min_doc_count"] = config.DefaultMinDocCount
		}

		// Define the main aggregation structure using our constant name
		mainAgg := map[string]interface{}{
			"terms": termsAggConfig,
		}

		// Add nested top_hits sub-aggregation *if* source fields are requested
		if len(metricCfg.Query.SourceFields) > 0 {
			slog.Debug("Adding top_hits sub-aggregation for source fields", "metric", metricCfg.Name, "fields", metricCfg.Query.SourceFields)
			mainAgg["aggs"] = map[string]interface{}{
				TopHitsAggregationNameForKey: map[string]interface{}{
					"top_hits": map[string]interface{}{
						"size":    1, // Only need one hit per bucket to get source fields
						"_source": metricCfg.Query.SourceFields,
					},
				},
			}
		}

		// Add the complete aggregation definition to the main query body
		fullQuery["aggs"] = map[string]interface{}{
			AggregationNameForKey: mainAgg,
		}

		// For aggregation queries, we don't need the actual documents, only aggregations
		fullQuery["size"] = 0
		// fullQuery["_source"] = false // Can explicitly disable _source for the top-level hits part

	} else {
		// --- Configure Direct Search ---
		slog.Debug("Building direct search query", "metric", metricCfg.Name, "size", metricCfg.Query.Size)
		// Size is set at the top level via searchReq fields in RunQuery

		// Include only the specified source fields if configured
		if len(metricCfg.Query.SourceFields) > 0 {
			slog.Debug("Requesting specific source fields", "metric", metricCfg.Name, "fields", metricCfg.Query.SourceFields)
			fullQuery["_source"] = metricCfg.Query.SourceFields
		} else {
			// Request all source fields (default ES behavior) or explicitly disable if desired
			// fullQuery["_source"] = true // Default
		}
	}

	// Marshal the complete query structure to JSON
	queryJSON, err := json.Marshal(fullQuery)
	if err != nil {
		return nil, fmt.Errorf("error marshalling query JSON for metric %q: %w", metricCfg.Name, err)
	}

	return queryJSON, nil
}

// GetBucketKeyAsString safely converts an aggregation bucket key (interface{}) to its string representation.
func GetBucketKeyAsString(key interface{}) string {
	if key == nil {
		// How should null keys be represented? Prometheus labels cannot be empty.
		// Maybe use a specific placeholder?
		return "null_key" // Or "<nil>" or other indicator
	}
	switch v := key.(type) {
	case string:
		return v
	case float64: // JSON numbers often unmarshal as float64
		// Check if it's actually an integer to avoid ".0" suffix
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int, int8, int16, int32, int64:
		// Handle various integer types explicitly if necessary
		// Use fmt.Sprintf for simplicity if direct strconv isn't needed
		return fmt.Sprintf("%d", v)
		// return strconv.FormatInt(int64(v), 10) // More performant if only int64
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
		// return strconv.FormatUint(uint64(v), 10) // More performant if only uint64
	case bool:
		return strconv.FormatBool(v)
	case json.Number: // Handles numbers precisely if UseNumber() was used in decoder
		return v.String()
	default:
		// Fallback for unexpected types - use standard string representation
		slog.Debug("Unexpected bucket key type", "type", fmt.Sprintf("%T", v), "value", v)
		return fmt.Sprintf("%v", v)
	}
}