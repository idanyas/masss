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
	RetryCount     int           // Number of validation attempts per proxy (always performed)
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
		CheckerWorkers: 36000,
		CheckTimeout:   8 * time.Second, // Reduced from 10s for faster failure detection
		CheckEndpoints: []string{
			"https://checkip.amazonaws.com/",
			"https://whatismyip.akamai.com/",
			"https://icanhazip.com",
		},
		RetryCount:    6,                     // Always make 6 validation attempts per proxy
		RetryDelay:    50 * time.Millisecond, // Fixed delay between attempts
		GeoAPIBaseURL: os.Getenv("IDAI"),     // Read from environment variable
	}
}
