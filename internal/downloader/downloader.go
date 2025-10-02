package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// maxDownloadSize limits proxy list downloads to 10MB to prevent memory exhaustion
	maxDownloadSize = 10 * 1024 * 1024
)

// HTTPDownloader implements domain.Downloader using HTTP
type HTTPDownloader struct {
	client *http.Client
}

// NewHTTPDownloader creates a new HTTP downloader with sensible defaults
func NewHTTPDownloader() *HTTPDownloader {
	return &HTTPDownloader{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Download fetches content from the given URL with size limits
func (d *HTTPDownloader) Download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	// Limit read size to prevent memory exhaustion from huge files
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return data, nil
}
