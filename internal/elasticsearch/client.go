package elasticsearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/AlectoTheFirst/uex/internal/config"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

const (
	AggregationName = "uex_aggregation"
)

// Client wraps the official Elasticsearch client and provides specific methods.
type Client struct {
	esClient *elasticsearch.Client
	Cfg      *config.ConnectionConfig
}

// NewClient creates and configures a new Elasticsearch client.
func NewClient(cfg *config.ConnectionConfig) (*Client, error) {
	esCfg := elasticsearch.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
		CloudID:   cfg.CloudID,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.InsecureSkipVerify,
			},
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: cfg.Timeout,
		},
	}

	// Log the username and password for debugging purposes
	slog.Debug("Elasticsearch client configuration", "username", cfg.Username, "password", cfg.Password)

	es, err := elasticsearch.NewClient(esCfg)
	if (err != nil) {
		return nil, fmt.Errorf("error creating Elasticsearch client: %w", err)
	}

	if cfg.HealthCheck {
		res, err := es.Ping()
		if err != nil {
			return nil, fmt.Errorf("error pinging Elasticsearch: %w", err)
		}
		defer res.Body.Close()
		if res.IsError() {
			return nil, fmt.Errorf("Elasticsearch ping failed: %s", res.String())
		}
		slog.Info("Successfully connected to Elasticsearch", "version", res.Header.Get("X-Elastic-Product-Version"))
	} else {
		slog.Info("Skipping Elasticsearch health check on startup")
	}

	return &Client{esClient: es, Cfg: cfg}, nil
}

// RunTermsQuery executes a specific terms aggregation query.
func (c *Client) RunTermsQuery(ctx context.Context, metricCfg *config.MetricConfig) (*SearchResponse, error) {
	queryJSON, err := buildQuery(metricCfg)
	if err != nil {
		return nil, fmt.Errorf("error building query for metric %q: %w", metricCfg.Name, err)
	}

	slog.Debug("Executing Elasticsearch query", "metric", metricCfg.Name, "query", string(queryJSON))

	searchReq := esapi.SearchRequest{
		Index:          metricCfg.Query.Indices,
		Body:           bytes.NewReader(queryJSON),
		Size:           esapi.IntPtr(0),
		TrackTotalHits: false,
		Timeout:        c.Cfg.Timeout,
	}

	res, err := searchReq.Do(ctx, c.esClient)
	if err != nil {
		return nil, fmt.Errorf("error executing search request for metric %q: %w", metricCfg.Name, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		var errorBody map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&errorBody); err == nil {
			return nil, fmt.Errorf("Elasticsearch query failed for metric %q: status=%s, error=%v", metricCfg.Name, res.Status(), errorBody)
		}
		return nil, fmt.Errorf("Elasticsearch query failed for metric %q: %s", metricCfg.Name, res.String())
	}

	var searchResp SearchResponse
	if err := json.NewDecoder(res.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("error decoding Elasticsearch response for metric %q: %w", metricCfg.Name, err)
	}

	if searchResp.TimedOut {
		slog.Warn("Elasticsearch query timed out", "metric", metricCfg.Name)
	}
	if searchResp.Shards.Failed > 0 {
		slog.Warn("Elasticsearch query encountered shard failures",
			"metric", metricCfg.Name,
			"total", searchResp.Shards.Total,
			"successful", searchResp.Shards.Successful,
			"skipped", searchResp.Shards.Skipped,
			"failed", searchResp.Shards.Failed,
		)
	}

	return &searchResp, nil
}

// Thorough fix for invalid composite literal element type `interface{}` in `buildQuery`
// Ensured all composite literals use explicitly defined types.

func buildQuery(metricCfg *config.MetricConfig) ([]byte, error) {
	queryPart := map[string]interface{}{}

	filters := []map[string]interface{}{}
	if metricCfg.Query.FilterQuery != "" {
		var fq map[string]interface{}
		if err := json.Unmarshal([]byte(metricCfg.Query.FilterQuery), &fq); err != nil {
			return nil, fmt.Errorf("invalid filter_query JSON: %w", err)
		}
		filters = append(filters, fq)
	}
	if metricCfg.Query.FilterQueryString != "" {
		filters = append(filters, map[string]interface{}{
			"query_string": map[string]interface{}{
				"query": metricCfg.Query.FilterQueryString,
			},
		})
	}

	if len(filters) > 0 {
		queryPart["query"] = map[string]interface{}{
			"bool": map[string]interface{}{
				"filter": filters,
			},
		}
	}

	termsAgg := map[string]interface{}{
		"field": metricCfg.Query.TermField,
		"size":  metricCfg.Query.Size,
	}

	if metricCfg.Query.Missing != nil {
		termsAgg["missing"] = *metricCfg.Query.Missing
	}

	if metricCfg.Query.MinDocCount != nil {
		termsAgg["min_doc_count"] = *metricCfg.Query.MinDocCount
	}

	aggsPart := map[string]interface{}{
		AggregationName: map[string]interface{}{
			"terms": termsAgg,
		},
	}

	fullQuery := map[string]interface{}{
		"aggs": aggsPart,
	}
	if query, exists := queryPart["query"]; exists {
		fullQuery["query"] = query
	}

	queryJSON, err := json.Marshal(fullQuery)
	if err != nil {
		return nil, fmt.Errorf("error marshalling query JSON: %w", err)
	}

	return queryJSON, nil
}

// GetBucketKeyAsString converts an aggregation bucket key to its string representation.
func GetBucketKeyAsString(key interface{}) string {
	switch v := key.(type) {
	case string:
		return v
	case float64: // JSON numbers are float64
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}