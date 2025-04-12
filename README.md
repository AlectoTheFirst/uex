# uex - Universal Elasticsearch Exporter

![UEX Banner](./assets/uex-banner-small.png)

```
# Disclaimer

This project is a personal hack project created in my free time as an Elasticsearch and Prometheus enthusiast.
It was primarily developed with the assistance of LLMs (Gemini 2.5 and GitHub Copilot).
It took roughly 2 hours to develop. This application may contain bugs, performance issues, or security vulnerabilities.
Use at your own risk in non-critical environments only.
Feel free to contribute!

```

A Prometheus exporter written in Go that generates metrics from Elasticsearch data based on flexible YAML configurations. It supports both terms aggregations and direct search queries.

This exporter allows you to define custom metrics derived from your Elasticsearch data without relying on Elasticsearch's built-in Watcher or complex scripting within Elasticsearch itself.

## Features

* **Terms Aggregation Metrics:** Executes Elasticsearch `terms` aggregations and exposes the bucket counts as Prometheus Gauge metrics.
* **Direct Search Metrics:** Performs direct search queries and creates metrics from the results without requiring aggregation.
* **YAML Configuration:** Define metrics, target indices, aggregation fields, filters, and labels in simple YAML files.
* **Multiple Query Files:** Load metric definitions from multiple `.yaml` files within a specified directory.
* **Decoupled Connection Config:** Configure Elasticsearch connection details via command-line flags or environment variables, separate from query definitions (ideal for GitOps and security).
* **Dynamic Label Mapping:** Map the term value from the aggregation bucket directly to a configurable Prometheus label.
* **Source Field Labels:** Extract values from document source fields and map them to Prometheus labels.
* **Static Labels:** Add fixed labels to metrics for environment identification, service names, etc.
* **Query Filtering:** Apply Elasticsearch Query DSL (JSON string) or Lucene `query_string` filters to narrow down documents before aggregation.
* **Aggregation Options:** Configure `size` (number of buckets), `missing` value handling, and `min_doc_count`.
* **Test Mode:** Validate your connection and query definitions against your live Elasticsearch cluster before starting the exporter.
* **Standard Go Tooling:** Built using Go and the standard `prometheus/client_golang` library.

## Prerequisites

* **Go:** Version 1.21 or later. ([Installation Guide](https://go.dev/doc/install))

## Building

1. **Clone the repository:**
   ```bash
   git clone 
   cd uex
   ```

2. **Ensure dependencies are present:**
   ```bash
   go mod tidy
   ```

3. **Build the binary:**
   ```bash
   go build ./cmd/uex/
   ```

This will create an executable file named `uex` in the current directory (`uex/`).

## Configuration

Configuration is split into two parts: Elasticsearch connection details and Query definitions.

### 1. Elasticsearch Connection

Connection details are configured primarily via command-line flags, with environment variables acting as fallbacks if a flag is *not* explicitly set.

**Flags take precedence over Environment Variables.**

| Flag | Environment Variable | Default | Description |
|:-----|:---------------------|:--------|:------------|
| `--elasticsearch.address` | `ES_ADDRESS` | `http://localhost:9200` | Comma-separated Elasticsearch node URLs |
| `--elasticsearch.username` | `ES_USERNAME` | | Basic authentication username |
| `--elasticsearch.password` | `ES_PASSWORD` | | Basic authentication password |
| `--elasticsearch.api-key` | `ES_API_KEY` | | API Key authentication (Base64 encoded `id:api_key`) |
| `--elasticsearch.cloud-id` | `ES_CLOUD_ID` | | Cloud ID for Elastic Cloud deployments |
| `--elasticsearch.timeout` | `ES_TIMEOUT` | `10s` | Request timeout for Elasticsearch queries |
| `--elasticsearch.healthcheck` | `ES_HEALTHCHECK` | `true` | Perform an initial health check ping on startup |
| `--elasticsearch.tls.insecure-skip-verify` | `ES_TLS_INSECURE_SKIP_VERIFY` | `false` | Skip TLS certificate verification (use with caution!) |
| `--web.config.file` | | `web.yaml` | Path to configuration file for web server settings |
| `--web.listen-address` | | `:9488` | Address to listen on for web interface and telemetry |
| `--web.telemetry-path` | | `/metrics` | Path under which to expose metrics |


**Authentication:**
* You can use **only one** of the following methods: Basic Auth (`username`/`password`), API Key (`api-key`).
* If using `cloud-id`, you can combine it with either Basic Auth or API Key, but you still cannot use Basic Auth and API Key together.

### 2. Query Definitions

Query definitions tell `uex` *what* metrics to generate. They are loaded from `.yaml` or `.yml` files located in a directory specified by the `--config.queries.dir` flag (defaults to `./queries`).

Each YAML file must contain a top-level `metrics:` key, which holds a list of metric definition objects.

**Metric Definition Structure (`<queries_dir>/my_metrics.yaml`):**

```yaml
metrics:
  # Example 1: Terms Aggregation with source fields for additional labeling
  - name: "myapp_http_requests_by_status" # Prometheus metric name (required)
    help: "Total HTTP requests to myapp by status code" # Prometheus help string (required)
    # type: gauge # Optional, defaults to "gauge", currently only type supported.
    query:
      indices: ["myapp-logs-*", "other-logs-*"] # List of index patterns (required)
      term_field: "http.response.status_code"   # Field for terms aggregation (determines grouping)
      filter_query: '{"term": {"service.name": "my-web-app"}}' # Optional: JSON Query DSL filter string
      # filter_query_string: "service.environment:production AND status:active" # Optional: Lucene query_string filter (use only one filter type)
      size: 20                # Optional: Max terms/buckets to return (default: 10)
      missing: "unknown"      # Optional: Value for documents missing the term_field
      min_doc_count: 1        # Optional: Minimum doc count for a bucket (default: 1)
      source_fields: ["@timestamp", "http.request.method"] # Extract these fields from documents for labels
    labels:
      term_label: "status_code" # Prometheus label name for the term value (required)
      source_labels:            # Map source fields to labels
        - field: "@timestamp"   # Field to extract from document
          label: "last_seen"    # Label name in Prometheus
        - field: "http.request.method"
          label: "method"
      static:                   # Optional: Map of static labels added to every metric from this definition
        environment: "production"
        service: "my-web-app"

  # Example 2: Simple terms aggregation
  - name: "app_errors_by_type"
    help: "Application errors categorized by error type"
    query:
      indices: ["app-errors-*"]
      term_field: "error.type.keyword" # Often use .keyword fields for exact matching
      size: 50
    labels:
      term_label: "error_type"
      static:
        app_version: "1.2.3"
        
  # Example 3: Direct search query (no aggregation)
  - name: "critical_errors_direct"
    help: "Individual critical error events"
    query:
      indices: ["app-errors-*"]
      # No term_field means this is a direct search query
      filter_query: '{"bool": {"must": [{"term": {"severity": "critical"}}, {"range": {"@timestamp": {"gte": "now-1d"}}}]}}'
      size: 20  # Maximum number of hits to process
      source_fields: ["error.message", "error.type", "host.name", "@timestamp"]
    labels:
      # No term_label needed since we're not doing aggregation
      source_labels:
        - field: "error.message"
          label: "message"
        - field: "error.type"
          label: "type"
        - field: "host.name"
          label: "host"
        - field: "@timestamp"
          label: "timestamp"
      static:
        environment: "production"
        monitored: "true"

# You can have multiple metric definitions in one file
# and multiple files in the queries directory.
```

## Field Explanations

* **name**: The base name for the Prometheus metric (will be prefixed with `uex_`). Must follow Prometheus naming rules.
* **help**: The metric's help text in Prometheus.

### Query Configuration

* **query.indices**: A list of index patterns to query against.
* **query.term_field**: The field in your Elasticsearch documents to perform the terms aggregation on. If specified, the exporter runs a terms aggregation query. If omitted, the exporter runs a direct search query.
* **query.filter_query**: An optional filter written as a JSON string representing an Elasticsearch Query DSL object. Applied before aggregation or search.
* **query.filter_query_string**: An optional filter written as a Lucene query string. Applied before aggregation or search. Use either `filter_query` OR `filter_query_string`, not both.
* **query.size**: For terms aggregations: the maximum number of unique terms (buckets) to retrieve. For direct searches: the maximum number of documents to retrieve. Affects performance and memory.
* **query.missing**: If specified, documents that do not have the `term_field` will be grouped under this value in a separate bucket (only applies to terms aggregations).
* **query.min_doc_count**: Only return term buckets that contain at least this many documents (only applies to terms aggregations).
* **query.source_fields**: List of fields to extract from the document _source. For terms aggregations, these fields are retrieved using a top_hits sub-aggregation. For direct searches, these limit the fields returned in the response.

### Label Configuration

* **labels.term_label**: The name of the Prometheus label that will hold the value of the `term_field` from the aggregation bucket key. Required for terms aggregations, not used for direct searches.
* **labels.source_labels**: Array of mappings from Elasticsearch document fields to Prometheus labels:
  * **field**: The field name in the Elasticsearch document to extract (can use dot notation for nested fields)
  * **label**: The Prometheus label name to assign the field's value to
* **labels.static**: A map of key-value pairs that will be added as static labels to all time series generated by this metric definition.

## Running the Exporter

### Standard Run

Execute the binary, providing necessary connection flags/env vars and pointing to your queries directory:

```bash
# Using flags
./uex \
  --elasticsearch.address=http://es-node1:9200,http://es-node2:9200 \
  --elasticsearch.username=my_user \
  --elasticsearch.password=my_secret \
  --config.queries.dir=/etc/uex/queries

# Using environment variables
export ES_ADDRESS="http://es-node1:9200"
export ES_USERNAME="my_user"
export ES_PASSWORD="my_secret"
./uex --config.queries.dir=/path/to/my/queries
```

### Test Mode

Test mode allows you to verify connection and query execution without starting the HTTP server. It prints the query results directly to the console.

```bash
./uex --test --config.queries.dir=/path/to/queries [connection flags/env vars]

./uex --test --test.query-name="myapp_http_requests_by_status" --config.queries.dir=/path/to/queries [connection flags/env vars]
```

## Metrics Exposed

### Exporter Metrics

`uex` exposes standard metrics about its own operation:

* **uex_up** (Gauge): 1 if the last scrape of Elasticsearch was overall successful, 0 otherwise.
* **uex_scrape_error** (Gauge): Labeled by `metric_name`. 1 if an error occurred scraping a specific query definition, 0 otherwise.
* **uex_scrape_duration_seconds** (Gauge): Duration of the entire exporter scrape cycle.
* **uex_query_duration_seconds** (Gauge): Labeled by `metric_name`. Duration reported by Elasticsearch ('took' field in ms / 1000) for executing a specific query definition.

### Dynamically Generated Metrics

For each definition in your YAML configuration files, `uex` generates a Prometheus Gauge metric named `uex_<metric_definition_name>`.

#### Terms Aggregation Metrics

**Value**: The `doc_count` from the Elasticsearch terms aggregation bucket.

**Labels**:
* The label specified by `labels.term_label` in your config, with its value set to the term from the ES bucket key.
* Any labels defined in `labels.source_labels` populated with values from the document's source fields.
* All labels defined in the `labels.static` map for that metric definition.

**Example**:
Using the `myapp_http_requests_by_status` example from the config section, you might see metrics like:

```
# HELP uex_myapp_http_requests_by_status Total HTTP requests to myapp by status code
# TYPE uex_myapp_http_requests_by_status gauge
uex_myapp_http_requests_by_status{environment="production",method="GET",service="my-web-app",status_code="200"} 5432
uex_myapp_http_requests_by_status{environment="production",method="POST",service="my-web-app",status_code="404"} 123
```

#### Direct Search Metrics

**Value**: The count of matching documents with the same set of source field values and static labels.

**Labels**:
* Any labels defined in `labels.source_labels` populated with values from the document's source fields.
* All labels defined in the `labels.static` map for that metric definition.

**Example**:
Using the `critical_errors_direct` example from the config section, you might see metrics like:

```
# HELP uex_critical_errors_direct Individual critical error events
# TYPE uex_critical_errors_direct gauge
uex_critical_errors_direct{environment="production",host="app-server-01",message="Connection refused",monitored="true",type="connection_error"} 3
uex_critical_errors_direct{environment="production",host="app-server-02",message="Out of memory",monitored="true",type="memory_error"} 1
uex_critical_errors_direct{environment="production",host="db-server-01",message="Disk full",monitored="true",type="storage_error"} 2
```

## Call Flow / Process Explanation

Understanding the internal flow helps with debugging and configuration:

### Startup (`cmd/uex/main.go`):

1. Application entry point.
2. Set up logging with slog based on the specified log level and format.
3. Register and parse flags using the spf13/pflag package (which extends standard flag package).
4. Load connection configuration from both flags and environment variables with `config.LoadConnectionConfig()`.
5. If test mode is enabled (`--test`), run `runTestMode()` function and exit.
6. For normal operation, load metric definitions with `config.LoadQueryConfigs()`.
7. Initialize Elasticsearch client with `elasticsearch.NewClient()`.
8. Create and register the Prometheus exporter with `exporter.NewExporter()`.
9. Set up HTTP server and handlers for metrics endpoint.
10. Configure graceful shutdown handling for SIGINT/SIGTERM signals.

### Query Configuration Loading (`config.LoadQueryConfigs`):

1. Reads the directory specified by `--config.queries.dir`.
2. Iterates through all `.yaml` and `.yml` files.
3. Parses each file, expecting a `metrics:` list.
4. For each metric definition found:
   - Calls `metric.Validate()` to check for required fields, valid names, and apply defaults.
   - Validates Prometheus naming rules for metrics and labels.
   - Determines if it's a terms aggregation (has `term_field`) or direct search query (no `term_field`).
   - Checks for proper label configuration based on query type.
   - Checks for duplicate metric names across all loaded files.
5. Returns a slice of validated `config.MetricConfig` structs.

### Elasticsearch Client Initialization (`elasticsearch.NewClient`):

1. Configures the official Elastic client with connection details, authentication, and TLS settings.
2. Configures transport settings including timeouts and TLS verification options.
3. Optionally performs a health check ping if `healthCheck` is enabled.
4. Returns a client wrapper that provides query building and execution functions.

### Exporter Initialization (`exporter.NewExporter`):

1. Takes the ES client and the list of loaded `MetricConfig` structs.
2. For each metric definition:
   - Determines all required Prometheus label keys in a fixed order:
     1. Term label (if using terms aggregation)
     2. Source field labels (from document _source)
     3. Static labels (alphabetically sorted for consistency)
   - Creates a `prometheus.Desc` object for this metric with the ordered label keys.
3. Creates descriptors for internal metrics (`uex_up`, `uex_scrape_error`, etc.).
4. Returns an exporter that implements the Prometheus Collector interface.

### HTTP Server Setup:

1. Registers the exporter with Prometheus client registry.
2. Sets up an HTTP server with appropriate timeouts.
3. Configures TLS and authentication if specified in `web.config.file`.
4. Handles requests to the `/metrics` path using the Prometheus handler.
5. Sets up graceful shutdown for clean termination.

### Prometheus Scrape Cycle (`exporter.Collect`):

1. When Prometheus scrapes the `/metrics` endpoint, the `Collect` method is called.
2. Sets a timeout context for the scrape operation.
3. Initializes `overallSuccess` status to 1.0 (success).
4. Launches a separate goroutine for each metric definition to run queries concurrently.
5. Inside each goroutine:
   - Calls `elasticsearch.RunQuery()` which handles both aggregation and direct search queries.
   - Reports errors and query duration via internal metrics.
   - For successful queries, processes results based on query type:

#### For Terms Aggregations (`processAggregationResult`):

1. Extracts the aggregation results from the response.
2. For each bucket:
   - Gets the term value and document count.
   - Extracts source field values from the top_hits sub-aggregation if requested.
   - Builds a slice of label values in the correct order (term, source, static).
   - Sends a metric with the bucket's document count as the value.

#### For Direct Search Queries (`processDirectSearchResult`):

1. Processes hit documents from the search results.
2. For each hit:
   - Extracts requested field values from the document's _source.
   - Builds labels from source fields and static labels.
   - Aggregates hits with identical label combinations to generate counts.
3. For each unique label combination, sends a metric with the count of matching documents.

6. Waits for all goroutines to complete using a WaitGroup.
7. Reports overall scrape status (`uex_up`) and duration metrics.

### Test Mode Processing:

1. Performs the same configuration loading and client initialization as normal mode.
2. For a specified query (or all queries if not specified):
   - Executes the query against Elasticsearch.
   - Prints detailed results to the console instead of exposing metrics.
   - Shows query details, execution time, hit counts, and for aggregations, the bucket data.
   - For direct searches, shows document source fields for inspection.

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss bugs or feature requests, or submit a pull request.