package checker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"masss/internal/config"
	"masss/internal/domain"
)

const (
	// maxResponseSize limits how much we read from check endpoints (IPs are ~20 bytes)
	maxResponseSize = 1024
	// maxGeoWorkers limits concurrent geo API requests
	maxGeoWorkers = 32
	// geoAPITimeout is the timeout for each geo API request
	geoAPITimeout = 30 * time.Second
	// geoRetryAttempts is the number of retry attempts for geo lookups
	geoRetryAttempts = 3
	// geoRetryDelay is the delay between geo lookup retries
	geoRetryDelay = 2 * time.Second

	// Endpoint identifiers for validation rules
	endpointAmazon    = "https://checkip.amazonaws.com/"
	endpointAkamai    = "https://whatismyip.akamai.com/"
	endpointIcanhazip = "https://icanhazip.com"
)

// WorkingProxy represents a proxy with its working protocol, public IP, and statistics
type WorkingProxy struct {
	Proxy          domain.Proxy
	Protocol       domain.Protocol
	PublicIP       string
	SuccessCount   int       // Total successful endpoint checks across all attempts
	Latencies      []float64 // in milliseconds
	AmazonValid    bool      // Amazon endpoint succeeded at least once
	AkamaiValid    bool      // Akamai endpoint succeeded at least once
	IcanhaziPValid bool      // Icanhazip endpoint succeeded at least once
}

// ProxyChecker validates proxies against multiple protocols and endpoints
type ProxyChecker struct {
	cfg       *config.Config
	hostIPs   map[string]struct{} // IPs to reject (host's own IPs)
	protocols []domain.Protocol
}

// NewProxyChecker creates a new proxy checker
func NewProxyChecker(cfg *config.Config) (*ProxyChecker, error) {
	checker := &ProxyChecker{
		cfg: cfg,
		protocols: []domain.Protocol{
			domain.ProtocolHTTP,
			domain.ProtocolSOCKS4,
			domain.ProtocolSOCKS5,
		},
		hostIPs: make(map[string]struct{}),
	}

	// Detect host IPs
	if err := checker.detectHostIPs(); err != nil {
		return nil, fmt.Errorf("failed to detect host IPs: %w", err)
	}

	return checker, nil
}

// detectHostIPs gets the host's real IP from check endpoints
func (c *ProxyChecker) detectHostIPs() error {
	fmt.Println("Detecting host IP addresses...")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	defer client.CloseIdleConnections()

	for _, endpoint := range c.cfg.CheckEndpoints {
		resp, err := client.Get(endpoint)
		if err != nil {
			fmt.Printf("  Warning: Could not get IP from %s: %v\n", endpoint, err)
			continue
		}

		// Read with size limit and ensure body is closed
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		resp.Body.Close()

		if err != nil {
			fmt.Printf("  Warning: Could not read response from %s: %v\n", endpoint, err)
			continue
		}

		ip := strings.TrimSpace(string(body))
		c.hostIPs[ip] = struct{}{}
		fmt.Printf("  Host IP from %s: %s\n", endpoint, ip)
	}

	if len(c.hostIPs) == 0 {
		return fmt.Errorf("could not detect any host IPs")
	}

	fmt.Println()
	return nil
}

// Check validates all proxies using a worker pool and writes results incrementally
func (c *ProxyChecker) Check(ctx context.Context, proxies []domain.Proxy, outputDir string) (map[domain.Protocol][]domain.Proxy, error) {
	if len(proxies) == 0 {
		return nil, nil
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("Starting proxy validation with %d workers...\n", c.cfg.CheckerWorkers)
	fmt.Printf("Testing %d proxies against %d protocols and %d endpoints\n",
		len(proxies), len(c.protocols), len(c.cfg.CheckEndpoints))
	fmt.Printf("Validation strategy: %d attempts per proxy+protocol for comprehensive statistics\n", c.cfg.RetryCount)
	fmt.Printf("Writing working proxies to: %s/\n\n", outputDir)

	start := time.Now()

	// Create job and result channels - minimal buffering to reduce memory
	jobs := make(chan domain.Proxy, 1)
	workingProxies := make(chan WorkingProxy, 2)

	// Progress tracking
	var checked, workingTotal atomic.Int32
	var httpCount, socks4Count, socks5Count atomic.Int32
	var additionalAttempts atomic.Int32

	// Start result collector (collects in memory, no file writes)
	collectorDone := make(chan struct{})
	var savedProxies map[domain.Protocol][]domain.Proxy
	var jsonResults []domain.ProxyResult
	go func() {
		savedProxies, jsonResults = c.collectResults(workingProxies, &httpCount, &socks4Count, &socks5Count)
		close(collectorDone)
	}()

	// Start worker pool
	var wg sync.WaitGroup
	for i := 0; i < c.cfg.CheckerWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.worker(ctx, jobs, workingProxies, &checked, &workingTotal, &additionalAttempts)
		}()
	}

	// Progress reporter
	progressDone := make(chan struct{})
	go c.progressReporter(&checked, &workingTotal, &httpCount, &socks4Count, &socks5Count, &additionalAttempts, len(proxies), progressDone)

	// Send jobs
	go func() {
		for _, proxy := range proxies {
			select {
			case jobs <- proxy:
			case <-ctx.Done():
				close(jobs)
				return
			}
		}
		close(jobs)
	}()

	// Wait for workers
	wg.Wait()
	close(workingProxies)

	// Wait for collector to finish
	<-collectorDone
	close(progressDone)

	elapsed := time.Since(start)

	// Enrich results with geolocation data if API is configured
	if c.cfg.GeoAPIBaseURL != "" {
		fmt.Print("\nEnriching results with geolocation data... ")
		geoStart := time.Now()
		c.enrichWithGeoData(ctx, jsonResults)
		fmt.Printf("done in %.3fs\n", time.Since(geoStart).Seconds())
	}

	// Write all output files (no sorting)
	fmt.Print("\nWriting results... ")
	writeStart := time.Now()

	// Write protocol-specific text files
	if err := c.writeResults(savedProxies, outputDir); err != nil {
		return nil, fmt.Errorf("failed to write results: %w", err)
	}

	// Write JSON file
	if err := c.writeJSON(jsonResults); err != nil {
		fmt.Printf("\nWarning: Failed to write JSON: %v\n", err)
	}

	fmt.Printf("done in %.3fs\n", time.Since(writeStart).Seconds())

	// Count total working
	totalWorking := 0
	for _, proxyList := range savedProxies {
		totalWorking += len(proxyList)
	}

	// Clear progress lines (we're on line 1 after the last update)
	fmt.Print("\r\033[K\n\r\033[K\n")

	fmt.Printf("✓ Validation complete in %.2fs\n", elapsed.Seconds())
	fmt.Printf("  Total checked: %d\n", len(proxies))
	fmt.Printf("  Working (unique proxy+protocol combinations): %d\n", totalWorking)
	fmt.Printf("  Failed: %d\n", len(proxies)-int(checked.Load()))
	fmt.Printf("  Total validation attempts: %d\n", additionalAttempts.Load()+int32(len(proxies))*int32(len(c.protocols)))
	fmt.Printf("  Average attempts per proxy: %.1f\n", float64(additionalAttempts.Load()+int32(len(proxies))*int32(len(c.protocols)))/float64(len(proxies)))
	fmt.Printf("  Speed: %.0f proxies/second\n\n", float64(len(proxies))/elapsed.Seconds())

	// Per-protocol breakdown
	fmt.Println("Protocol breakdown:")
	for _, proto := range c.protocols {
		count := len(savedProxies[proto])
		if count > 0 {
			fmt.Printf("  ✓ %s: %d proxies → %s/%s.txt\n",
				strings.ToUpper(string(proto)), count, outputDir, proto)
		}
	}
	fmt.Printf("\n✓ JSON results saved to %s (%d entries)\n", c.cfg.ResultJSONFile, len(jsonResults))
	fmt.Println()

	return savedProxies, nil
}

// enrichWithGeoData fetches geolocation data for unique IPs and updates results
func (c *ProxyChecker) enrichWithGeoData(ctx context.Context, results []domain.ProxyResult) {
	// Collect unique IPs that need geo lookups
	uniqueIPs := make(map[string]struct{})
	for i := range results {
		// Extract proxy IP
		proxyInfo, err := domain.ParseProxyInfo(results[i].Address)
		if err == nil {
			uniqueIPs[proxyInfo.Host] = struct{}{}
		}
		// Public IP
		uniqueIPs[results[i].PublicIP] = struct{}{}
	}

	if len(uniqueIPs) == 0 {
		return
	}

	fmt.Printf("\n  Fetching geo data for %d unique IPs...\n", len(uniqueIPs))

	// Fetch geo data concurrently with limited workers
	ipList := make([]string, 0, len(uniqueIPs))
	for ip := range uniqueIPs {
		ipList = append(ipList, ip)
	}

	geoCache := &sync.Map{} // map[string]*domain.GeoInfo
	jobs := make(chan string, len(ipList))
	var wg sync.WaitGroup
	var completed, successful atomic.Int32

	// Progress reporter for geo enrichment
	geoDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-geoDone:
				return
			case <-ticker.C:
				c := completed.Load()
				s := successful.Load()
				if c > 0 {
					fmt.Printf("\r  Geo progress: %d/%d IPs completed (%d successful, %d failed)",
						c, len(ipList), s, c-s)
				}
			}
		}
	}()

	// Start workers
	workers := maxGeoWorkers
	if workers > len(ipList) {
		workers = len(ipList)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				var geoInfo *domain.GeoInfo
				var lastErr error

				// Try up to geoRetryAttempts times
				for attempt := 0; attempt < geoRetryAttempts; attempt++ {
					if attempt > 0 {
						time.Sleep(geoRetryDelay)
					}

					geoInfo, lastErr = c.fetchGeoInfo(ip)
					if lastErr == nil && geoInfo != nil {
						geoCache.Store(ip, geoInfo)
						successful.Add(1)
						break
					}
				}

				if lastErr != nil && geoInfo == nil {
					fmt.Printf("\n  Warning: Failed to fetch geo data for %s after %d attempts: %v",
						ip, geoRetryAttempts, lastErr)
				}

				completed.Add(1)
			}
		}()
	}

	// Send jobs
	for _, ip := range ipList {
		select {
		case jobs <- ip:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			close(geoDone)
			return
		}
	}
	close(jobs)
	wg.Wait()
	close(geoDone)

	// Clear progress line
	fmt.Print("\r\033[K")
	fmt.Printf("  ✓ Geo enrichment complete: %d/%d IPs successful\n",
		successful.Load(), len(ipList))

	// Update results with geo data
	for i := range results {
		// Proxy geo
		proxyInfo, err := domain.ParseProxyInfo(results[i].Address)
		if err == nil {
			if geoData, ok := geoCache.Load(proxyInfo.Host); ok {
				if geo, ok := geoData.(*domain.GeoInfo); ok {
					results[i].ProxyGeoInfo = geo
				}
			}
		}

		// Public IP geo
		if geoData, ok := geoCache.Load(results[i].PublicIP); ok {
			if geo, ok := geoData.(*domain.GeoInfo); ok {
				results[i].PublicGeoInfo = geo
			}
		}
	}
}

// fetchGeoInfo fetches geolocation data for an IP from the configured API
func (c *ProxyChecker) fetchGeoInfo(ip string) (*domain.GeoInfo, error) {
	if c.cfg.GeoAPIBaseURL == "" {
		return nil, nil
	}

	url := fmt.Sprintf("%s/json/%s", strings.TrimRight(c.cfg.GeoAPIBaseURL, "/"), ip)

	client := &http.Client{Timeout: geoAPITimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	var geoInfo domain.GeoInfo
	if err := json.NewDecoder(resp.Body).Decode(&geoInfo); err != nil {
		return nil, err
	}

	return &geoInfo, nil
}

// writeResults writes final protocol-specific files (no sorting for speed)
func (c *ProxyChecker) writeResults(savedProxies map[domain.Protocol][]domain.Proxy, outputDir string) error {
	for proto, proxies := range savedProxies {
		if len(proxies) == 0 {
			continue
		}

		// Write file directly without sorting
		filename := filepath.Join(outputDir, string(proto)+".txt")
		file, err := os.Create(filename)
		if err != nil {
			return err
		}

		writer := bufio.NewWriter(file)
		for _, proxy := range proxies {
			writer.WriteString(string(proxy) + "\n")
		}
		writer.Flush()
		file.Close()
	}

	return nil
}

// writeJSON writes JSON results in compact format (no indentation for minimal file size)
func (c *ProxyChecker) writeJSON(results []domain.ProxyResult) error {
	if len(results) == 0 {
		return nil
	}

	// Write compact JSON without indentation
	data, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(c.cfg.ResultJSONFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}

	return nil
}

// collectResults collects working proxies as a flat list (no grouping).
// Each working proxy+protocol combination is a separate entry.
func (c *ProxyChecker) collectResults(workingProxies <-chan WorkingProxy, httpCount, socks4Count, socks5Count *atomic.Int32) (map[domain.Protocol][]domain.Proxy, []domain.ProxyResult) {
	// For protocol-specific .txt files
	proxiesByProto := make(map[domain.Protocol][]domain.Proxy)
	proxiesByProto[domain.ProtocolHTTP] = make([]domain.Proxy, 0, 100)
	proxiesByProto[domain.ProtocolSOCKS4] = make([]domain.Proxy, 0, 100)
	proxiesByProto[domain.ProtocolSOCKS5] = make([]domain.Proxy, 0, 100)

	// For flat JSON results (no grouping)
	jsonResults := make([]domain.ProxyResult, 0, 100)

	for wp := range workingProxies {
		// Store in memory by protocol for .txt files
		proxiesByProto[wp.Protocol] = append(proxiesByProto[wp.Protocol], wp.Proxy)

		// Add as separate entry to JSON (no grouping)
		jsonResults = append(jsonResults, domain.ProxyResult{
			Protocol:       string(wp.Protocol),
			Address:        string(wp.Proxy),
			PublicIP:       wp.PublicIP,
			SuccessCount:   wp.SuccessCount,
			Latencies:      wp.Latencies,
			AmazonValid:    wp.AmazonValid,
			AkamaiValid:    wp.AkamaiValid,
			IcanhaziPValid: wp.IcanhaziPValid,
		})

		// Increment the appropriate protocol counter
		switch wp.Protocol {
		case domain.ProtocolHTTP:
			httpCount.Add(1)
		case domain.ProtocolSOCKS4:
			socks4Count.Add(1)
		case domain.ProtocolSOCKS5:
			socks5Count.Add(1)
		}
	}

	return proxiesByProto, jsonResults
}

// worker processes proxies from the job channel, testing ALL protocols for each proxy
func (c *ProxyChecker) worker(ctx context.Context, jobs <-chan domain.Proxy, workingProxies chan<- WorkingProxy, checked, working, additionalAttempts *atomic.Int32) {
	for proxy := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Test all protocols for this proxy
		for _, protocol := range c.protocols {
			var publicIP string
			var totalSuccessCount int
			var allLatencies []float64
			var amazonValid, akamaiValid, icanhaziPValid bool
			foundWorking := false

			// Make RetryCount attempts for this protocol
			for attempt := 0; attempt < c.cfg.RetryCount; attempt++ {
				if attempt > 0 {
					time.Sleep(c.cfg.RetryDelay)
					additionalAttempts.Add(1)
				}

				stats, ok := c.checkProtocol(ctx, proxy, protocol)
				if ok && stats != nil {
					if !foundWorking {
						publicIP = stats.publicIP
						foundWorking = true
					}
					totalSuccessCount += stats.successCount
					allLatencies = append(allLatencies, stats.latencies...)

					// Aggregate endpoint flags (OR operation - true if succeeded at least once)
					amazonValid = amazonValid || stats.amazonValid
					akamaiValid = akamaiValid || stats.akamaiValid
					icanhaziPValid = icanhaziPValid || stats.icanhaziPValid
				}
			}

			// Report if this protocol works (at least one endpoint succeeded)
			if foundWorking && totalSuccessCount > 0 {
				workingProxies <- WorkingProxy{
					Proxy:          proxy,
					Protocol:       protocol,
					PublicIP:       publicIP,
					SuccessCount:   totalSuccessCount,
					Latencies:      allLatencies,
					AmazonValid:    amazonValid,
					AkamaiValid:    akamaiValid,
					IcanhaziPValid: icanhaziPValid,
				}
				working.Add(1)
			}
		}

		checked.Add(1)
	}
}

// checkStats contains statistics from checking a proxy with a specific protocol
type checkStats struct {
	publicIP       string
	successCount   int       // Number of successful endpoint checks in this attempt
	latencies      []float64 // in milliseconds
	amazonValid    bool      // Amazon endpoint succeeded
	akamaiValid    bool      // Akamai endpoint succeeded
	icanhaziPValid bool      // Icanhazip endpoint succeeded
}

// checkProtocol tests a proxy with a specific protocol against all endpoints and returns statistics
func (c *ProxyChecker) checkProtocol(ctx context.Context, proxy domain.Proxy, protocol domain.Protocol) (*checkStats, bool) {
	client, err := createHTTPClient(string(proxy), protocol, c.cfg.CheckTimeout)
	if err != nil {
		return nil, false
	}
	// Explicitly close idle connections when done to free resources immediately
	defer client.CloseIdleConnections()

	// Test all endpoints concurrently and collect results
	type endpointResult struct {
		endpoint string
		publicIP string
		latency  float64
		success  bool
	}

	results := make(chan endpointResult, len(c.cfg.CheckEndpoints))
	var wg sync.WaitGroup

	for _, endpoint := range c.cfg.CheckEndpoints {
		wg.Add(1)
		go func(ep string) {
			defer wg.Done()
			start := time.Now()
			if publicIP, ok := c.checkEndpoint(ctx, client, ep); ok {
				latency := time.Since(start).Seconds() * 1000 // convert to milliseconds
				results <- endpointResult{endpoint: ep, publicIP: publicIP, latency: latency, success: true}
			} else {
				results <- endpointResult{endpoint: ep, success: false}
			}
		}(endpoint)
	}

	// Wait for all endpoint checks to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	stats := &checkStats{
		latencies: make([]float64, 0, len(c.cfg.CheckEndpoints)),
	}

	for result := range results {
		if result.success {
			stats.successCount++
			stats.latencies = append(stats.latencies, result.latency)
			if stats.publicIP == "" {
				stats.publicIP = result.publicIP
			}

			// Track which endpoint succeeded
			switch result.endpoint {
			case endpointAmazon:
				stats.amazonValid = true
			case endpointAkamai:
				stats.akamaiValid = true
			case endpointIcanhazip:
				stats.icanhaziPValid = true
			}
		}
	}

	if stats.successCount > 0 {
		return stats, true
	}
	return nil, false
}

// checkEndpoint tests a proxy against a specific endpoint with endpoint-specific validation rules
func (c *ProxyChecker) checkEndpoint(ctx context.Context, client *http.Client, endpoint string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", false
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	// Limit response size to prevent memory exhaustion from malicious endpoints
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", false
	}

	returnedIP := strings.TrimSpace(string(body))

	// Check if IP is valid
	if returnedIP == "" {
		return "", false
	}

	// Validate it's IPv4
	if !domain.IsIPv4(returnedIP) {
		return "", false
	}

	// Apply endpoint-specific validation rules
	switch endpoint {
	case endpointAmazon, endpointAkamai:
		// Accept any valid IPv4, don't check if it's a host IP
		return returnedIP, true

	case endpointIcanhazip:
		// Only accept if it's NOT a host IP
		if _, isHostIP := c.hostIPs[returnedIP]; isHostIP {
			return "", false
		}
		return returnedIP, true

	default:
		// For any unknown endpoints, use conservative logic (reject host IPs)
		if _, isHostIP := c.hostIPs[returnedIP]; isHostIP {
			return "", false
		}
		return returnedIP, true
	}
}

// progressReporter prints periodic progress updates with stable ETA calculation
func (c *ProxyChecker) progressReporter(checked, working, httpCount, socks4Count, socks5Count, additionalAttempts *atomic.Int32, total int, done <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			chk := checked.Load()
			wrk := working.Load()
			http := httpCount.Load()
			socks4 := socks4Count.Load()
			socks5 := socks5Count.Load()
			additional := additionalAttempts.Load()

			if chk > 0 {
				// Calculate overall average rate from start (stable and accurate)
				overallElapsed := time.Since(startTime).Seconds()
				overallRate := float64(chk) / overallElapsed

				remaining := total - int(chk)

				// Use overall rate for stable ETA
				eta := 0.0
				if overallRate > 0 {
					eta = float64(remaining) / overallRate
				}

				percentage := float64(chk) / float64(total) * 100

				// Progress bar (20 chars wide)
				barWidth := 20
				filled := int(percentage / 100 * float64(barWidth))
				bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

				// Line 1: Overall progress with human-readable ETA
				fmt.Printf("\r\033[KChecked: %d/%d (%.1f%%) [%s] %.0f/s | ETA: %s\n",
					chk, total, percentage, bar, overallRate, formatDuration(eta))

				// Line 2: Protocol breakdown
				fmt.Printf("\r\033[KWorking: %d | HTTP: %d | SOCKS4: %d | SOCKS5: %d | Attempts: %d\033[A",
					wrk, http, socks4, socks5, additional+chk*int32(len(c.protocols)))
			}
		}
	}
}

// formatDuration converts seconds to human-readable format (e.g., "5m 30s")
func formatDuration(seconds float64) string {
	if seconds < 0 {
		return "0s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	if seconds < 3600 {
		mins := int(seconds / 60)
		secs := int(seconds) % 60
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := int(seconds / 3600)
	mins := int(seconds/60) % 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}
