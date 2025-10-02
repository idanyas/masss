package scraper

import (
	"bufio"
	"bytes"
	"strings"

	"masss/internal/domain"
)

// DefaultParser parses standard IP:PORT format and handles protocol prefixes
// Only accepts IPv4 addresses, filters out IPv6 during parsing
// Supported formats:
//   - ip:port (IPv4 only)
//   - user:pass@ip:port (IPv4 only)
//   - ip:port:user:pass (IPv4 only)
//   - protocol://ip:port (strips protocol, IPv4 only)
//   - protocol://user:pass@ip:port (strips protocol, IPv4 only)
type DefaultParser struct{}

// NewDefaultParser creates a new default parser
func NewDefaultParser() *DefaultParser {
	return &DefaultParser{}
}

// Parse parses proxy data in various formats and normalizes to user:pass@ip:port or ip:port
// Filters out IPv6 addresses early to avoid wasting resources
func (p *DefaultParser) Parse(data []byte) ([]domain.Proxy, error) {
	proxies := make([]domain.Proxy, 0, bytes.Count(data, []byte("\n")))
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse and normalize the proxy (ParseProxyInfo filters IPv6)
		proxyInfo, err := domain.ParseProxyInfo(line)
		if err != nil {
			// Skip invalid entries silently (includes IPv6)
			continue
		}

		// Convert back to normalized string format
		normalized := proxyInfo.String()
		proxies = append(proxies, domain.Proxy(normalized))
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return proxies, nil
}

// ParserFactory creates parsers based on type
type ParserFactory struct {
	parsers map[string]domain.Parser
}

// NewParserFactory creates a new parser factory with default parsers
func NewParserFactory() *ParserFactory {
	factory := &ParserFactory{
		parsers: make(map[string]domain.Parser),
	}

	// Register default parser
	factory.Register("default", NewDefaultParser())

	return factory
}

// Register adds a new parser type
func (f *ParserFactory) Register(name string, parser domain.Parser) {
	f.parsers[name] = parser
}

// Get retrieves a parser by type, returns default if not found
func (f *ParserFactory) Get(parserType string) domain.Parser {
	if parser, exists := f.parsers[parserType]; exists {
		return parser
	}
	return f.parsers["default"]
}
