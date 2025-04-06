package config

import (
	"fmt"
	"regexp"
	"time"
)

// ConnectionConfig holds Elasticsearch connection details.
type ConnectionConfig struct {
	Addresses          []string      `yaml:"addresses"`
	Username           string        `yaml:"username"`
	Password           string        `yaml:"password"`
	APIKey             string        `yaml:"apiKey"`
	CloudID            string        `yaml:"cloudId"`
	Timeout            time.Duration `yaml:"timeout"`
	HealthCheck        bool          `yaml:"healthCheck"`
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"`
}

// LabelConfig defines how aggregation results map to Prometheus labels.
type LabelConfig struct {
	TermLabel string            `yaml:"term_label"`
	Static    map[string]string `yaml:"static,omitempty"`
}

// QueryConfig defines the specifics of an Elasticsearch terms query.
type QueryConfig struct {
	Indices           []string `yaml:"indices"`
	TermField         string   `yaml:"term_field"`
	FilterQuery       string   `yaml:"filter_query"`
	FilterQueryString string   `yaml:"filter_query_string"`
	Size              int      `yaml:"size"`
	Missing           *string  `yaml:"missing,omitempty"`
	MinDocCount       *int     `yaml:"min_doc_count,omitempty"`
}

// MetricConfig defines a single Prometheus metric derived from an ES query.
type MetricConfig struct {
	Name   string      `yaml:"name"`
	Help   string      `yaml:"help"`
	Type   string      `yaml:"type"`
	Query  QueryConfig `yaml:"query"`
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
)

var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Validate checks metric configuration for basic correctness and applies defaults.
func (mc *MetricConfig) Validate() error {
	if mc.Name == "" {
		return fmt.Errorf("metric name is required")
	}
	if !isValidMetricName(mc.Name) {
		return fmt.Errorf("invalid metric name: %s", mc.Name)
	}
	if mc.Help == "" {
		return fmt.Errorf("metric help string is required for metric %q", mc.Name)
	}
	if mc.Type != "gauge" && mc.Type != "Gauge" && mc.Type != "" {
		return fmt.Errorf("invalid metric type %q for metric %q: only 'gauge' is supported", mc.Type, mc.Name)
	}
	if mc.Query.TermField == "" {
		return fmt.Errorf("query.term_field is required for metric %q", mc.Name)
	}
	if len(mc.Query.Indices) == 0 {
		return fmt.Errorf("query.indices list cannot be empty for metric %q", mc.Name)
	}
	if mc.Labels.TermLabel == "" {
		return fmt.Errorf("labels.term_label is required for metric %q", mc.Name)
	}
	if !isValidLabelName(mc.Labels.TermLabel) {
		return fmt.Errorf("invalid term_label name: %s", mc.Labels.TermLabel)
	}
	for k := range mc.Labels.Static {
		if !isValidLabelName(k) {
			return fmt.Errorf("invalid static label name: %s", k)
		}
	}
	if mc.Query.Size < 0 {
		return fmt.Errorf("query.size cannot be negative for metric %q", mc.Name)
	}
	if mc.Query.FilterQuery != "" && mc.Query.FilterQueryString != "" {
		return fmt.Errorf("cannot specify both query.filter_query and query.filter_query_string for metric %q", mc.Name)
	}
	if mc.Query.MinDocCount != nil && *mc.Query.MinDocCount < 0 {
		return fmt.Errorf("query.min_doc_count cannot be negative for metric %q", mc.Name)
	}

	// Set defaults
	if mc.Type == "" {
		mc.Type = "gauge"
	}
	if mc.Query.Size == 0 {
		mc.Query.Size = DefaultQuerySize
	}

	return nil
}

func isValidMetricName(name string) bool {
	return metricNameRE.MatchString(name)
}

func isValidLabelName(name string) bool {
	if len(name) > 2 && name[:2] == "__" {
		return false
	}
	return labelNameRE.MatchString(name)
}