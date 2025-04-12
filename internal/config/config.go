package config

import (
	"fmt"
	"strings"
	"regexp"
	"time"
)

// ConnectionConfig holds Elasticsearch connection details.
type ConnectionConfig struct {
	Addresses          []string      `yaml:"addresses"`
	Username           string        `yaml:"username,omitempty"` // omitempty useful for YAML
	Password           string        `yaml:"password,omitempty"` // omitempty useful for YAML
	APIKey             string        `yaml:"apiKey,omitempty"`   // Base64 encoded 'id:api_key'
	CloudID            string        `yaml:"cloudId,omitempty"`
	Timeout            time.Duration `yaml:"timeout"`
	HealthCheck        bool          `yaml:"healthCheck"`
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"`
	// Consider adding CACert path if needed:
	// CACertPath         string        `yaml:"caCertPath,omitempty"`
}

// SourceLabelConfig defines how to map an Elasticsearch source field to a Prometheus label.
type SourceLabelConfig struct {
	SourceField string `yaml:"field"`        // The field name in the Elasticsearch _source document (can use dot notation for nested fields)
	PromLabel   string `yaml:"label"`        // The desired Prometheus label name
}

// LabelConfig defines how aggregation results and source data map to Prometheus labels.
type LabelConfig struct {
	// TermLabel is the Prometheus label for the aggregation term/key.
	// Required only when query.term_field is specified.
	TermLabel string `yaml:"term_label,omitempty"`

	// Static labels are added to every metric produced by this config.
	Static map[string]string `yaml:"static,omitempty"`

	// SourceLabels define mappings from Elasticsearch _source fields to Prometheus labels.
	// Requires corresponding fields to be listed in query.source_fields.
	SourceLabels []SourceLabelConfig `yaml:"source_labels,omitempty"`
}

// QueryConfig defines the specifics of an Elasticsearch query.
type QueryConfig struct {
	// Indices are the Elasticsearch indices to target. Required.
	Indices []string `yaml:"indices"`

	// TermField specifies the field for a terms aggregation.
	// If set, the exporter performs a terms aggregation.
	// If empty, the exporter performs a direct search query.
	TermField string `yaml:"term_field,omitempty"`

	// FilterQuery is an optional Elasticsearch query DSL (in JSON string format)
	// used to filter documents *before* aggregation or direct search.
	FilterQuery string `yaml:"filter_query,omitempty"` // e.g., '{"term": {"status": "error"}}'

	// FilterQueryString is an optional Lucene query string syntax filter.
	// Use either filter_query or filter_query_string, not both.
	FilterQueryString string `yaml:"filter_query_string,omitempty"` // e.g., 'status:error AND NOT user:system'

	// Size determines the number of buckets for terms aggregations OR
	// the number of hits for direct search queries. Defaults to 10.
	Size int `yaml:"size"`

	// Missing provides a value to use for documents missing the term_field in aggregations.
	Missing *string `yaml:"missing,omitempty"`

	// MinDocCount filters terms aggregation buckets to only include those with
	// at least this many documents. Defaults to 1.
	MinDocCount *int `yaml:"min_doc_count,omitempty"`

	// SourceFields lists the fields to retrieve from the _source of matching documents.
	// Required if Labels.SourceLabels are defined.
	// For aggregations, this uses a top_hits sub-aggregation.
	// For direct searches, this limits the _source fields returned.
	SourceFields []string `yaml:"source_fields,omitempty"`
}

// MetricConfig defines a single Prometheus metric derived from an ES query.
type MetricConfig struct {
	// Name is the Prometheus metric name (without namespace). Required.
	Name string `yaml:"name"`
	// Help is the Prometheus metric help text. Required.
	Help string `yaml:"help"`
	// Type is the Prometheus metric type. Only "gauge" is currently supported. Defaults to "gauge".
	Type string `yaml:"type,omitempty"`
	// Query defines the Elasticsearch query to execute. Required.
	Query QueryConfig `yaml:"query"`
	// Labels define how to label the resulting Prometheus metric. Required.
	Labels LabelConfig `yaml:"labels"`
}

// TopLevelMetrics is used for unmarshalling YAML files containing a list of metrics.
type TopLevelMetrics struct {
	Metrics []MetricConfig `yaml:"metrics"`
}

// Default values
const (
	DefaultQuerySize   = 10
	DefaultTimeout     = 10 * time.Second
	DefaultMinDocCount = 1
	DefaultMetricType  = "gauge"
)

var (
	// Prometheus metric names must match ^[a-zA-Z_:][a-zA-Z0-9_:]*$
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	// Prometheus label names must match ^[a-zA-Z_][a-zA-Z0-9_]*$ and not start with __
	labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Validate checks metric configuration for basic correctness and applies defaults.
func (mc *MetricConfig) Validate() error {
	// --- Required Fields ---
	if mc.Name == "" {
		return fmt.Errorf("metric 'name' is required")
	}
	if !isValidMetricName(mc.Name) {
		return fmt.Errorf("invalid metric name: %q. Must match %s", mc.Name, metricNameRE.String())
	}
	if mc.Help == "" {
		return fmt.Errorf("metric 'help' string is required for metric %q", mc.Name)
	}
	if len(mc.Query.Indices) == 0 {
		return fmt.Errorf("query.indices list cannot be empty for metric %q", mc.Name)
	}

	// --- Type Validation & Default ---
	mc.Type = strings.ToLower(mc.Type)
	if mc.Type == "" {
		mc.Type = DefaultMetricType
	}
	if mc.Type != DefaultMetricType {
		return fmt.Errorf("invalid metric type %q for metric %q: only '%s' is supported", mc.Type, mc.Name, DefaultMetricType)
	}

	// --- Query Type Specific Validation ---
	isAggregation := mc.Query.TermField != ""

	if isAggregation {
		// Aggregation Query Checks
		if mc.Labels.TermLabel == "" {
			// TermLabel is required if TermField is specified
			return fmt.Errorf("labels.term_label is required for metric %q when query.term_field (%q) is specified", mc.Name, mc.Query.TermField)
		}
		if !isValidLabelName(mc.Labels.TermLabel) {
			return fmt.Errorf("invalid labels.term_label name %q for metric %q. Must match %s and not start with '__'", mc.Labels.TermLabel, mc.Name, labelNameRE.String())
		}
	} else {
		// Direct Search Query Checks
		if mc.Labels.TermLabel != "" {
			// TermLabel should not be specified if TermField is empty
			return fmt.Errorf("labels.term_label (%q) should only be specified for metric %q when query.term_field is also set", mc.Labels.TermLabel, mc.Name)
		}
	}

	// --- General Query Validation ---
	if mc.Query.Size < 0 {
		return fmt.Errorf("query.size cannot be negative for metric %q", mc.Name)
	}
	if mc.Query.FilterQuery != "" && mc.Query.FilterQueryString != "" {
		return fmt.Errorf("cannot specify both query.filter_query and query.filter_query_string for metric %q", mc.Name)
	}
	if mc.Query.MinDocCount != nil {
		if *mc.Query.MinDocCount < 0 {
			return fmt.Errorf("query.min_doc_count cannot be negative for metric %q", mc.Name)
		}
		if !isAggregation {
			// MinDocCount only applies to aggregations
			return fmt.Errorf("query.min_doc_count can only be used when query.term_field is specified for metric %q", mc.Name)
		}
	}
	if mc.Query.Missing != nil && !isAggregation {
		// Missing only applies to aggregations
		return fmt.Errorf("query.missing can only be used when query.term_field is specified for metric %q", mc.Name)
	}

	// --- Label Validation ---
	for k := range mc.Labels.Static {
		if !isValidLabelName(k) {
			return fmt.Errorf("invalid static label name %q for metric %q. Must match %s and not start with '__'", k, mc.Name, labelNameRE.String())
		}
	}
	sourceFieldsSpecified := len(mc.Query.SourceFields) > 0
	for _, sl := range mc.Labels.SourceLabels {
		if sl.SourceField == "" {
			return fmt.Errorf("source_labels entry requires a 'field' for metric %q", mc.Name)
		}
		if sl.PromLabel == "" {
			return fmt.Errorf("source_labels entry requires a 'label' for metric %q (field: %q)", mc.Name, sl.SourceField)
		}
		if !isValidLabelName(sl.PromLabel) {
			return fmt.Errorf("invalid source_labels label name %q for metric %q (field: %q). Must match %s and not start with '__'", sl.PromLabel, mc.Name, sl.SourceField, labelNameRE.String())
		}
		// Warn if source labels are configured but no source fields requested
		if !sourceFieldsSpecified {
			// This isn't strictly an error preventing startup, but the labels will never be populated.
			fmt.Printf("Warning: Metric %q defines source_labels (e.g., field %q -> label %q) but does not specify any query.source_fields. These labels will be empty.\n", mc.Name, sl.SourceField, sl.PromLabel)
			// To make this a hard error, return an error here instead of printing a warning.
		}
	}

	// --- Apply Defaults ---
	if mc.Query.Size == 0 {
		mc.Query.Size = DefaultQuerySize
	}
	// DefaultMinDocCount is handled implicitly by Elasticsearch if not set,
	// but we could default it here if we want a non-zero default like 1:
	// if isAggregation && mc.Query.MinDocCount == nil {
	//     one := DefaultMinDocCount
	//     mc.Query.MinDocCount = &one
	// }

	return nil
}

func isValidMetricName(name string) bool {
	return metricNameRE.MatchString(name)
}

func isValidLabelName(name string) bool {
	// Prometheus convention: labels starting with __ are reserved.
	if len(name) > 2 && name[:2] == "__" {
		return false
	}
	return labelNameRE.MatchString(name)
}