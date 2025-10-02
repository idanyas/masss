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
}

// Default returns default configuration
func Default() *Config {
	return &Config{
		OutputFile:     "all.txt",
		OutputDir:      "result",
		ResultJSONFile: "result/all.json",
		CheckerWorkers: 8000,
		CheckTimeout:   10 * time.Second,
		CheckEndpoints: []string{
			"https://checkip.amazonaws.com/",
			"https://whatismyip.akamai.com/",
		},
	}
}
