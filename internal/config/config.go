package config

import (
	"os"
	"time"
)

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
	RetryCount     int           // Number of validation attempts per proxy (for working proxies)
	RetryDelay     time.Duration // Delay between validation attempts

	// Geolocation API (optional - from IDAI environment variable)
	GeoAPIBaseURL string
}

// Default returns default configuration
func Default() *Config {
	return &Config{
		OutputFile:     "all.txt",
		OutputDir:      "result",
		ResultJSONFile: "result/all.json",
		CheckerWorkers: 24000,
		CheckTimeout:   5 * time.Second,
		CheckEndpoints: []string{
			"https://checkip.amazonaws.com/",
			"https://whatismyip.akamai.com/",
			"https://icanhazip.com",
		},
		RetryCount:    3,
		RetryDelay:    50 * time.Millisecond,
		GeoAPIBaseURL: os.Getenv("IDAI"),
	}
}
