package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/spf13/pflag"
)

var (
	// Web server flags
	listenAddress = pflag.String("web.listen-address", ":9488", "Address to listen on for web interface and telemetry.")
	metricsPath   = pflag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	webConfigFile = pflag.String("web.config.file", "", "Path to web configuration file enabling TLS or authentication.")

	// Configuration flags
	queriesDir = pflag.String("config.queries.dir", "./queries", "Path to the directory containing query definition YAML files.")

	// Logging flags
	logLevel  = pflag.String("log.level", "info", "Set log level (debug, info, warn, error).")
	logFormat = pflag.String("log.format", "text", "Set log format (text, json).")

	// Test mode flags
	testQuery = pflag.String("test.query-name", "", "Run in test mode for a specific query name (or all if empty) and exit.")
	testMode  = pflag.Bool("test", false, "Enable test mode. Connects to ES, runs queries, prints results, and exits.")
)

func main() {
	// Register flags from config package *before* parsing
	config.RegisterConnectionFlags()
	// Register flags defined in main
	pflag.CommandLine.AddFlagSet(pflag.CommandLine)

	pflag.Parse() // Parse all defined flags

	// Setup logging as early as possible using a local function
	setupLogging(*logLevel, *logFormat) // Call local setupLogging
	// From now on, use slog directly (e.g., slog.Info, slog.Error)

	// Load connection config AFTER parsing flags.
	connCfg, err := config.LoadConnectionConfig()
	if err != nil {
		slog.Error("Failed to load Elasticsearch connection configuration", "error", err)
		os.Exit(1)
	}

	// Log sensitive info only at debug level
	logCfgForDebug := *connCfg
	if logCfgForDebug.Password != "" { logCfgForDebug.Password = "[masked]" }
	if logCfgForDebug.APIKey != "" { logCfgForDebug.APIKey = "[masked]" }
	slog.Debug("Elasticsearch connection configuration loaded", "config", logCfgForDebug)

	// --- Test Mode ---
	if *testMode {
		slog.Info("Running uex in test mode...")
		runTestMode(connCfg)
		os.Exit(0)
	}

	// --- Normal Operation ---
	slog.Info("Starting uex - Universal Elasticsearch Exporter")

	slog.Info("Loading query configurations...", "directory", *queriesDir)
	metricConfigs, err := config.LoadQueryConfigs(*queriesDir)
	if err != nil {
		slog.Error("Fatal error loading query configurations directory", "directory", *queriesDir, "error", err)
		os.Exit(1)
	}

	slog.Info("Initializing Elasticsearch client...")
	esClient, err := elasticsearch.NewClient(connCfg)
	if err != nil {
		slog.Error("Failed to initialize Elasticsearch client", "error", err)
		os.Exit(1)
	}
	slog.Info("Elasticsearch client initialized successfully")

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
	mux.HandleFunc("/", landingPageHandler)

	server := &http.Server{
		Addr:         *listenAddress,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// --- Start Server and Handle Shutdown ---
	_, serverStopCtx := context.WithCancel(context.Background())
	srvExit := make(chan error, 1)

	go func() {
		slog.Info("Starting HTTP server", "address", *listenAddress, "metrics_path", *metricsPath)
		webFlags := &web.FlagConfig{
			WebListenAddresses: &[]string{*listenAddress},
			WebSystemdSocket:   new(bool),
			WebConfigFile:      webConfigFile,
		}
		// Pass the default slog logger directly
		srvExit <- web.ListenAndServe(server, webFlags, slog.Default())
	}()

	// --- Graceful Shutdown ---
	shutdownSig := make(chan os.Signal, 1)
	signal.Notify(shutdownSig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	select {
	case err := <-srvExit:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server unexpected exit", "error", err)
			serverStopCtx()
			os.Exit(1)
		}
		slog.Info("HTTP server stopped.")
	case sig := <-shutdownSig:
		slog.Warn("Shutdown signal received", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server graceful shutdown failed", "error", err)
		} else {
			slog.Info("HTTP server shutting down gracefully...")
		}

		<-srvExit // Wait for ListenAndServe goroutine to exit
		slog.Info("HTTP server stopped.")
		serverStopCtx() // Signal context cancellation
	}

	slog.Info("Application shut down gracefully")
}

func landingPageHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<html>
		<head><title>uex - Universal Elasticsearch Exporter</title></head>
		<body>
		<h1>uex - Universal Elasticsearch Exporter</h1>
		<p><a href="%s">Metrics</a></p>
		</body>
		</html>`, *metricsPath)
}

// setupLogging configures the global logger based on command-line flags.
// Moved back into main.go as there's no internal/logging package.
func setupLogging(level string, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning": // Added "warning" alias
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
		// Use fmt here as slog isn't fully configured yet if level is invalid
		fmt.Fprintf(os.Stderr, "Warning: Invalid log level '%s', defaulting to 'info'\n", level)
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: lvl <= slog.LevelDebug, // Add source info for debug level
	}

	// Use os.Stderr for logging by default
	logWriter := os.Stderr

	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(logWriter, opts)
	} else {
		handler = slog.NewTextHandler(logWriter, opts)
	}
	slog.SetDefault(slog.New(handler))
	slog.Debug("Logging setup complete", "level", lvl.String(), "format", format)
}


// runTestMode connects to ES, loads query configs, runs them, prints results, and exits.
func runTestMode(connCfg *config.ConnectionConfig) {
	// Logging is already set up by setupLogging() called in main()

	slog.Info("Test Mode: Loading query configurations...", "directory", *queriesDir)
	metricConfigs, err := config.LoadQueryConfigs(*queriesDir)
	if err != nil {
		slog.Error("Test Mode: Fatal error loading query configurations directory", "directory", *queriesDir, "error", err)
		os.Exit(1)
	}
	if len(metricConfigs) == 0 && *testQuery != "" {
		slog.Error("Test Mode: No valid metric configurations loaded, cannot test specific query.", "query_name", *testQuery, "directory", *queriesDir)
		os.Exit(1)
	}
	if len(metricConfigs) == 0 && *testQuery == "" {
		slog.Warn("Test Mode: No valid metric configurations loaded.", "directory", *queriesDir)
	} else {
		slog.Info("Test Mode: Successfully loaded metric configurations", "count", len(metricConfigs), "directory", *queriesDir)
	}

	slog.Info("Test Mode: Initializing Elasticsearch client...")
	esClient, err := elasticsearch.NewClient(connCfg)
	if err != nil {
		slog.Error("Test Mode: Failed to initialize Elasticsearch client", "error", err)
		os.Exit(1)
	}
	slog.Info("Test Mode: Elasticsearch client initialized successfully.")

	if *testQuery == "" && len(metricConfigs) == 0 {
		slog.Info("Test mode: Connection successful, no query definitions loaded or specified to test.")
		return
	}
	if *testQuery == "" && len(metricConfigs) > 0 {
		slog.Info("Test mode: Connection successful. Testing ALL loaded query definitions.")
	}
	if *testQuery != "" {
		slog.Info("Test mode: Connection successful. Testing specific query definition.", "query_name", *testQuery)
	}

	foundQuery := false
	for i := range metricConfigs {
		mc := metricConfigs[i]
		if *testQuery == "" || mc.Name == *testQuery {
			foundQuery = true
			// Use fmt.Printf for test mode output for direct console readability
			fmt.Printf("\n--- Testing Query Definition: %s ---\n", mc.Name)
			fmt.Printf("  Help: %s\n", mc.Help)
			fmt.Printf("  Indices: %s\n", strings.Join(mc.Query.Indices, ", "))

			isAggregation := mc.Query.TermField != ""
			if isAggregation {
				fmt.Printf("  Type: Terms Aggregation\n")
				fmt.Printf("  Term Field: %s\n", mc.Query.TermField)
				fmt.Printf("  Term Label: %s\n", mc.Labels.TermLabel)
				if mc.Query.Missing != nil {
					fmt.Printf("  Missing Value: %q\n", *mc.Query.Missing)
				}
				if mc.Query.MinDocCount != nil {
					fmt.Printf("  Min Doc Count: %d\n", *mc.Query.MinDocCount)
				}
			} else {
				fmt.Printf("  Type: Direct Search\n")
			}

			if mc.Query.FilterQuery != "" {
				fmt.Printf("  Filter Query (JSON): %s\n", mc.Query.FilterQuery)
			}
			if mc.Query.FilterQueryString != "" {
				fmt.Printf("  Filter Query (String): %s\n", mc.Query.FilterQueryString)
			}
			fmt.Printf("  Size: %d\n", mc.Query.Size)
			fmt.Printf("  Source Fields Requested: %v\n", mc.Query.SourceFields)
			fmt.Printf("  Static Labels: %v\n", mc.Labels.Static)
			fmt.Printf("  Source Labels Config: %v\n", mc.Labels.SourceLabels)

			fmt.Println("\n  Executing query against Elasticsearch...")
			ctx, cancel := context.WithTimeout(context.Background(), connCfg.Timeout)
			resp, err := esClient.RunQuery(ctx, &mc)
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

			if isAggregation {
				fmt.Printf("\n  Aggregation Results (%s):\n", elasticsearch.AggregationNameForKey)
				aggRaw, exists := resp.Aggregations[elasticsearch.AggregationNameForKey]
				if !exists {
					fmt.Println("    Aggregation results key not found in response. (Maybe no matching docs?)")
				} else {
					var termsResult elasticsearch.TermsAggregationResult
					if err := json.Unmarshal(aggRaw, &termsResult); err != nil {
						fmt.Printf("    ERROR unmarshalling terms aggregation result: %v\n", err)
						fmt.Printf("    Raw aggregation JSON: %s\n", string(aggRaw))
					} else {
						fmt.Printf("    Doc Count Error Upper Bound: %d\n", termsResult.DocCountErrorUpperBound)
						fmt.Printf("    Sum Other Doc Count: %d\n", termsResult.SumOtherDocCount)
						fmt.Printf("    Buckets (%d):\n", len(termsResult.Buckets))
						if len(termsResult.Buckets) == 0 {
							fmt.Println("      (No buckets returned)")
						}
						for _, bucket := range termsResult.Buckets {
							sourceInfo := "(No source requested or found)"
							if bucket.SourceDoc != nil && len(bucket.SourceDoc.Hits.Hits) > 0 {
								sourceJSON, _ := json.MarshalIndent(bucket.SourceDoc.Hits.Hits[0].Source, "        ", "  ")
								sourceInfo = fmt.Sprintf("_source:\n        %s", string(sourceJSON))
							} else if len(mc.Query.SourceFields) > 0 {
								 sourceInfo = "(_source requested but not found in bucket)"
							}
							fmt.Printf("      - Key: %v (%s), Count: %d %s\n",
								bucket.Key,
								elasticsearch.GetBucketKeyAsString(bucket.Key),
								bucket.DocCount,
								sourceInfo)
						}
					}
				}
			} else {
				fmt.Printf("\n  Direct Search Hits Info:\n")
				fmt.Printf("    Total Matching Docs: %d (Relation: %s)\n", resp.Hits.Total.Value, resp.Hits.Total.Relation)
				fmt.Printf("    Hits Returned (Size Limit): %d\n", len(resp.Hits.Hits))
				if len(resp.Hits.Hits) == 0 {
					fmt.Println("      (No hits returned)")
				}
				for i, hit := range resp.Hits.Hits {
					fmt.Printf("      - Hit %d: Index=%s, ID=%s, Score=%v\n", i+1, hit.Index, hit.ID, hit.Score)
					if hit.Source != nil {
						sourceJSON, _ := json.MarshalIndent(hit.Source, "        ", "  ")
						fmt.Printf("        _source:\n        %s\n", string(sourceJSON))
					} else {
						fmt.Println("        (_source not available or not requested)")
					}
				}
			}
			fmt.Println("--------------------------------------")

			if *testQuery != "" {
				break
			}
		}
	}

	if !foundQuery && *testQuery != "" {
		fmt.Printf("\nERROR: Test mode: Query definition with name '%s' not found in loaded configurations.\n", *testQuery)
		os.Exit(1)
	}
}