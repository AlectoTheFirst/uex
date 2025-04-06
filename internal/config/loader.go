package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// Added a check to prevent redefinition of flags
var flagsDefined bool

// Updated `LoadConnectionConfig` to return the existing configuration if flags are already defined
var existingConfig *ConnectionConfig

// LoadConnectionConfig defines connection flags and loads overrides from environment variables.
// Flag parsing must happen in main() *after* calling this function to define the flags.
// It returns a pointer to a ConnectionConfig struct populated with defaults or flag definitions,
// ready to be modified by flag parsing and environment variable overlays.
func LoadConnectionConfig() (*ConnectionConfig, error) {
	if flagsDefined {
		return existingConfig, nil // Return the existing configuration
	}
	flagsDefined = true

	cfg := &ConnectionConfig{} // Start with an empty config

	// Define flags using pflag
	pflag.StringSliceVar(&cfg.Addresses, "elasticsearch.address", []string{"http://localhost:9200"}, "Elasticsearch node addresses (comma-separated URLs)")
	pflag.StringVar(&cfg.Username, "elasticsearch.username", "", "Elasticsearch basic authentication username")
	pflag.StringVar(&cfg.Password, "elasticsearch.password", "", "Elasticsearch basic authentication password")
	pflag.StringVar(&cfg.APIKey, "elasticsearch.api-key", "", "Elasticsearch API Key (Base64 encoded 'id:api_key')")
	pflag.StringVar(&cfg.CloudID, "elasticsearch.cloud-id", "", "Elasticsearch Cloud ID")
	pflag.DurationVar(&cfg.Timeout, "elasticsearch.timeout", DefaultTimeout, "Elasticsearch request timeout")
	pflag.BoolVar(&cfg.HealthCheck, "elasticsearch.healthcheck", true, "Perform health check on Elasticsearch connection startup")
	pflag.BoolVar(&cfg.InsecureSkipVerify, "elasticsearch.tls.insecure-skip-verify", false, "Skip TLS certificate verification")

	existingConfig = cfg // Cache the configuration
	return cfg, nil // Return the config struct with defaults/flag definitions
}

// ApplyEnvVarOverrides updates the ConnectionConfig with environment variable values
// ONLY if the corresponding flag was not set (i.e., still has its default value).
// This function should be called AFTER flag.Parse() in main.go.
func ApplyEnvVarOverrides(cfg *ConnectionConfig) {

	// Check environment variables ONLY if the flag is still set to its default.
	// This requires knowing the default values.

	// Check Addresses (more complex due to slice default)
	addressesFlag := flag.Lookup("elasticsearch.address")
	if addressesFlag != nil && !isFlagPassed(addressesFlag.Name) { // Check if the flag was actually passed on the command line
		if addressesEnv := os.Getenv("ES_ADDRESS"); addressesEnv != "" {
			cfg.Addresses = strings.Split(addressesEnv, ",")
		}
	}
	// A simpler check if we assume any non-empty env var overrides the default value:
	// isDefaultAddr := len(cfg.Addresses) == 1 && cfg.Addresses[0] == "http://localhost:9200"
	// if isDefaultAddr {
	//     if addressesEnv := os.Getenv("ES_ADDRESS"); addressesEnv != "" {
	//         cfg.Addresses = strings.Split(addressesEnv, ",")
	//     }
	// }


	if !isFlagPassed("elasticsearch.username") {
		if usernameEnv := os.Getenv("ES_USERNAME"); usernameEnv != "" {
			cfg.Username = usernameEnv
		}
	}
	if !isFlagPassed("elasticsearch.password") {
		if passwordEnv := os.Getenv("ES_PASSWORD"); passwordEnv != "" {
			cfg.Password = passwordEnv
		}
	}
	if !isFlagPassed("elasticsearch.api-key") {
		if apiKeyEnv := os.Getenv("ES_API_KEY"); apiKeyEnv != "" {
			cfg.APIKey = apiKeyEnv
		}
	}
	if !isFlagPassed("elasticsearch.cloud-id") {
		if cloudIDEnv := os.Getenv("ES_CLOUD_ID"); cloudIDEnv != "" {
			cfg.CloudID = cloudIDEnv
		}
	}
	if !isFlagPassed("elasticsearch.timeout") {
		if timeoutStr := os.Getenv("ES_TIMEOUT"); timeoutStr != "" {
			if duration, err := time.ParseDuration(timeoutStr); err == nil {
				cfg.Timeout = duration
			} else {
				fmt.Printf("Warning: Invalid ES_TIMEOUT format: %v. Using flag/default value %s\n", err, cfg.Timeout)
			}
		}
	}
	// Booleans: Rely on flags; env var handling for booleans is often ambiguous.
	// If env var override is needed, add explicit checks like:
	// if !isFlagPassed("elasticsearch.healthcheck") {
	//     if hcEnv := os.Getenv("ES_HEALTHCHECK"); hcEnv != "" {
	//          // parse hcEnv (e.g., "true", "1", "false", "0")
	//     }
	// }
}

// FinalizeConfigValidation performs validation checks that should happen *after*
// flags have been parsed and environment variables applied.
func FinalizeConfigValidation(cfg *ConnectionConfig) error {
	// Basic validation
	if len(cfg.Addresses) == 0 && cfg.CloudID == "" {
		return fmt.Errorf("elasticsearch connection requires at least one address via --elasticsearch.address/ES_ADDRESS or Cloud ID via --elasticsearch.cloud-id/ES_CLOUD_ID")
	}

	// Validate authentication methods
	authMethods := 0
	hasBasicAuth := cfg.Username != "" && cfg.Password != ""
	hasApiKey := cfg.APIKey != ""

	if hasBasicAuth {
		authMethods++
	}
	if hasApiKey {
		authMethods++
	}

	// CloudID can coexist with Basic Auth OR ApiKey, but not both Basic and ApiKey together
	if hasBasicAuth && hasApiKey {
		// This combination is never allowed
		return fmt.Errorf("cannot use both Basic Authentication (username/password) and API Key authentication together")
	}

	// If CloudID is not present, ensure at most one auth method is used.
	// (The check above already covers the case where both are present).
	// No extra check needed here based on the previous one.

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
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		fileName := file.Name()
		if strings.HasSuffix(fileName, ".yaml") || strings.HasSuffix(fileName, ".yml") {
			foundYaml = true
			filePath := filepath.Join(dirPath, fileName)
			yamlFile, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Printf("Warning: Failed to read query file %q: %v. Skipping file.\n", filePath, err)
				continue // Skip this file, try others
			}

			var topLevel TopLevelMetrics
			err = yaml.Unmarshal(yamlFile, &topLevel)
			if err != nil {
				fmt.Printf("Warning: Failed to parse YAML in file %q: %v. Skipping file.\n", filePath, err)
				continue // Skip this file
			}

			for i := range topLevel.Metrics {
				metric := &topLevel.Metrics[i] // Get pointer for validation modifications

				// Validate and apply defaults for each metric definition
				if err := metric.Validate(); err != nil {
					fmt.Printf("Warning: Invalid metric definition in file %q (name: %q): %v. Skipping metric.\n", filePath, metric.Name, err)
					continue // Skip invalid metric
				}

				// Check for duplicate metric names across all loaded files
				if existingFile, exists := metricNames[metric.Name]; exists {
					fmt.Printf("Warning: Duplicate metric name %q found. Defined in %q and %q. Skipping definition from %q.\n",
						metric.Name, existingFile, filePath, filePath)
					continue // Skip duplicate metric
				}

				metricNames[metric.Name] = filePath // Record the name and origin
				allMetrics = append(allMetrics, *metric) // Append the validated metric
			}
		}
	}

	if !foundYaml {
		// It's not necessarily an error to find no YAML files, just maybe unexpected.
		fmt.Printf("Warning: No .yaml or .yml files found in query config directory %q\n", dirPath)
	}

	if len(allMetrics) > 0 {
		fmt.Printf("Successfully loaded %d metric configuration(s) from %q\n", len(allMetrics), dirPath)
	} else if foundYaml {
		// Found YAML files but none contained valid, non-duplicate metrics
		fmt.Printf("Warning: Found YAML file(s) in %q, but none contained valid metric definitions.\n", dirPath)
	}


	// Return successfully loaded metrics, even if it's an empty slice.
	// An error is only returned if the directory itself couldn't be read.
	return allMetrics, nil
}


// isFlagPassed checks if a flag was explicitly passed on the command line.
// Credit: https://stackoverflow.com/a/54747682
func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}