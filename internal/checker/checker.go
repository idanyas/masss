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
)

// WorkingProxy represents a proxy with its working protocol, public IP, and statistics
type WorkingProxy struct {
	Proxy        domain.Proxy
	Protocol     domain.Protocol
	PublicIP     string
	SuccessCount int
	Latencies    []float64 // in milliseconds
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
	fmt.Printf("Validation strategy: %d attempts per proxy for comprehensive statistics\n", c.cfg.RetryCount)
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
	fmt.Printf("  Working: %d (%.1f%%)\n", totalWorking, float64(totalWorking)/float64(len(proxies))*100)
	fmt.Printf("  Failed: %d\n", len(proxies)-totalWorking)
	fmt.Printf("  Total validation attempts: %d\n", additionalAttempts.Load()+int32(len(proxies)))
	fmt.Printf("  Average attempts per proxy: %.1f\n", float64(additionalAttempts.Load()+int32(len(proxies)))/float64(len(proxies)))
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
	fmt.Printf("\n✓ JSON results saved to %s\n", c.cfg.ResultJSONFile)
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

// writeJSON writes JSON results (no sorting for speed)
func (c *ProxyChecker) writeJSON(results []domain.ProxyResult) error {
	if len(results) == 0 {
		return nil
	}

	// Write formatted JSON without sorting
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(c.cfg.ResultJSONFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}

	return nil
}

// groupKey is used to group proxies by public IP and protocol for JSON output
type groupKey struct {
	ip    string
	proto domain.Protocol
}

// collectResults collects working proxies, grouping them for JSON output.
func (c *ProxyChecker) collectResults(workingProxies <-chan WorkingProxy, httpCount, socks4Count, socks5Count *atomic.Int32) (map[domain.Protocol][]domain.Proxy, []domain.ProxyResult) {
	// For protocol-specific .txt files
	proxiesByProto := make(map[domain.Protocol][]domain.Proxy)
	proxiesByProto[domain.ProtocolHTTP] = make([]domain.Proxy, 0, 100)
	proxiesByProto[domain.ProtocolSOCKS4] = make([]domain.Proxy, 0, 100)
	proxiesByProto[domain.ProtocolSOCKS5] = make([]domain.Proxy, 0, 100)

	// For grouped JSON results
	groupedResults := make(map[groupKey]*domain.ProxyResult)
	orderedResults := make([]*domain.ProxyResult, 0, 100)

	for wp := range workingProxies {
		// Store in memory by protocol for .txt files
		proxiesByProto[wp.Protocol] = append(proxiesByProto[wp.Protocol], wp.Proxy)

		// Group for JSON output
		key := groupKey{ip: wp.PublicIP, proto: wp.Protocol}
		if existing, found := groupedResults[key]; found {
			// Add as an alias to the existing entry
			existing.Aliases = append(existing.Aliases, domain.Alias{
				Address:      string(wp.Proxy),
				SuccessCount: wp.SuccessCount,
				Latencies:    wp.Latencies,
			})
		} else {
			// Create a new entry
			newResult := &domain.ProxyResult{
				Protocol:     string(wp.Protocol),
				Address:      string(wp.Proxy),
				PublicIP:     wp.PublicIP,
				SuccessCount: wp.SuccessCount,
				Latencies:    wp.Latencies,
			}
			groupedResults[key] = newResult
			orderedResults = append(orderedResults, newResult)
		}

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

	// Convert slice of pointers to slice of values for the return type
	finalJSONResults := make([]domain.ProxyResult, len(orderedResults))
	for i, res := range orderedResults {
		finalJSONResults[i] = *res
	}

	return proxiesByProto, finalJSONResults
}

// worker processes proxies from the job channel, always making RetryCount attempts per proxy
func (c *ProxyChecker) worker(ctx context.Context, jobs <-chan domain.Proxy, workingProxies chan<- WorkingProxy, checked, working, additionalAttempts *atomic.Int32) {
	for proxy := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var workingProtocol domain.Protocol
		var publicIP string
		var successfulAttempts int // Counts successful attempts (max = RetryCount)
		var allLatencies []float64
		foundWorkingProtocol := false

		// Always make exactly RetryCount attempts per proxy
		for attempt := 0; attempt < c.cfg.RetryCount; attempt++ {
			if attempt > 0 {
				// Fixed delay between attempts
				time.Sleep(c.cfg.RetryDelay)
				additionalAttempts.Add(1)
			}

			var stats *checkStats
			var ok bool

			if !foundWorkingProtocol {
				// First attempt or still searching for a working protocol
				var protocol domain.Protocol
				protocol, stats, ok = c.isProxyWorking(ctx, proxy)
				if ok {
					workingProtocol = protocol
					publicIP = stats.publicIP
					foundWorkingProtocol = true
				}
			} else {
				// We already found a working protocol, test it again to gather more stats
				stats, ok = c.checkProtocol(ctx, proxy, workingProtocol)
			}

			if ok && stats != nil {
				successfulAttempts++ // Increment per successful attempt, not per endpoint
				allLatencies = append(allLatencies, stats.latencies...)
			}
		}

		// Report as working if we found a working protocol and had at least one success
		if foundWorkingProtocol && successfulAttempts > 0 {
			workingProxies <- WorkingProxy{
				Proxy:        proxy,
				Protocol:     workingProtocol,
				PublicIP:     publicIP,
				SuccessCount: successfulAttempts, // Now represents successful attempts (1-6)
				Latencies:    allLatencies,
			}
			working.Add(1)
		}

		checked.Add(1)
	}
}

// checkStats contains statistics from checking a proxy with a specific protocol
type checkStats struct {
	publicIP     string
	successCount int
	latencies    []float64 // in milliseconds
}

// isProxyWorking tests if a proxy works with any protocol and returns the working protocol and stats
func (c *ProxyChecker) isProxyWorking(ctx context.Context, proxy domain.Proxy) (domain.Protocol, *checkStats, bool) {
	// Test all protocols concurrently
	type result struct {
		protocol domain.Protocol
		stats    *checkStats
	}
	resultChan := make(chan result, len(c.protocols))
	var wg sync.WaitGroup

	for _, protocol := range c.protocols {
		wg.Add(1)
		go func(proto domain.Protocol) {
			defer wg.Done()
			if stats, ok := c.checkProtocol(ctx, proxy, proto); ok {
				select {
				case resultChan <- result{protocol: proto, stats: stats}:
				default:
				}
			}
		}(protocol)
	}

	// Wait with early exit on first success
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case res := <-resultChan:
		return res.protocol, res.stats, true
	case <-done:
		return "", nil, false
	case <-ctx.Done():
		return "", nil, false
	}
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
				results <- endpointResult{publicIP: publicIP, latency: latency, success: true}
			} else {
				results <- endpointResult{success: false}
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
		}
	}

	if stats.successCount > 0 {
		return stats, true
	}
	return nil, false
}

// checkEndpoint tests a proxy against a specific endpoint and returns the public IP
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

	// Check if IP is valid and different from host
	if returnedIP == "" {
		return "", false
	}

	// Validate it's IPv4
	if !domain.IsIPv4(returnedIP) {
		return "", false
	}

	// Reject if same as host IP
	if _, isHostIP := c.hostIPs[returnedIP]; isHostIP {
		return "", false
	}

	return returnedIP, true
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
				successRate := float64(0)
				if chk > 0 {
					successRate = float64(wrk) / float64(chk) * 100
				}
				fmt.Printf("\r\033[KWorking: %d (%.1f%%) | HTTP: %d | SOCKS4: %d | SOCKS5: %d | Attempts: %d | Failed: %d\033[A",
					wrk, successRate, http, socks4, socks5, additional+chk, chk-wrk)
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
