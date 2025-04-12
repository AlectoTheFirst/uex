package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag" // Using pflag as in the original code
	"gopkg.in/yaml.v3"
)

// Connection flag variables (will be populated by pflag)
var (
	esAddresses          *[]string
	esUsername           *string
	esPassword           *string
	esAPIKey             *string
	esCloudID            *string
	esTimeout            *time.Duration
	esHealthCheck        *bool
	esInsecureSkipVerify *bool
)

// RegisterConnectionFlags defines the command-line flags related to Elasticsearch connection.
// This should be called *before* pflag.Parse().
func RegisterConnectionFlags() {
	// Use default values here
	esAddresses = pflag.StringSlice("elasticsearch.address", []string{"http://localhost:9200"}, "Elasticsearch node addresses (comma-separated URLs, env: ES_ADDRESS)")
	esUsername = pflag.String("elasticsearch.username", "", "Elasticsearch basic authentication username (env: ES_USERNAME)")
	esPassword = pflag.String("elasticsearch.password", "", "Elasticsearch basic authentication password (env: ES_PASSWORD)")
	esAPIKey = pflag.String("elasticsearch.api-key", "", "Elasticsearch API Key (Base64 encoded 'id:api_key', env: ES_API_KEY)")
	esCloudID = pflag.String("elasticsearch.cloud-id", "", "Elasticsearch Cloud ID (env: ES_CLOUD_ID)")
	esTimeout = pflag.Duration("elasticsearch.timeout", DefaultTimeout, "Elasticsearch request timeout (env: ES_TIMEOUT)")
	esHealthCheck = pflag.Bool("elasticsearch.healthcheck", true, "Perform health check on Elasticsearch connection startup (env: ES_HEALTHCHECK)")
	esInsecureSkipVerify = pflag.Bool("elasticsearch.tls.insecure-skip-verify", false, "Skip TLS certificate verification (env: ES_TLS_INSECURE_SKIP_VERIFY)")
}

// LoadConnectionConfig creates a ConnectionConfig based on flags and environment variables.
// This must be called *after* pflag.Parse() has been called in main.go.
func LoadConnectionConfig() (*ConnectionConfig, error) {
	// Start with default values (or values potentially set by flags already)
	cfg := &ConnectionConfig{
		Addresses:          *esAddresses,
		Username:           *esUsername,
		Password:           *esPassword,
		APIKey:             *esAPIKey,
		CloudID:            *esCloudID,
		Timeout:            *esTimeout,
		HealthCheck:        *esHealthCheck,
		InsecureSkipVerify: *esInsecureSkipVerify,
	}

	// Apply environment variable overrides *if* the corresponding flag was NOT set.
	// We check this by comparing the flag value to its default value *after* parsing.
	applyEnvVarOverrides(cfg) // Apply env vars based on parsed flags/defaults

	// Final validation after flags and env vars are considered
	if err := validateConnectionConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyEnvVarOverrides updates the ConnectionConfig with environment variable values.
// This version assumes standard precedence: Flag > Env Var > Default.
// It modifies the passed cfg based on environment variables.
func applyEnvVarOverrides(cfg *ConnectionConfig) {
	if addressesEnv := os.Getenv("ES_ADDRESS"); addressesEnv != "" {
		// Check if flag was set by comparing to default. This is imperfect but common.
		// A better way requires tracking flag sources, which pflag can do but adds complexity.
		// Simple approach: If flag has default AND env var exists, use env var.
		// If flag was set to non-default, flag takes precedence (already done by pflag parsing).
		// This requires checking the *initial* default value.
		defaultAddr := []string{"http://localhost:9200"} // Hardcoding default here, less ideal
		isDefaultAddr := len(cfg.Addresses) == len(defaultAddr)
		if isDefaultAddr {
			for i, v := range cfg.Addresses {
				if v != defaultAddr[i] {
					isDefaultAddr = false
					break
				}
			}
		}
		if isDefaultAddr {
			cfg.Addresses = strings.Split(addressesEnv, ",")
			slog.Debug("Applying ES_ADDRESS from environment variable", "value", cfg.Addresses)
		}
	}

	if usernameEnv := os.Getenv("ES_USERNAME"); usernameEnv != "" && cfg.Username == "" {
		cfg.Username = usernameEnv
		slog.Debug("Applying ES_USERNAME from environment variable")
	}
	if passwordEnv := os.Getenv("ES_PASSWORD"); passwordEnv != "" && cfg.Password == "" {
		cfg.Password = passwordEnv
		slog.Debug("Applying ES_PASSWORD from environment variable")
	}
	if apiKeyEnv := os.Getenv("ES_API_KEY"); apiKeyEnv != "" && cfg.APIKey == "" {
		cfg.APIKey = apiKeyEnv
		slog.Debug("Applying ES_API_KEY from environment variable")
	}
	if cloudIDEnv := os.Getenv("ES_CLOUD_ID"); cloudIDEnv != "" && cfg.CloudID == "" {
		cfg.CloudID = cloudIDEnv
		slog.Debug("Applying ES_CLOUD_ID from environment variable")
	}

	if timeoutStr := os.Getenv("ES_TIMEOUT"); timeoutStr != "" && cfg.Timeout == DefaultTimeout {
		if duration, err := time.ParseDuration(timeoutStr); err == nil {
			cfg.Timeout = duration
			slog.Debug("Applying ES_TIMEOUT from environment variable", "value", cfg.Timeout)
		} else {
			slog.Warn("Invalid duration format in environment variable ES_TIMEOUT", "value", timeoutStr, "error", err)
		}
	}

	if healthCheckStr := os.Getenv("ES_HEALTHCHECK"); healthCheckStr != "" && cfg.HealthCheck == true { // Default is true
		if hc, err := parseBoolEnvVar(healthCheckStr); err == nil {
			cfg.HealthCheck = hc
			slog.Debug("Applying ES_HEALTHCHECK from environment variable", "value", cfg.HealthCheck)
		} else {
			slog.Warn("Invalid boolean format in environment variable ES_HEALTHCHECK", "value", healthCheckStr, "error", err)
		}
	}

	if skipVerifyStr := os.Getenv("ES_TLS_INSECURE_SKIP_VERIFY"); skipVerifyStr != "" && cfg.InsecureSkipVerify == false { // Default is false
		if sv, err := parseBoolEnvVar(skipVerifyStr); err == nil {
			cfg.InsecureSkipVerify = sv
			slog.Debug("Applying ES_TLS_INSECURE_SKIP_VERIFY from environment variable", "value", cfg.InsecureSkipVerify)
		} else {
			slog.Warn("Invalid boolean format in environment variable ES_TLS_INSECURE_SKIP_VERIFY", "value", skipVerifyStr, "error", err)
		}
	}
}

// parseBoolEnvVar parses common boolean string representations.
func parseBoolEnvVar(val string) (bool, error) {
	lowerVal := strings.ToLower(val)
	if lowerVal == "true" || lowerVal == "1" || lowerVal == "yes" || lowerVal == "on" {
		return true, nil
	}
	if lowerVal == "false" || lowerVal == "0" || lowerVal == "no" || lowerVal == "off" {
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean string: %q", val)
}


// validateConnectionConfig performs validation checks *after* flags and env vars are resolved.
func validateConnectionConfig(cfg *ConnectionConfig) error {
	// Basic validation
	if len(cfg.Addresses) == 0 && cfg.CloudID == "" {
		return fmt.Errorf("elasticsearch connection requires at least one address via --elasticsearch.address/ES_ADDRESS or Cloud ID via --elasticsearch.cloud-id/ES_CLOUD_ID")
	}
	if len(cfg.Addresses) > 0 && cfg.CloudID != "" {
		// Client library usually prefers CloudID if both are set, but warn user.
		slog.Warn("Both elasticsearch.address and elasticsearch.cloud-id are specified; Cloud ID will likely be used by the client library.")
	}

	// Validate authentication methods
	authMethods := 0
	hasBasicAuth := cfg.Username != "" || cfg.Password != "" // User might provide only one, treat as intent
	hasApiKey := cfg.APIKey != ""

	if hasBasicAuth {
		if cfg.Username == "" || cfg.Password == "" {
			// Require both for basic auth
			return fmt.Errorf("basic authentication requires both --elasticsearch.username/ES_USERNAME and --elasticsearch.password/ES_PASSWORD")
		}
		authMethods++
	}
	if hasApiKey {
		authMethods++
	}

	// Cannot use multiple *explicit* auth methods (Basic vs APIKey).
	// CloudID often handles its own auth or can be combined with APIKey/Basic by the client lib.
	if authMethods > 1 {
		return fmt.Errorf("cannot use multiple authentication methods (basic auth, api key) simultaneously")
	}

	return nil
}

// LoadQueryConfigs loads metric definitions from all YAML files in a directory.
func LoadQueryConfigs(dirPath string) ([]MetricConfig, error) {
	var allMetrics []MetricConfig
	metricNames := make(map[string]string) // Keep track of names and origin file

	files, err := os.ReadDir(dirPath)
	if err != nil {
		// This is a critical error - can't read the directory
		return nil, fmt.Errorf("failed to read query config directory %q: %w", dirPath, err)
	}

	foundYaml := false
	fileLoadErrors := 0
	metricParseErrors := 0
	duplicateMetrics := 0

	for _, file := range files {
		if file.IsDir() {
			continue // Skip directories
		}

		fileName := file.Name()
		if !strings.HasSuffix(fileName, ".yaml") && !strings.HasSuffix(fileName, ".yml") {
			continue // Skip non-yaml files
		}

		foundYaml = true
		filePath := filepath.Join(dirPath, fileName)
		slog.Debug("Reading query config file", "path", filePath)

		yamlFile, err := os.ReadFile(filePath)
		if err != nil {
			slog.Error("Failed to read query file, skipping", "path", filePath, "error", err)
			fileLoadErrors++
			continue // Skip this file, try others
		}

		var topLevel TopLevelMetrics
		err = yaml.Unmarshal(yamlFile, &topLevel)
		if err != nil {
			slog.Error("Failed to parse YAML in file, skipping", "path", filePath, "error", err)
			fileLoadErrors++
			continue // Skip this file
		}

		slog.Debug("Processing metrics from file", "path", filePath, "count", len(topLevel.Metrics))
		for i := range topLevel.Metrics {
			// Operate on a pointer to allow Validate to modify (e.g., set defaults)
			metric := &topLevel.Metrics[i]

			// Validate and apply defaults for each metric definition
			if err := metric.Validate(); err != nil {
				slog.Error("Invalid metric definition, skipping metric",
					"path", filePath,
					"metric_name", metric.Name, // Use name even if invalid for logging
					"error", err)
				metricParseErrors++
				continue // Skip invalid metric
			}

			// Check for duplicate metric names across all loaded files
			if existingFile, exists := metricNames[metric.Name]; exists {
				slog.Error("Duplicate metric name found, skipping definition",
					"metric_name", metric.Name,
					"file1", existingFile,
					"file2", filePath)
				duplicateMetrics++
				continue // Skip duplicate metric
			}

			metricNames[metric.Name] = filePath      // Record the name and origin
			allMetrics = append(allMetrics, *metric) // Append the validated metric (dereference pointer)
			slog.Debug("Successfully validated and added metric config", "name", metric.Name, "file", filePath)
		}
	}

	// --- Logging Summary ---
	if !foundYaml {
		slog.Warn("No .yaml or .yml files found in query config directory", "directory", dirPath)
	}
	if fileLoadErrors > 0 {
		slog.Warn("Encountered errors reading/parsing some query config files", "count", fileLoadErrors, "directory", dirPath)
	}
	if metricParseErrors > 0 {
		slog.Warn("Skipped some invalid metric definitions", "count", metricParseErrors, "directory", dirPath)
	}
	if duplicateMetrics > 0 {
		slog.Warn("Skipped some duplicate metric name definitions", "count", duplicateMetrics, "directory", dirPath)
	}

	if len(allMetrics) > 0 {
		slog.Info("Successfully loaded metric configurations", "count", len(allMetrics), "directory", dirPath)
	} else if foundYaml {
		// Found YAML files but none contained valid, non-duplicate metrics
		slog.Warn("Found YAML file(s) but none contained valid metric definitions", "directory", dirPath)
	} else {
		// No YAML files found and no metrics loaded
		slog.Info("No metric configurations loaded", "directory", dirPath)
	}

	// Return successfully loaded metrics, even if it's an empty slice.
	// An error is only returned if the directory itself couldn't be read initially.
	return allMetrics, nil
}

// Note: Removed isFlagPassed function as the env var logic relies on defaults comparison now.