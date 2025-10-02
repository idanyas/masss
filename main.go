package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"masss/internal/checker"
	"masss/internal/config"
	"masss/internal/domain"
	"masss/internal/downloader"
	"masss/internal/scraper"
	"masss/internal/storage"
)

func main() {
	// Load configuration
	cfg := config.Default()

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\n⚠ Interrupt received, shutting down gracefully...")
		fmt.Println("⚠ Working proxies found so far are already saved to files")
		cancel()
	}()

	// Initialize components
	httpDownloader := downloader.NewHTTPDownloader()
	proxyScraper := scraper.NewProxyScraper(httpDownloader)
	fileStorage := storage.NewFileStorage()

	// Configure sources - all formats supported (protocol prefixes stripped, auth preserved)
	// Note: All sources will have protocols stripped and output normalized to ip:port or user:pass@ip:port
	sources := []domain.ProxySource{
		// proxifly - mixed protocols with prefixes (http://, socks4://, socks5://) - strips protocol
		{
			URL:        "https://github.com/proxifly/free-proxy-list/raw/refs/heads/main/proxies/all/data.txt",
			ParserType: "default",
		},
		// ProxyScraper
		{
			URL:        "https://github.com/ProxyScraper/ProxyScraper/raw/refs/heads/main/http.txt",
			ParserType: "default",
		},
		{
			URL:        "https://github.com/ProxyScraper/ProxyScraper/raw/refs/heads/main/socks4.txt",
			ParserType: "default",
		},
		{
			URL:        "https://github.com/ProxyScraper/ProxyScraper/raw/refs/heads/main/socks5.txt",
			ParserType: "default",
		},
		// proxyscrape.com API
		{
			URL:        "https://api.proxyscrape.com/?request=displayproxies&status=alive&proxytype=http",
			ParserType: "default",
		},
		{
			URL:        "https://api.proxyscrape.com/?request=displayproxies&status=alive&proxytype=https",
			ParserType: "default",
		},
		{
			URL:        "https://api.proxyscrape.com/?request=displayproxies&status=alive&proxytype=socks4",
			ParserType: "default",
		},
		{
			URL:        "https://api.proxyscrape.com/?request=displayproxies&status=alive&proxytype=socks5",
			ParserType: "default",
		},
		// openproxylist.xyz
		{
			URL:        "https://openproxylist.xyz/http.txt",
			ParserType: "default",
		},
		{
			URL:        "https://openproxylist.xyz/socks4.txt",
			ParserType: "default",
		},
		{
			URL:        "https://openproxylist.xyz/socks5.txt",
			ParserType: "default",
		},
		// TheSpeedX
		{
			URL:        "https://github.com/TheSpeedX/PROXY-List/raw/refs/heads/master/http.txt",
			ParserType: "default",
		},
		{
			URL:        "https://github.com/TheSpeedX/PROXY-List/raw/refs/heads/master/socks4.txt",
			ParserType: "default",
		},
		{
			URL:        "https://github.com/TheSpeedX/PROXY-List/raw/refs/heads/master/socks5.txt",
			ParserType: "default",
		},
		// ShiftyTR
		{
			URL:        "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/http.txt",
			ParserType: "default",
		},
		{
			URL:        "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/https.txt",
			ParserType: "default",
		},
		{
			URL:        "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/socks4.txt",
			ParserType: "default",
		},
		{
			URL:        "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/socks5.txt",
			ParserType: "default",
		},
		// proxiesmaster
		{
			URL:        "https://raw.githubusercontent.com/proxiesmaster/Free-Proxy-List/main/proxies.txt",
			ParserType: "default",
		},
		// sunny9577
		{
			URL:        "https://raw.githubusercontent.com/sunny9577/proxy-scraper/master/generated/http_proxies.txt",
			ParserType: "default",
		},
		{
			URL:        "https://raw.githubusercontent.com/sunny9577/proxy-scraper/master/generated/socks4_proxies.txt",
			ParserType: "default",
		},
		{
			URL:        "https://raw.githubusercontent.com/sunny9577/proxy-scraper/master/generated/socks5_proxies.txt",
			ParserType: "default",
		},
		// hookzof
		{
			URL:        "https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt",
			ParserType: "default",
		},
	}

	// Add sources to scraper
	for _, source := range sources {
		proxyScraper.AddSource(source)
	}

	totalStart := time.Now()

	// === SCRAPING PHASE ===
	fmt.Println("═══ PHASE 1: SCRAPING ═══")
	fmt.Println()

	scrapeStart := time.Now()

	// Scrape all sources
	proxies, err := proxyScraper.Scrape(ctx)
	if err != nil {
		fmt.Printf("\n✗ Error during scraping: %v\n", err)
		os.Exit(1)
	}

	if len(proxies) == 0 {
		fmt.Println("\n✗ No proxies found")
		os.Exit(1)
	}

	scrapeElapsed := time.Since(scrapeStart)
	fmt.Printf("\n✓ Scraped %d unique proxies in %.2fs\n", len(proxies), scrapeElapsed.Seconds())

	// Sort proxies for consistent, organized output
	fmt.Print("Sorting proxies... ")
	sortStart := time.Now()
	domain.SortProxies(proxies)
	fmt.Printf("done in %.3fs\n", time.Since(sortStart).Seconds())

	// Save all proxies
	if err := fileStorage.Save(proxies, cfg.OutputFile); err != nil {
		fmt.Printf("✗ Error saving to file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Saved all proxies to %s (sorted by IP:port)\n\n", cfg.OutputFile)

	// === CHECKING PHASE ===
	fmt.Println("═══ PHASE 2: VALIDATION ═══")
	fmt.Println()

	checkStart := time.Now()

	// Initialize checker
	proxyChecker, err := checker.NewProxyChecker(cfg)
	if err != nil {
		fmt.Printf("✗ Error initializing checker: %v\n", err)
		os.Exit(1)
	}

	// Check all proxies (writes incrementally to protocol-specific files)
	workingProxiesByProtocol, err := proxyChecker.Check(ctx, proxies, cfg.OutputDir)
	if err != nil {
		fmt.Printf("✗ Error during validation: %v\n", err)
		os.Exit(1)
	}

	checkElapsed := time.Since(checkStart)

	// Sort working proxies by protocol (for consistent output in result files)
	domain.SortProxiesByProtocol(workingProxiesByProtocol)

	// Count total working
	totalWorking := 0
	for _, proxyList := range workingProxiesByProtocol {
		totalWorking += len(proxyList)
	}

	if totalWorking == 0 {
		fmt.Println("✗ No working proxies found")
		os.Exit(1)
	}

	// === SUMMARY ===
	totalElapsed := time.Since(totalStart)
	fmt.Println("═══ SUMMARY ═══")
	fmt.Printf("Total time: %.2fs\n", totalElapsed.Seconds())
	fmt.Printf("  Scraping: %.2fs\n", scrapeElapsed.Seconds())
	fmt.Printf("  Validation: %.2fs\n", checkElapsed.Seconds())
	fmt.Printf("\nProxies scraped: %d\n", len(proxies))
	fmt.Printf("Proxies working: %d (%.1f%%)\n", totalWorking,
		float64(totalWorking)/float64(len(proxies))*100)
	fmt.Printf("\n✓ Working proxies saved to %s/ directory\n", cfg.OutputDir)
}
