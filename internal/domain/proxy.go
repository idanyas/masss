package domain

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// Proxy represents a proxy address
type Proxy string

// Protocol represents the proxy protocol type
type Protocol string

const (
	ProtocolHTTP   Protocol = "http"
	ProtocolSOCKS4 Protocol = "socks4"
	ProtocolSOCKS5 Protocol = "socks5"
)

// ProxySource represents a source URL and its parser type
type ProxySource struct {
	URL        string
	ParserType string // "default", "json", etc. - extensible
}

// Alias represents an alternative address for a proxy with the same public IP and protocol
type Alias struct {
	Address string `json:"address"`
}

// ProxyResult represents a validated proxy with its metadata for JSON output.
// It groups proxies with the same public IP and protocol using Aliases.
type ProxyResult struct {
	Protocol string  `json:"protocol"`
	Address  string  `json:"address"`
	PublicIP string  `json:"public_ip"`
	Aliases  []Alias `json:"aliases,omitempty"`
}

// ProxyInfo contains parsed proxy components
type ProxyInfo struct {
	Host     string // IP or hostname (IPv4 only)
	Port     string // Port number
	Username string // Optional username (empty if no auth)
	Password string // Optional password (empty if no auth)
}

// IsIPv4 checks if the given IP address is a valid IPv4 address
func IsIPv4(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// Check if it's a valid IPv4 (To4 returns nil for IPv6)
	return ip.To4() != nil
}

// ParseProxyInfo parses a proxy string into its components.
// Only accepts IPv4 addresses, rejects IPv6.
// Supports formats:
//   - ip:port
//   - user:pass@ip:port
//   - ip:port:user:pass (converted to user:pass@ip:port)
//   - protocol://ip:port (protocol stripped)
//   - protocol://user:pass@ip:port (protocol stripped)
func ParseProxyInfo(proxy string) (ProxyInfo, error) {
	original := proxy
	proxy = strings.TrimSpace(proxy)

	if proxy == "" {
		return ProxyInfo{}, fmt.Errorf("empty proxy string")
	}

	// Strip protocol prefixes (http://, https://, socks4://, socks5://)
	proxy = stripProtocol(proxy)

	var info ProxyInfo

	// Check for format: user:pass@host:port
	if strings.Contains(proxy, "@") {
		parts := strings.SplitN(proxy, "@", 2)
		if len(parts) != 2 {
			return ProxyInfo{}, fmt.Errorf("invalid proxy format: %s", original)
		}

		// Parse credentials
		authParts := strings.SplitN(parts[0], ":", 2)
		if len(authParts) == 2 {
			info.Username = authParts[0]
			info.Password = authParts[1]
		}

		// Parse host:port
		hostPort := parts[1]
		host, port, err := net.SplitHostPort(hostPort)
		if err != nil {
			return ProxyInfo{}, fmt.Errorf("invalid host:port in %s: %w", original, err)
		}

		// Reject IPv6 addresses
		if !IsIPv4(host) {
			return ProxyInfo{}, fmt.Errorf("only IPv4 addresses supported, got: %s", host)
		}

		info.Host = host
		info.Port = port

		return info, nil
	}

	// Check for format: host:port:user:pass (4 parts)
	parts := strings.Split(proxy, ":")
	if len(parts) == 4 {
		// Assume ip:port:user:pass
		host := parts[0]

		// Reject IPv6 addresses
		if !IsIPv4(host) {
			return ProxyInfo{}, fmt.Errorf("only IPv4 addresses supported, got: %s", host)
		}

		info.Host = host
		info.Port = parts[1]
		info.Username = parts[2]
		info.Password = parts[3]
		return info, nil
	}

	// Standard format: host:port
	if len(parts) >= 2 {
		host, port, err := net.SplitHostPort(proxy)
		if err != nil {
			return ProxyInfo{}, fmt.Errorf("invalid host:port format in %s: %w", original, err)
		}

		// Reject IPv6 addresses
		if !IsIPv4(host) {
			return ProxyInfo{}, fmt.Errorf("only IPv4 addresses supported, got: %s", host)
		}

		info.Host = host
		info.Port = port
		return info, nil
	}

	return ProxyInfo{}, fmt.Errorf("unrecognized proxy format: %s", original)
}

// String returns normalized proxy string (user:pass@host:port or host:port)
func (pi ProxyInfo) String() string {
	hostPort := net.JoinHostPort(pi.Host, pi.Port)
	if pi.Username != "" || pi.Password != "" {
		return fmt.Sprintf("%s:%s@%s", pi.Username, pi.Password, hostPort)
	}
	return hostPort
}

// HasAuth returns true if proxy has authentication credentials
func (pi ProxyInfo) HasAuth() bool {
	return pi.Username != "" || pi.Password != ""
}

// stripProtocol removes common protocol prefixes from proxy strings
func stripProtocol(proxy string) string {
	// Handle case-insensitive protocol stripping
	lower := strings.ToLower(proxy)

	prefixes := []string{"http://", "https://", "socks4://", "socks5://"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return proxy[len(prefix):]
		}
	}

	return proxy
}

// Downloader defines interface for downloading content from URLs
type Downloader interface {
	Download(ctx context.Context, url string) ([]byte, error)
}

// Parser defines interface for parsing proxy data
type Parser interface {
	Parse(data []byte) ([]Proxy, error)
}

// Storage defines interface for persisting proxies
type Storage interface {
	Save(proxies []Proxy, destination string) error
}

// Scraper defines interface for the main scraping operation
type Scraper interface {
	Scrape(ctx context.Context) ([]Proxy, error)
	AddSource(source ProxySource)
}

// SourceResult represents the result of downloading from a single source
type SourceResult struct {
	Source    ProxySource
	Proxies   []Proxy
	Error     error
	Count     int
	Duration  float64
	DataSize  int // Size of downloaded data in bytes
	SourceNum int
	TotalSrcs int
}
