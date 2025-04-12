package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/AlectoTheFirst/uex/internal/config"
	internal_es "github.com/AlectoTheFirst/uex/internal/elasticsearch" 
)

const (
	namespace = "uex" // Prometheus metric namespace
)

// Exporter collects Elasticsearch metrics based on configured queries.
type Exporter struct {
	client          *internal_es.Client
	metrics         []config.MetricConfig
	descs           map[string]*prometheus.Desc
	staticLabelKeys map[string][]string
	
	// Internal metrics
	upDesc          *prometheus.Desc
	errorDesc       *prometheus.Desc
	scrapeDurDesc   *prometheus.Desc
	esTookDesc      *prometheus.Desc
	
	// Add this field
	overallSuccess  float64
	
	// Mutex specifically for updating the shared overallSuccess variable
	successMutex    sync.Mutex
}

// NewExporter creates, configures, and returns a new Exporter.
func NewExporter(client *internal_es.Client, metrics []config.MetricConfig) (*Exporter, error) {
	if client == nil {
		return nil, fmt.Errorf("elasticsearch client cannot be nil")
	}

	descs := make(map[string]*prometheus.Desc)
	staticLabelKeysMap := make(map[string][]string)

	slog.Info("Initializing exporter for configured metrics", "count", len(metrics))

	for _, mc := range metrics {
		// Defensively copy metric config in case it's modified later? Usually not needed.
		// currentMetric := mc

		// Determine the label keys for this metric IN ORDER.
		// Order must match the order of label values supplied in Collect.
		var labelKeys []string

		// 1. Add Term Label Key (if this is a terms aggregation query)
		isAggregation := mc.Query.TermField != ""
		if isAggregation {
			if mc.Labels.TermLabel == "" {
				// This should have been caught by config validation, but double-check.
				return nil, fmt.Errorf("internal error: metric %q is aggregation but has no term_label", mc.Name)
			}
			labelKeys = append(labelKeys, mc.Labels.TermLabel)
			slog.Debug("Adding term label key", "metric", mc.Name, "label", mc.Labels.TermLabel)
		}

		// 2. Add Source Label Keys (in the order they appear in the config)
		for _, sourceLabelConf := range mc.Labels.SourceLabels {
			labelKeys = append(labelKeys, sourceLabelConf.PromLabel)
			slog.Debug("Adding source label key", "metric", mc.Name, "label", sourceLabelConf.PromLabel)
		}

		// 3. Add Static Label Keys (sorted alphabetically for consistent order)
		var sortedStaticKeys []string
		for k := range mc.Labels.Static {
			sortedStaticKeys = append(sortedStaticKeys, k)
		}
		sort.Strings(sortedStaticKeys) // Ensure consistent order
		labelKeys = append(labelKeys, sortedStaticKeys...)
		staticLabelKeysMap[mc.Name] = sortedStaticKeys // Store ordered keys for Collect
		slog.Debug("Adding static label keys", "metric", mc.Name, "labels", sortedStaticKeys)


		// Create the Prometheus Description
		fqName := prometheus.BuildFQName(namespace, "", mc.Name)
		desc := prometheus.NewDesc(
			fqName,
			mc.Help,
			labelKeys, // Variable labels determined above
			nil,       // No constant labels at the Desc level for this exporter
		)
		descs[mc.Name] = desc
		slog.Debug("Registered metric description", "fqName", fqName, "variable_labels", labelKeys)
	}

	// --- Create Descriptors for Internal Metrics ---
	upDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last scrape of Elasticsearch successful (all configured queries ran without error).",
		nil, nil,
	)
	errorDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "scrape_error"),
		"Indicates an error occurred during metric scraping for a specific query definition (1 = error, 0 = success).",
		[]string{"metric_name"}, nil, // Label to identify which query failed
	)
	scrapeDurDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "scrape_duration_seconds"),
		"Duration of the overall Elasticsearch exporter scrape operation.",
		nil, nil,
	)
	esTookDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "query_duration_seconds"),
		"Duration of the Elasticsearch query execution ('took' field in ms / 1000).",
		[]string{"metric_name"}, nil, // Label to identify which query's duration
	)


	return &Exporter{
		client:          client,
		metrics:         metrics,
		descs:           descs,
		staticLabelKeys: staticLabelKeysMap,
		upDesc:          upDesc,
		errorDesc:       errorDesc,
		scrapeDurDesc:   scrapeDurDesc,
		esTookDesc:      esTookDesc,
	}, nil
}

// Describe implements prometheus.Collector. It sends descriptors of all possible metrics
// collected by this exporter to the channel ch.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// Describe internal metrics
	ch <- e.upDesc
	ch <- e.errorDesc
	ch <- e.scrapeDurDesc
	ch <- e.esTookDesc

	// Describe metrics derived from configuration
	for _, desc := range e.descs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector. It fetches data from Elasticsearch for each
// configured query and sends the resulting metrics to the channel ch.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	scrapeTimeStart := time.Now()
	// Initialize the struct field
	e.overallSuccess = 1.0

	// Use a context for the entire scrape operation.
	// Base timeout on ES client config, slightly less than Prometheus scrape timeout.
	scrapeTimeout := e.client.Cfg.Timeout
	// Example: Reduce timeout slightly to allow exporter logic time before Prometheus timeout hits
	if scrapeTimeout > 500*time.Millisecond {
		scrapeTimeout -= 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	slog.Info("Starting metric collection scrape")

	var wg sync.WaitGroup
	wg.Add(len(e.metrics)) // Add count for all metrics routines

	for i := range e.metrics {
		// Capture loop variable for the goroutine closure
		metricConf := e.metrics[i]

		go func(mc config.MetricConfig) {
			defer wg.Done() // Signal completion for this goroutine

			collectStartTime := time.Now()
			var scrapeErr float64 = 0.0 // 0 = success for this specific metric config
			var tookMs int = 0

			// Get the precomputed description for this metric
			metricDesc, ok := e.descs[mc.Name]
			if !ok {
				slog.Error("Internal error: Metric description not found during Collect. This should not happen.", "metric", mc.Name)
				scrapeErr = 1.0
				// Update overall success safely
				e.successMutex.Lock()
				e.overallSuccess = 0.0
				e.successMutex.Unlock()
				// Still report the scrape error for this metric below
			}

			slog.Debug("Collecting metric", "name", mc.Name)
			resp, err := e.client.RunQuery(ctx, &mc) // Use RunQuery which handles both types

			if err != nil {
				slog.Error("Failed to run query or process response for metric", "metric", mc.Name, "error", err)
				scrapeErr = 1.0
				e.successMutex.Lock()
				e.overallSuccess = 0.0
				e.successMutex.Unlock()
				// No return here, report the error metric below
			} else {
				// Query succeeded, record 'took' time from response
				tookMs = resp.Took
			}

			// --- Report per-metric scrape status and duration ---
			// Report scrape error status (0 or 1) for this specific metric config
			ch <- prometheus.MustNewConstMetric(e.errorDesc, prometheus.GaugeValue, scrapeErr, mc.Name)
			// Report Elasticsearch 'took' time if query was successful
			if err == nil && resp != nil {
				ch <- prometheus.MustNewConstMetric(e.esTookDesc, prometheus.GaugeValue, float64(tookMs)/1000.0, mc.Name)
			}


			// --- Process results and generate metrics ONLY if query succeeded ---
			if err == nil && resp != nil {
				isAggregation := mc.Query.TermField != ""

				if isAggregation {
					e.processAggregationResult(ch, mc, metricDesc, resp)
				} else {
					e.processDirectSearchResult(ch, mc, metricDesc, resp)
				}
			}
			slog.Debug("Finished collecting for metric", "name", mc.Name, "duration_ms", time.Since(collectStartTime).Milliseconds(), "error", scrapeErr > 0.5)

		}(metricConf) // Pass captured metricConf to goroutine
	}

	// Wait for all metric collection goroutines to finish
	wg.Wait()

	// --- Report overall scrape status and duration ---
	ch <- prometheus.MustNewConstMetric(e.upDesc, prometheus.GaugeValue, e.overallSuccess)
	totalScrapeDuration := time.Since(scrapeTimeStart)
	ch <- prometheus.MustNewConstMetric(e.scrapeDurDesc, prometheus.GaugeValue, totalScrapeDuration.Seconds())

	slog.Info("Finished scraping all metrics", "duration_seconds", totalScrapeDuration.Seconds(), "overall_success", e.overallSuccess > 0.5)
}


// processAggregationResult handles the response from a terms aggregation query.
func (e *Exporter) processAggregationResult(ch chan<- prometheus.Metric, mc config.MetricConfig, desc *prometheus.Desc, resp *internal_es.SearchResponse) {
	// Find our specific aggregation in the response using the constant key
	aggRaw, exists := resp.Aggregations[internal_es.AggregationNameForKey]
	if !exists {
		slog.Warn("Aggregation result key not found in response, perhaps no matching documents?",
			"metric", mc.Name,
			"expected_key", internal_es.AggregationNameForKey)
		return // No aggregation data to process
	}

	// Unmarshal the raw JSON into our specific TermsAggregationResult struct.
	// This struct's Buckets field is []AggregationBucket, which includes SourceDoc.
	var termsResult internal_es.TermsAggregationResult
	if err := json.Unmarshal(aggRaw, &termsResult); err != nil {
		slog.Error("Failed to unmarshal terms aggregation result", "metric", mc.Name, "error", err)
		// Mark overall scrape as failed? Could argue query succeeded but parsing failed.
		e.successMutex.Lock()
		e.overallSuccess = 0.0
		e.successMutex.Unlock()
		return
	}

	slog.Debug("Processing aggregation buckets for metric", "name", mc.Name, "bucket_count", len(termsResult.Buckets))

	// Get the ordered list of static label keys once
	orderedStaticKeys := e.staticLabelKeys[mc.Name]

	// Process each bucket
	for _, bucket := range termsResult.Buckets {
		// Construct label values IN THE CORRECT ORDER: Term -> Source -> Static

		// 1. Term Label Value
		termValue := internal_es.GetBucketKeyAsString(bucket.Key)
		labelValues := []string{termValue} // Start with term value

		// 2. Source Label Values
		sourceLabelsValues := make([]string, len(mc.Labels.SourceLabels)) // Pre-allocate slice
		// Extract source fields from the nested SourceDoc if it exists and fields were requested
		if len(mc.Query.SourceFields) > 0 && len(mc.Labels.SourceLabels) > 0 && bucket.SourceDoc != nil && len(bucket.SourceDoc.Hits.Hits) > 0 {
			// Get the source map from the first (and only) hit inside the bucket's SourceDoc
			sourceData := bucket.SourceDoc.Hits.Hits[0].Source
			extractAndPopulateSourceFields(sourceData, mc, sourceLabelsValues, termValue) // Pass termValue as identifier for logging
		} else {
			// Populate with empty strings if no source fields requested, no labels configured, or no hit found
			for i := range sourceLabelsValues {
				sourceLabelsValues[i] = ""
			}
			if len(mc.Labels.SourceLabels) > 0 {
			    slog.Debug("No source data found or requested for bucket", "metric", mc.Name, "term", termValue)
			}
		}
		labelValues = append(labelValues, sourceLabelsValues...) // Append source values

		// 3. Static Label Values (in the pre-sorted order)
		staticLabelValues := make([]string, len(orderedStaticKeys))
		for i, k := range orderedStaticKeys {
			// Apply dynamic logic for specific keys like 'time_window' if needed
			// WARNING: This dynamic logic is potentially inefficient and brittle. Prefer truly static labels.
			val := mc.Labels.Static[k]
			if k == "time_window" {
				// Try to extract from filter_query JSON (example, adapt as needed)
				if gw := getGteFromFilterQuery(mc.Query.FilterQuery); gw != "" {
					val = gw
				}
			}
			staticLabelValues[i] = val
		}
		labelValues = append(labelValues, staticLabelValues...) // Append static values


		// Create and send the metric
		m, err := prometheus.NewConstMetric(
			desc,
			prometheus.GaugeValue,
			float64(bucket.DocCount),
			labelValues..., // Expand slice to varargs
		)
		if err != nil {
			// This usually indicates a mismatch between label keys in Desc and label values provided
			slog.Error("Failed to create metric for aggregation bucket",
				"metric", mc.Name,
				"term", termValue,
				"label_keys_expected_count", len(labelValues), // Or however you're determining expected label count
				"label_values_provided_count", len(labelValues),
				"label_values", strings.Join(labelValues, ", "), // Log values for debugging
				"error", err)
			continue // Skip this bucket
		}
		ch <- m
		slog.Debug("Sent aggregation metric", "metric", mc.Name, "term", termValue, "value", bucket.DocCount)
	}

	// TODO: Optionally expose sum_other_doc_count? Needs a separate metric descriptor.
	// Example:
	// sumOtherDesc := prometheus.NewDesc(...)
	// ch <- prometheus.MustNewConstMetric(sumOtherDesc, prometheus.GaugeValue, float64(termsResult.SumOtherDocCount), ...)
}


// processDirectSearchResult handles the response from a direct search query (no aggregation).
func (e *Exporter) processDirectSearchResult(ch chan<- prometheus.Metric, mc config.MetricConfig, desc *prometheus.Desc, resp *internal_es.SearchResponse) {
	hitCount := len(resp.Hits.Hits)
	slog.Debug("Processing direct search hits for metric", "name", mc.Name, "hit_count", hitCount, "total_matches", resp.Hits.Total.Value)

	// Get the ordered list of static label keys once
	orderedStaticKeys := e.staticLabelKeys[mc.Name]

	// Instead of sending one metric per hit, aggregate counts by label value.
	aggregated := make(map[string]struct {
		labels []string
		count  float64
	})

	for _, hit := range resp.Hits.Hits {
		// 1. Source Label Values.
		sourceLabelsValues := make([]string, len(mc.Labels.SourceLabels)) // Pre-allocate.
		if len(mc.Labels.SourceLabels) > 0 && hit.Source != nil {
			extractAndPopulateSourceFields(hit.Source, mc, sourceLabelsValues, hit.ID)
		} else {
			for i := range sourceLabelsValues {
				sourceLabelsValues[i] = ""
			}
			if len(mc.Labels.SourceLabels) > 0 && hit.Source == nil {
				slog.Debug("Hit missing _source field", "metric", mc.Name, "hit_id", hit.ID)
			}
		}

		// 2. Static Label Values (in pre-sorted order).
		staticLabelValues := make([]string, len(orderedStaticKeys))
		for i, k := range orderedStaticKeys {
			val := mc.Labels.Static[k]
			if k == "time_window" {
				if gw := getGteFromFilterQuery(mc.Query.FilterQuery); gw != "" {
					val = gw
				}
			}
			staticLabelValues[i] = val
		}

		// Combine source and static label values.
		labelValues := append(sourceLabelsValues, staticLabelValues...)
		// Use a delimiter unlikely to appear in the labels.
		key := strings.Join(labelValues, "|")

		if entry, ok := aggregated[key]; ok {
			entry.count++
			aggregated[key] = entry
		} else {
			aggregated[key] = struct {
				labels []string
				count  float64
			}{labels: labelValues, count: 1.0}
		}
	}

	// Emit aggregated metrics.
	for _, entry := range aggregated {
		m, err := prometheus.NewConstMetric(
			desc,
			prometheus.GaugeValue,
			entry.count,
			entry.labels...,
		)
		if err != nil {
			slog.Error("Failed to create aggregated direct search metric", "metric", mc.Name, "error", err, "labels", entry.labels)
			continue
		}
		ch <- m
		slog.Debug("Sent aggregated direct search metric", "metric", mc.Name, "labels", entry.labels, "count", entry.count)
	}
}


// --- Helper Functions ---

// extractAndPopulateSourceFields extracts data from the sourceDoc based on configured
// source labels and populates the target sourceLabelsValues slice.
// The `idValue` is used purely for logging context (e.g., term value or document ID).
func extractAndPopulateSourceFields(sourceDoc map[string]interface{}, mc config.MetricConfig, sourceLabelsValues []string, idValue string) {
    if sourceDoc == nil {
        slog.Debug("Source document map is nil, cannot extract source labels", "metric", mc.Name, "id", idValue)
        // Ensure slice elements are empty (should be already from initialization)
        for i := range sourceLabelsValues { sourceLabelsValues[i] = "" }
        return
    }

    for i, slConfig := range mc.Labels.SourceLabels {
        fieldName := slConfig.SourceField
        found := false
        var extractedValue interface{} // Use interface{} to handle different types

        // Try splitting by dot for nested fields first
        parts := strings.Split(fieldName, ".")
        if len(parts) > 1 {
            // Handle nested field traversal
            current := sourceDoc
            var currentVal interface{} = current // Start with the root map
            validPath := true
            for j, part := range parts {
                if currentMap, ok := currentVal.(map[string]interface{}); ok {
                    if val, exists := currentMap[part]; exists {
                        currentVal = val // Move deeper
                        if j == len(parts)-1 {
                            // Last part, this is our value
                            extractedValue = currentVal
                            found = true
                        }
                    } else {
                        validPath = false // Part not found
                        break
                    }
                } else {
                    validPath = false // Cannot traverse deeper, not a map
                    break
                }
            }
            if !validPath {
                found = false // Ensure found is false if path was invalid
            }
        } else {
            // Not nested, try direct access
            if val, exists := sourceDoc[fieldName]; exists {
                extractedValue = val
                found = true
            }
        }

        // Assign value if found, otherwise default to empty string
        if found && extractedValue != nil {
            // Convert extracted value to string for the label
            sourceLabelsValues[i] = fmt.Sprintf("%v", extractedValue)
            slog.Debug("Extracted source field value", "metric", mc.Name, "id", idValue, "field", fieldName, "value", sourceLabelsValues[i])
        } else {
            sourceLabelsValues[i] = "" // Set empty string if field not found or value is nil
            slog.Debug("Source field not found or nil in document", "metric", mc.Name, "id", idValue, "field", fieldName)
        }
    }
}

// getGteFromFilterQuery is a helper for the dynamic 'time_window' static label.
// It attempts to parse the filter_query JSON and extract a 'gte' value from a range query on '@timestamp'.
// WARNING: This is fragile and inefficient. Consider alternatives.
func getGteFromFilterQuery(filterQueryJSON string) string {
    if filterQueryJSON == "" {
        return ""
    }
    
    var query map[string]interface{}
    if err := json.Unmarshal([]byte(filterQueryJSON), &query); err != nil {
        slog.Debug("Failed to parse filter_query JSON for dynamic time_window label", "error", err)
        return "" // Error parsing, return empty
    }
    
    // Example structure: {"range": {"@timestamp": {"gte": "now-1h"}}}
    if rangeQuery, ok := query["range"].(map[string]interface{}); ok {
        if timestampRange, ok := rangeQuery["@timestamp"].(map[string]interface{}); ok {
            if gteVal, ok := timestampRange["gte"].(string); ok {
                return gteVal
            }
        }
    }
    
    return "" // Structure not found or gte not a string
}

// Note: Removed marshalSourceFields helper as it wasn't used.