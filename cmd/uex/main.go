package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AlectoTheFirst/uex/internal/config"
	"github.com/AlectoTheFirst/uex/internal/elasticsearch"
	"github.com/AlectoTheFirst/uex/internal/exporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
)

var (
	// Command line flags
	listenAddress = flag.String("web.listen-address", ":9488", "Address to listen on for web interface and telemetry.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	queriesDir    = flag.String("config.queries.dir", "./queries", "Path to the directory containing query definition YAML files.")
	logLevel      = flag.String("log.level", "info", "Set log level (debug, info, warn, error).")
	logFormat     = flag.String("log.format", "text", "Set log format (text, json).")
	testQuery     = flag.String("test.query-name", "", "Run in test mode for a specific query name (or all if empty) and exit.")
	testMode      = flag.Bool("test", false, "Enable test mode. Connects to ES, runs queries, prints results, and exits.")
	webConfigFile = flag.String("web.config.file", "", "Path to configuration file that can enable TLS or authentication.")
)

func main() {
	// Define connection flags before parsing
	config.LoadConnectionConfig() // Defines flags, doesn't load values yet

	flag.Parse() // Now parse all flags

	setupLogging(*logLevel, *logFormat)

	if *testMode {
		slog.Info("Running uex in test mode...")
		runTestMode()
		os.Exit(0)
	}

	slog.Info("Starting uex - Universal Elasticsearch Exporter")

	// --- Load Configuration ---
	slog.Info("Loading Elasticsearch connection configuration...")
	// Call LoadConnectionConfig again, this time it uses the parsed flag values
	// and overlays environment variables as needed.
	connCfg, err := config.LoadConnectionConfig()
	if err != nil {
		slog.Error("Failed to load Elasticsearch connection configuration", "error", err)
		os.Exit(1)
	}

	// Apply environment variable overrides after parsing flags
	config.ApplyEnvVarOverrides(connCfg)

	slog.Info("Loading query configurations...", "directory", *queriesDir)
	metricConfigs, err := config.LoadQueryConfigs(*queriesDir)
	if err != nil {
		// Log the error but continue if the directory itself was readable
		slog.Error("Error loading query configurations", "error", err)
		// Decide if this is fatal. If LoadQueryConfigs returns error only for dir read fail, maybe exit.
		// If it returns nil error even if no files found/parsed, check length below.
		// Assuming error is only for directory issues here:
		os.Exit(1)
	}
	if len(metricConfigs) == 0 {
		slog.Warn("No valid metric configurations loaded. Exporter will run but produce no metrics from queries.", "directory", *queriesDir)
		// Allow running without query configs? Or exit? For now, allow.
		// os.Exit(1)
	}

	// --- Initialize Elasticsearch Client ---
	slog.Info("Initializing Elasticsearch client...")
	esClient, err := elasticsearch.NewClient(connCfg)
	if err != nil {
		slog.Error("Failed to initialize Elasticsearch client", "error", err)
		os.Exit(1)
	}

	// --- Initialize and Register Exporter ---
	slog.Info("Initializing Prometheus exporter...")
	exp, err := exporter.NewExporter(esClient, metricConfigs)
	if err != nil {
		slog.Error("Failed to create exporter", "error", err)
		os.Exit(1)
	}

	prometheus.MustRegister(exp)
	slog.Info("Exporter registered with Prometheus")

	// --- Setup HTTP Server ---
	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<html>
			<head><title>uex - Universal Elasticsearch Exporter</title></head>
			<body>
			<h1>uex - Universal Elasticsearch Exporter</h1>
			<p><a href='%s'>Metrics</a></p>
			</body>
			</html>`, *metricsPath)
	})

	server := &http.Server{
		Addr:         *listenAddress,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// --- Start Server and Handle Shutdown ---
	serverCtx, serverStopCtx := context.WithCancel(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sig
		slog.Warn("Shutdown signal received, shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(serverCtx, 10*time.Second)
		defer cancel()

		go func() {
			<-shutdownCtx.Done()
			if shutdownCtx.Err() == context.DeadlineExceeded {
				slog.Error("Graceful shutdown timed out, forcing exit.")
				os.Exit(1)
			}
		}()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown failed", "error", err)
		}
		serverStopCtx() // Signal that server has stopped
	}()

	slog.Info("Starting HTTP server", "address", *listenAddress)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	
	// Use the exporter-toolkit's web server with TLS/auth support
	if err := web.ListenAndServe(server, &web.FlagConfig{
		WebListenAddresses: &[]string{*listenAddress},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      webConfigFile,
	}, logger); err != nil {
		slog.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}

	<-serverCtx.Done() // Wait for server context to be cancelled (by shutdown routine)
	slog.Info("Server gracefully stopped")
}

func setupLogging(level string, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
		log.Printf("Invalid log level '%s', defaulting to 'info'", level)
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: lvl}

	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func runTestMode() {
	// Logging already set up in main

	connCfg, err := config.LoadConnectionConfig() // Load config using parsed flags/env vars
	if err != nil {
		slog.Error("Failed to load Elasticsearch connection configuration for test", "error", err)
		os.Exit(1)
	}

	metricConfigs, err := config.LoadQueryConfigs(*queriesDir)
	if err != nil {
		slog.Error("Failed to load query configurations for test", "error", err)
		// Continue if only specific files failed, but error if dir unreadable
	}
	if len(metricConfigs) == 0 && *testQuery != "" {
		slog.Error("No valid metric configurations loaded, cannot test specific query.", "query_name", *testQuery)
		os.Exit(1)
	}

	esClient, err := elasticsearch.NewClient(connCfg)
	if err != nil {
		slog.Error("Failed to initialize Elasticsearch client for test", "error", err)
		os.Exit(1)
	}
	slog.Info("Elasticsearch client initialized successfully for test.")

	if *testQuery == "" && len(metricConfigs) == 0 {
		slog.Info("Test mode: Connection successful, no query definitions loaded or specified to test.")
		return
	}
	if *testQuery == "" && len(metricConfigs) > 0 {
		slog.Info("Test mode: Connection successful. Testing ALL loaded query definitions.")
	}

	foundQuery := false
	for i := range metricConfigs {
		mc := metricConfigs[i] // Operate on copy for safety
		if *testQuery == "" || mc.Name == *testQuery {
			foundQuery = true
			fmt.Printf("\n--- Testing Query Definition: %s ---\n", mc.Name)
			fmt.Printf("  Help: %s\n", mc.Help)
			fmt.Printf("  Indices: %s\n", strings.Join(mc.Query.Indices, ", "))
			fmt.Printf("  Term Field: %s\n", mc.Query.TermField)
			if mc.Query.FilterQuery != "" {
				fmt.Printf("  Filter Query (JSON): %s\n", mc.Query.FilterQuery)
			}
			if mc.Query.FilterQueryString != "" {
				fmt.Printf("  Filter Query (String): %s\n", mc.Query.FilterQueryString)
			}
			if mc.Query.Missing != nil {
				fmt.Printf("  Missing Value: %s\n", *mc.Query.Missing)
			}
			if mc.Query.MinDocCount != nil {
				fmt.Printf("  Min Doc Count: %d\n", *mc.Query.MinDocCount)
			}
			fmt.Printf("  Term Label: %s\n", mc.Labels.TermLabel)
			fmt.Printf("  Static Labels: %v\n", mc.Labels.Static)

			fmt.Println("\n  Executing query against Elasticsearch...")
			ctx, cancel := context.WithTimeout(context.Background(), connCfg.Timeout)
			resp, err := esClient.RunTermsQuery(ctx, &mc)
			cancel()

			if err != nil {
				fmt.Printf("  ERROR running query: %v\n", err)
				fmt.Println("--------------------------------------")
				continue
			}

			fmt.Printf("  Query Execution Time (Took): %dms\n", resp.Took)
			fmt.Printf("  Timed Out: %t\n", resp.TimedOut)
			fmt.Printf("  Shards: Total=%d, Successful=%d, Skipped=%d, Failed=%d\n",
				resp.Shards.Total, resp.Shards.Successful, resp.Shards.Skipped, resp.Shards.Failed)

			aggRaw, exists := resp.Aggregations[elasticsearch.AggregationName]
			if !exists {
				fmt.Println("  Aggregation results key not found in response.")
				fmt.Println("--------------------------------------")
				continue
			}

			var termsResult elasticsearch.TermsAggregationResult
			if err := json.Unmarshal(aggRaw, &termsResult); err != nil {
				fmt.Printf("  ERROR unmarshalling aggregation result: %v\n", err)
				fmt.Printf("  Raw aggregation JSON: %s\n", string(aggRaw))
				fmt.Println("--------------------------------------")
				continue
			}

			fmt.Printf("\n  Aggregation Results (%s):\n", elasticsearch.AggregationName)
			fmt.Printf("    Doc Count Error Upper Bound: %d\n", termsResult.DocCountErrorUpperBound)
			fmt.Printf("    Sum Other Doc Count: %d\n", termsResult.SumOtherDocCount)
			fmt.Printf("    Buckets (%d):\n", len(termsResult.Buckets))
			if len(termsResult.Buckets) == 0 {
				fmt.Println("      (No buckets returned)")
			}
			for _, bucket := range termsResult.Buckets {
				fmt.Printf("      - Key: %v (%s), Count: %d\n", bucket.Key, elasticsearch.GetBucketKeyAsString(bucket.Key), bucket.DocCount)
			}
			fmt.Println("--------------------------------------")

			if *testQuery != "" { // If testing a specific query, break after finding and testing it
				break
			}
		}
	}

	if !foundQuery && *testQuery != "" {
		fmt.Printf("\nERROR: Test mode: Query definition with name '%s' not found in loaded configurations.\n", *testQuery)
		os.Exit(1)
	}
}
