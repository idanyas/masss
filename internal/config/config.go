package config

import "time"

// Config holds application configuration
type Config struct {
	// Output settings
	OutputFile     string
	OutputDir      string
	ResultJSONFile string

	// Checker settings
	CheckerWorkers int
	CheckTimeout   time.Duration
	CheckEndpoints []string
	RetryCount     int           // Number of retry attempts for failed proxies
	RetryDelay     time.Duration // Base delay between retries (exponential backoff)
}

// Default returns default configuration
func Default() *Config {
	return &Config{
		OutputFile:     "all.txt",
		OutputDir:      "result",
		ResultJSONFile: "result/all.json",
		CheckerWorkers: 8000,
		CheckTimeout:   8 * time.Second, // Reduced from 10s for faster failure detection
		CheckEndpoints: []string{
			"https://checkip.amazonaws.com/",
			"https://whatismyip.akamai.com/",
		},
		RetryCount: 3,                    // Retry up to 3 times
		RetryDelay: 50 * time.Millisecond, // Start with 50ms, exponential backoff
	}
}