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

A Prometheus exporter written in Go that generates metrics from Elasticsearch `terms` aggregations based on flexible YAML configurations.

This exporter allows you to define custom metrics derived from your Elasticsearch data without relying on Elasticsearch's built-in Watcher or complex scripting within Elasticsearch itself.

## Features

* **Terms Aggregation Metrics:** Executes Elasticsearch `terms` aggregations and exposes the bucket counts as Prometheus Gauge metrics.
* **YAML Configuration:** Define metrics, target indices, aggregation fields, filters, and labels in simple YAML files.
* **Multiple Query Files:** Load metric definitions from multiple `.yaml` files within a specified directory.
* **Decoupled Connection Config:** Configure Elasticsearch connection details via command-line flags or environment variables, separate from query definitions (ideal for GitOps and security).
* **Dynamic Label Mapping:** Map the term value from the aggregation bucket directly to a configurable Prometheus label.
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
   git clone https://github.com/AlectoTheFirst/uex.git
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
  - name: "myapp_http_requests_by_status" # Prometheus metric name (required)
    help: "Total HTTP requests to myapp by status code" # Prometheus help string (required)
    # type: gauge # Optional, defaults to "gauge", currently only type supported.
    query:
      indices: ["myapp-logs-*", "other-logs-*"] # List of index patterns (required)
      term_field: "http.response.status_code"   # Field for terms aggregation (required)
      filter_query: '{"term": {"service.name": "my-web-app"}}' # Optional: JSON Query DSL filter string
      # filter_query_string: "service.environment:production AND status:active" # Optional: Lucene query_string filter (use only one filter type)
      size: 20                # Optional: Max terms/buckets to return (default: 10)
      missing: "unknown"      # Optional: Value for documents missing the term_field
      min_doc_count: 1        # Optional: Minimum doc count for a bucket (default: 1)
    labels:
      term_label: "status_code" # Prometheus label name for the term value (required)
      static:                   # Optional: Map of static labels added to every metric from this definition
        environment: "production"
        service: "my-web-app"

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

# You can have multiple metric definitions in one file
# and multiple files in the queries directory.
```

## Field Explanations

* **name**: The base name for the Prometheus metric (will be prefixed with `uex_`). Must follow Prometheus naming rules.
* **help**: The metric's help text in Prometheus.
* **query.indices**: A list of index patterns to query against.
* **query.term_field**: The field in your Elasticsearch documents to perform the terms aggregation on. The values of this field will become the `term_label` values in Prometheus.
* **query.filter_query**: An optional filter written as a JSON string representing an Elasticsearch Query DSL object. Applied before aggregation.
* **query.filter_query_string**: An optional filter written as a Lucene query string. Applied before aggregation. Use either `filter_query` OR `filter_query_string`, not both.
* **query.size**: The maximum number of unique terms (buckets) to retrieve from Elasticsearch. Affects performance and memory.
* **query.missing**: If specified, documents that do not have the `term_field` will be grouped under this value in a separate bucket.
* **query.min_doc_count**: Only return term buckets that contain at least this many documents.
* **labels.term_label**: The name of the Prometheus label that will hold the value of the `term_field` from the aggregation bucket key.
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

**Value**: The `doc_count` from the Elasticsearch terms aggregation bucket.

**Labels**:
* The label specified by `labels.term_label` in your config, with its value set to the term from the ES bucket key.
* All labels defined in the `labels.static` map for that metric definition.

**Example**:
Using the `myapp_http_requests_by_status` example from the config section, you might see metrics like:

```
# HELP uex_myapp_http_requests_by_status Total HTTP requests to myapp by status code
# TYPE uex_myapp_http_requests_by_status gauge
uex_myapp_http_requests_by_status{environment="production",service="my-web-app",status_code="200"} 5432
uex_myapp_http_requests_by_status{environment="production",service="my-web-app",status_code="404"} 123
uex_myapp_http_requests_by_status{environment="production",service="my-web-app",status_code="500"} 45
uex_myapp_http_requests_by_status{environment="production",service="my-web-app",status_code="unknown"} 10 # If 'missing: unknown' was used
```

## Call Flow / Process Explanation

Understanding the internal flow helps with debugging and configuration:

### Startup (`cmd/uex/main.go`):

1. Application entry point.
2. Call `config.LoadConnectionConfig()` first: This defines all connection-related command-line flags using the flag package (e.g., `--elasticsearch.address`). It returns a `config.ConnectionConfig` struct initialized with default values.
3. Call `flag.Parse()`: This parses the actual command-line arguments provided by the user, populating the variables associated with the flags (updating the `connCfg` struct where flags were passed).
4. Call `config.ApplyEnvVarOverrides(connCfg)`: Checks environment variables (e.g., `ES_ADDRESS`). If a corresponding flag was not passed on the command line, the environment variable's value is applied to the `connCfg` struct.
5. Call `config.FinalizeConfigValidation(connCfg)`: Performs validation checks on the now fully populated connection configuration (e.g., checks for conflicting auth methods).
6. Handle `--test` mode if enabled (runs `runTestMode()` and exits).

### Query Configuration Loading (`config.LoadQueryConfigs`):

1. Reads the directory specified by `--config.queries.dir`.
2. Iterates through all `.yaml` and `.yml` files.
3. Parses each file, expecting a `metrics:` list.
4. For each metric definition found:
   - Calls `metric.Validate()` to check for required fields, valid names, and apply defaults (e.g., size, type).
   - Checks for duplicate metric name across all loaded files.
5. Returns a slice of valid `config.MetricConfig` structs.

### Elasticsearch Client Initialization (`elasticsearch.NewClient`):

1. Uses the validated `connCfg` struct to configure the official `elastic/go-elasticsearch` client (addresses, auth, TLS settings, timeout).
2. Optionally performs a Ping request if `healthCheck` is enabled.

### Exporter Initialization (`exporter.NewExporter`):

1. Takes the ES client and the list of loaded `MetricConfig` structs.
2. Iterates through each `MetricConfig`:
   - Determines the required Prometheus label keys (the `term_label` plus all keys from static labels). Crucially, it establishes a fixed order for these labels.
   - Creates a `prometheus.Desc` object for this metric. The `Desc` is a descriptor containing the fully qualified metric name (`uex_<name>`), help text, and the ordered list of variable label keys. These `Desc` objects are precomputed for efficiency.
3. Creates `Desc` objects for the exporter's own metrics (`uex_up`, etc.).

### HTTP Server Setup (`cmd/uex/main.go`):

1. Registers the created exporter instance with the `prometheus/client_golang` registry (`prometheus.MustRegister`).
2. Sets up an HTTP server using `net/http`.
3. Handles requests to the `/metrics` path using `promhttp.Handler()`. This handler automatically calls the `Describe` and `Collect` methods of all registered collectors (including our exporter) when Prometheus scrapes the endpoint.
4. Sets up graceful shutdown handling for SIGINT/SIGTERM.

### Prometheus Scrape Cycle (`exporter.Collect`):

1. When Prometheus scrapes `/metrics`, the `promhttp.Handler()` calls the `Collect` method of our exporter.
2. `Collect` receives a channel (`ch`) to send metrics back to Prometheus.
3. Sets an overall timeout for the scrape operation (slightly less than the ES query timeout).
4. Launches a separate goroutine for each `MetricConfig` defined in the configuration to perform queries concurrently.
5. Inside each goroutine:
   - Calls `elasticsearch.buildQuery()` to construct the JSON query body based on the `MetricConfig`.
   - Calls `elasticsearch.RunTermsQuery()` to execute the search request against Elasticsearch using the pre-initialized client and the scrape context (with timeout).
   - Handles potential errors from the query. If an error occurs, it sends a `uex_scrape_error{metric_name="<name>"} 1` metric.
   - If the query is successful:
     - Sends the `uex_query_duration_seconds{metric_name="<name>}` metric.
     - Parses the aggregations part of the Elasticsearch JSON response, looking for the predefined aggregation name (`uex_aggregation`).
     - Unmarshals the aggregation result into `TermsAggregationResult`.
     - Iterates through each bucket in the aggregation result.
     - For each bucket:
       - Gets the term value (`bucket.Key`) and document count (`bucket.DocCount`).
       - Constructs a slice of label values. Crucially, the order of values in this slice MUST exactly match the order of label keys defined in the `prometheus.Desc` created during `NewExporter`.
       - Calls `prometheus.NewConstMetric()` with the precomputed `Desc`, gauge type, the `doc_count` as the value, and the ordered label values.
       - Sends the resulting `prometheus.Metric` object over the channel (`ch`).
6. The main `Collect` function waits for all goroutines to complete using a `sync.WaitGroup`.
7. Finally, `Collect` sends the `uex_up` and `uex_scrape_duration_seconds` metrics over the channel (`ch`).

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss bugs or feature requests, or submit a pull request.
