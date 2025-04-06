package exporter

import (
	"context"
	"encoding/json"
	"sync"
	"time"
    "log/slog"

	"github.com/prometheus/client_golang/prometheus"
	internal_es "github.com/AlectoTheFirst/uex/internal/elasticsearch"
	"github.com/AlectoTheFirst/uex/internal/config"
)

const (
	namespace = "elasticsearch_term" // Prometheus metric namespace
)

// Exporter collects Elasticsearch term aggregation metrics.
type Exporter struct {
	client      *internal_es.Client
	metrics     []config.MetricConfig
	descs       map[string]*prometheus.Desc // Map metric name to its precomputed Desc
	upDesc      *prometheus.Desc            // Exporter up metric
	errorDesc   *prometheus.Desc            // Exporter scrape error metric
	scrapeDurDesc *prometheus.Desc        // Exporter scrape duration metric
	esTookDesc  *prometheus.Desc            // Elasticsearch query took duration
	mutex       sync.Mutex                  // Protects scrape logic if needed (usually not for Collectors)
}

// NewExporter creates a new Exporter instance.
func NewExporter(client *internal_es.Client, metrics []config.MetricConfig) (*Exporter, error) {
	descs := make(map[string]*prometheus.Desc)

	for _, m := range metrics {
		labelKeys := []string{m.Labels.TermLabel} // Term label is always dynamic
		for k := range m.Labels.Static {
			labelKeys = append(labelKeys, k) // Add static label keys
		}

		fqName := prometheus.BuildFQName(namespace, "", m.Name)
		descs[m.Name] = prometheus.NewDesc(
			fqName,
			m.Help,
			labelKeys,
			nil, // No constant labels at the Desc level for this exporter
		)
		slog.Debug("Registered metric description", "name", fqName, "labels", labelKeys)
	}

	return &Exporter{
		client:  client,
		metrics: metrics,
		descs:   descs,
		upDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Was the last scrape of Elasticsearch successful.",
			nil, nil,
		),
		errorDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "scrape_error"),
			"Indicates an error occurred during metric scraping for a specific query definition (1 = error, 0 = success).",
			[]string{"metric_name"}, nil,
		),
        scrapeDurDesc: prometheus.NewDesc(
            prometheus.BuildFQName(namespace, "", "scrape_duration_seconds"),
            "Duration of the overall Elasticsearch exporter scrape.",
            nil, nil,
        ),
        esTookDesc: prometheus.NewDesc(
            prometheus.BuildFQName(namespace, "", "query_duration_seconds"),
            "Duration of the Elasticsearch query execution ('took' time).",
            []string{"metric_name"}, nil,
        ),

	}, nil
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.upDesc
	ch <- e.errorDesc
	ch <- e.scrapeDurDesc
    ch <- e.esTookDesc
	for _, desc := range e.descs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// Lock not strictly needed for Collect if scrapes aren't concurrent for the same instance,
	// but can prevent issues if the exporter instance is somehow reused.
	// e.mutex.Lock()
	// defer e.mutex.Unlock()

	scrapeTimeStart := time.Now()
	overallSuccess := 1.0 // Assume success initially

	// Use a context with timeout for the entire scrape operation
	// Timeout should be slightly less than Prometheus scrape timeout
	// TODO: Make this timeout configurable? Maybe based on ES client timeout?
	ctx, cancel := context.WithTimeout(context.Background(), e.client.Cfg.Timeout - 500*time.Millisecond) // Example: Use ES timeout minus buffer
    if e.client.Cfg.Timeout <= 500*time.Millisecond { // Prevent negative timeout
        ctx, cancel = context.WithTimeout(context.Background(), e.client.Cfg.Timeout)
    }
	defer cancel()


	var wg sync.WaitGroup
	for i := range e.metrics {
		wg.Add(1)
		// Capture loop variable for goroutine
		metricConf := e.metrics[i]

		go func(mc config.MetricConfig) {
			defer wg.Done()
			scrapeErr := 0.0 // 0 = success for this metric config
			metricDesc, ok := e.descs[mc.Name]
			if !ok {
				slog.Error("Internal error: Metric description not found", "metric", mc.Name)
				scrapeErr = 1.0
                overallSuccess = 0.0 // Mark overall scrape as failed
				return // Should not happen if NewExporter worked correctly
			}

			slog.Debug("Collecting metric", "name", mc.Name)
			resp, err := e.client.RunTermsQuery(ctx, &mc)
			if err != nil {
				slog.Error("Failed to run query for metric", "metric", mc.Name, "error", err)
				scrapeErr = 1.0
				overallSuccess = 0.0 // Mark overall scrape as failed
				// No return here, still report the error metric below
			}

            // Report scrape error status for this specific metric definition
            ch <- prometheus.MustNewConstMetric(e.errorDesc, prometheus.GaugeValue, scrapeErr, mc.Name)

			if err == nil && resp != nil {
                // Report Elasticsearch 'took' time
                ch <- prometheus.MustNewConstMetric(e.esTookDesc, prometheus.GaugeValue, float64(resp.Took)/1000.0, mc.Name)

				// Find our specific aggregation in the response
				aggRaw, exists := resp.Aggregations[internal_es.AggregationName]
				if !exists {
					slog.Warn("Aggregation result not found in response", "metric", mc.Name, "aggregation_name", internal_es.AggregationName)
					// Don't mark as error, maybe the query just returned no results/aggs
					return
				}

				var termsResult internal_es.TermsAggregationResult
				if err := json.Unmarshal(aggRaw, &termsResult); err != nil {
					slog.Error("Failed to unmarshal terms aggregation result", "metric", mc.Name, "error", err)
					overallSuccess = 0.0
                    // Update error metric? Already reported query success but parsing failed. Maybe a different metric?
                    // For now, let the overall 'up' metric reflect this parsing failure.
					return
				}

				slog.Debug("Processing buckets for metric", "name", mc.Name, "count", len(termsResult.Buckets))

				// Process buckets
				for _, bucket := range termsResult.Buckets {
					// Construct label values: term label first, then static labels alphabetically
					termValue := internal_es.GetBucketKeyAsString(bucket.Key)
					labelValues := []string{termValue}

					// Ensure consistent order for static labels (important!)
					staticLabelKeys := make([]string, 0, len(mc.Labels.Static))
					for k := range mc.Labels.Static {
						staticLabelKeys = append(staticLabelKeys, k)
					}
					// Sort keys for consistent label order in Prometheus
                    // Note: The order MUST match the order defined when creating the Desc
                    // Let's reconstruct the order based on the metric configuration
                    for _, key := range staticLabelKeys {
                        labelValues = append(labelValues, mc.Labels.Static[key])
                    }

					// Create and send the metric
					m, err := prometheus.NewConstMetric(
						metricDesc,
						prometheus.GaugeValue,
						float64(bucket.DocCount),
						labelValues..., // Expand slice to varargs
					)
					if err != nil {
						slog.Warn("Failed to create metric", "metric", mc.Name, "term", termValue, "error", err)
						continue // Skip this specific bucket/metric point
					}
					ch <- m
				}

                // TODO: Optionally expose sum_other_doc_count? Maybe as a separate metric?
                // e.g., <metric_name>_other_docs gauge

			}

		}(metricConf)
	}

	wg.Wait() // Wait for all metric scrapes to complete

	ch <- prometheus.MustNewConstMetric(e.upDesc, prometheus.GaugeValue, overallSuccess)
	ch <- prometheus.MustNewConstMetric(e.scrapeDurDesc, prometheus.GaugeValue, time.Since(scrapeTimeStart).Seconds())
	slog.Info("Finished scraping metrics", "duration_seconds", time.Since(scrapeTimeStart).Seconds(), "overall_success", overallSuccess > 0.5)

}