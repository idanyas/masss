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
)

// WorkingProxy represents a proxy with its working protocol and public IP
type WorkingProxy struct {
	Proxy    domain.Proxy
	Protocol domain.Protocol
	PublicIP string
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
	fmt.Printf("Writing working proxies to: %s/\n\n", outputDir)

	start := time.Now()

	// Create job and result channels - minimal buffering to reduce memory
	jobs := make(chan domain.Proxy)
	workingProxies := make(chan WorkingProxy, 10)

	// Progress tracking
	var checked, workingTotal atomic.Int32
	var httpCount, socks4Count, socks5Count atomic.Int32

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
			c.worker(ctx, jobs, workingProxies, &checked, &workingTotal)
		}()
	}

	// Progress reporter
	progressDone := make(chan struct{})
	go c.progressReporter(&checked, &workingTotal, &httpCount, &socks4Count, &socks5Count, len(proxies), progressDone)

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

	// Sort and write all output files
	fmt.Print("\nSorting and writing results... ")
	sortStart := time.Now()

	// Sort and write protocol-specific text files (sorted by proxy IP:port)
	if err := c.writeSortedResults(savedProxies, outputDir); err != nil {
		return nil, fmt.Errorf("failed to write sorted results: %w", err)
	}

	// Sort and write JSON file (sorted by public IP)
	if err := c.writeSortedJSON(jsonResults); err != nil {
		fmt.Printf("\nWarning: Failed to write sorted JSON: %v\n", err)
	}

	fmt.Printf("done in %.3fs\n", time.Since(sortStart).Seconds())

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
	fmt.Printf("  Speed: %.0f proxies/second\n\n", float64(len(proxies))/elapsed.Seconds())

	// Per-protocol breakdown
	fmt.Println("Protocol breakdown:")
	for _, proto := range c.protocols {
		count := len(savedProxies[proto])
		if count > 0 {
			fmt.Printf("  ✓ %s: %d proxies → %s/%s.txt (sorted by IP:port)\n",
				strings.ToUpper(string(proto)), count, outputDir, proto)
		}
	}
	fmt.Printf("\n✓ JSON results saved to %s (sorted by public IP)\n", c.cfg.ResultJSONFile)
	fmt.Println()

	return savedProxies, nil
}

// writeSortedResults sorts and writes final protocol-specific files
func (c *ProxyChecker) writeSortedResults(savedProxies map[domain.Protocol][]domain.Proxy, outputDir string) error {
	for proto, proxies := range savedProxies {
		if len(proxies) == 0 {
			continue
		}

		// Sort proxies by IP:port
		domain.SortProxies(proxies)

		// Write sorted file
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

// writeSortedJSON sorts results by public IP and writes JSON file
func (c *ProxyChecker) writeSortedJSON(results []domain.ProxyResult) error {
	if len(results) == 0 {
		return nil
	}

	// Sort by public IP (numerically)
	domain.SortProxyResultsByIP(results)

	// Write formatted JSON
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(c.cfg.ResultJSONFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}

	return nil
}

// collectResults collects working proxies in memory only (no file writes during validation)
func (c *ProxyChecker) collectResults(workingProxies <-chan WorkingProxy, httpCount, socks4Count, socks5Count *atomic.Int32) (map[domain.Protocol][]domain.Proxy, []domain.ProxyResult) {
	proxies := make(map[domain.Protocol][]domain.Proxy)
	proxies[domain.ProtocolHTTP] = make([]domain.Proxy, 0)
	proxies[domain.ProtocolSOCKS4] = make([]domain.Proxy, 0)
	proxies[domain.ProtocolSOCKS5] = make([]domain.Proxy, 0)

	jsonResults := make([]domain.ProxyResult, 0)

	for wp := range workingProxies {
		// Store in memory by protocol
		proxies[wp.Protocol] = append(proxies[wp.Protocol], wp.Proxy)

		// Collect JSON results
		jsonResults = append(jsonResults, domain.ProxyResult{
			Protocol: string(wp.Protocol),
			Address:  string(wp.Proxy),
			PublicIP: wp.PublicIP,
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

	return proxies, jsonResults
}

// worker processes proxies from the job channel
func (c *ProxyChecker) worker(ctx context.Context, jobs <-chan domain.Proxy, workingProxies chan<- WorkingProxy, checked, working *atomic.Int32) {
	for proxy := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Check this proxy against all protocols
		if protocol, publicIP, ok := c.isProxyWorking(ctx, proxy); ok {
			workingProxies <- WorkingProxy{
				Proxy:    proxy,
				Protocol: protocol,
				PublicIP: publicIP,
			}
			working.Add(1)
		}
		checked.Add(1)
	}
}

// isProxyWorking tests if a proxy works with any protocol and returns the working protocol and public IP
func (c *ProxyChecker) isProxyWorking(ctx context.Context, proxy domain.Proxy) (domain.Protocol, string, bool) {
	// Test all protocols concurrently
	type result struct {
		protocol domain.Protocol
		publicIP string
	}
	resultChan := make(chan result, len(c.protocols))
	var wg sync.WaitGroup

	for _, protocol := range c.protocols {
		wg.Add(1)
		go func(proto domain.Protocol) {
			defer wg.Done()
			if publicIP, ok := c.checkProtocol(ctx, proxy, proto); ok {
				select {
				case resultChan <- result{protocol: proto, publicIP: publicIP}:
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
		return res.protocol, res.publicIP, true
	case <-done:
		return "", "", false
	case <-ctx.Done():
		return "", "", false
	}
}

// checkProtocol tests a proxy with a specific protocol against all endpoints
func (c *ProxyChecker) checkProtocol(ctx context.Context, proxy domain.Proxy, protocol domain.Protocol) (string, bool) {
	client, err := createHTTPClient(string(proxy), protocol, c.cfg.CheckTimeout)
	if err != nil {
		return "", false
	}
	// Explicitly close idle connections when done to free resources immediately
	defer client.CloseIdleConnections()

	// Try endpoints concurrently
	resultChan := make(chan string, len(c.cfg.CheckEndpoints))
	var wg sync.WaitGroup

	for _, endpoint := range c.cfg.CheckEndpoints {
		wg.Add(1)
		go func(ep string) {
			defer wg.Done()
			if publicIP, ok := c.checkEndpoint(ctx, client, ep); ok {
				select {
				case resultChan <- publicIP:
				default:
				}
			}
		}(endpoint)
	}

	// Wait with early exit on first success
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case publicIP := <-resultChan:
		return publicIP, true
	case <-done:
		return "", false
	case <-ctx.Done():
		return "", false
	}
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

	// Reject if same as host IP
	if _, isHostIP := c.hostIPs[returnedIP]; isHostIP {
		return "", false
	}

	return returnedIP, true
}

// progressReporter prints periodic progress updates with protocol breakdown
func (c *ProxyChecker) progressReporter(checked, working, httpCount, socks4Count, socks5Count *atomic.Int32, total int, done <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastChecked := int32(0)
	lastTime := time.Now()

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

			// Calculate rate
			elapsed := time.Since(lastTime).Seconds()
			rate := float64(chk-lastChecked) / elapsed
			lastChecked = chk
			lastTime = time.Now()

			if chk > 0 {
				remaining := total - int(chk)
				eta := 0.0
				if rate > 0 {
					eta = float64(remaining) / rate
				}

				percentage := float64(chk) / float64(total) * 100

				// Progress bar (20 chars wide)
				barWidth := 20
				filled := int(percentage / 100 * float64(barWidth))
				bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)

				// Line 1: Overall progress
				fmt.Printf("\r\033[KChecked: %d/%d (%.1f%%) [%s] %.0f/s | ETA: %.0fs\n",
					chk, total, percentage, bar, rate, eta)

				// Line 2: Protocol breakdown
				successRate := float64(0)
				if chk > 0 {
					successRate = float64(wrk) / float64(chk) * 100
				}
				fmt.Printf("\r\033[KWorking: %d (%.1f%%) | HTTP: %d | SOCKS4: %d | SOCKS5: %d | Failed: %d\033[A",
					wrk, successRate, http, socks4, socks5, chk-wrk)
			}
		}
	}
}
