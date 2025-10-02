package scraper

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"masss/internal/domain"
)

// ProxyScraper orchestrates the proxy scraping process
type ProxyScraper struct {
	sources       []domain.ProxySource
	downloader    domain.Downloader
	parserFactory *ParserFactory
}

// NewProxyScraper creates a new scraper instance
func NewProxyScraper(downloader domain.Downloader) *ProxyScraper {
	return &ProxyScraper{
		sources:       make([]domain.ProxySource, 0),
		downloader:    downloader,
		parserFactory: NewParserFactory(),
	}
}

// AddSource adds a new proxy source
func (s *ProxyScraper) AddSource(source domain.ProxySource) {
	s.sources = append(s.sources, source)
}

// RegisterParser registers a custom parser
func (s *ProxyScraper) RegisterParser(name string, parser domain.Parser) {
	s.parserFactory.Register(name, parser)
}

// Scrape downloads and aggregates proxies from all sources
func (s *ProxyScraper) Scrape(ctx context.Context) ([]domain.Proxy, error) {
	if len(s.sources) == 0 {
		return nil, fmt.Errorf("no sources configured")
	}

	var wg sync.WaitGroup
	results := make(chan domain.SourceResult, len(s.sources))

	var completed, successful atomic.Int32

	fmt.Printf("Downloading from %d sources...\n", len(s.sources))

	// Progress updater
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				c := completed.Load()
				succ := successful.Load()
				if c > 0 {
					fmt.Printf("\rProgress: %d/%d sources completed (%d successful)", c, len(s.sources), succ)
				}
			}
		}
	}()

	// Download all sources concurrently
	for i, source := range s.sources {
		wg.Add(1)
		go func(idx int, src domain.ProxySource) {
			defer wg.Done()
			result := s.downloadSource(ctx, src, idx+1, len(s.sources))
			if result.Error == nil {
				successful.Add(1)
			}
			completed.Add(1)
			results <- result
		}(i, source)
	}

	// Wait for all downloads to complete
	wg.Wait()
	close(results)
	close(done)

	// Clear progress line
	fmt.Printf("\r\033[K")

	// Process results
	return s.aggregateResults(results), nil
}

// downloadSource downloads and parses a single source
func (s *ProxyScraper) downloadSource(ctx context.Context, source domain.ProxySource, sourceNum, total int) domain.SourceResult {
	result := domain.SourceResult{
		Source:    source,
		SourceNum: sourceNum,
		TotalSrcs: total,
	}

	start := time.Now()

	// Download
	data, err := s.downloader.Download(ctx, source.URL)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start).Seconds()
		return result
	}

	result.DataSize = len(data)

	// Parse
	parser := s.parserFactory.Get(source.ParserType)
	proxies, err := parser.Parse(data)
	if err != nil {
		result.Error = err
		result.Duration = time.Since(start).Seconds()
		return result
	}

	result.Proxies = proxies
	result.Count = len(proxies)
	result.Duration = time.Since(start).Seconds()

	// Clear data reference to allow GC
	data = nil

	return result
}

// aggregateResults deduplicates and combines all results
func (s *ProxyScraper) aggregateResults(results <-chan domain.SourceResult) []domain.Proxy {
	proxySet := make(map[domain.Proxy]struct{})
	totalDownloaded := 0
	successCount := 0
	failCount := 0

	for result := range results {
		if result.Error == nil {
			successCount++
			totalDownloaded += result.Count
			for _, proxy := range result.Proxies {
				proxySet[proxy] = struct{}{}
			}
			result.Proxies = nil
		} else {
			failCount++
		}
	}

	// Convert set to slice
	allProxies := make([]domain.Proxy, 0, len(proxySet))
	for proxy := range proxySet {
		allProxies = append(allProxies, proxy)
	}

	// Simple summary
	fmt.Printf("✓ Downloaded from %d/%d sources (%d failed)\n",
		successCount, successCount+failCount, failCount)
	fmt.Printf("  Total: %d | Unique: %d | Duplicates removed: %d\n",
		totalDownloaded, len(allProxies), totalDownloaded-len(allProxies))

	return allProxies
}

// getSourceName extracts a readable name from URL
func getSourceName(url string) string {
	parts := []rune(url)
	lastSlash := 0
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash > 0 && lastSlash < len(parts)-1 {
		return string(parts[lastSlash+1:])
	}
	return url
}
