package scraper

import (
	"bufio"
	"bytes"
	"strings"

	"masss/internal/domain"
)

// DefaultParser parses standard IP:PORT format and handles protocol prefixes
// Supported formats:
//   - ip:port
//   - user:pass@ip:port
//   - ip:port:user:pass
//   - protocol://ip:port (strips protocol)
//   - protocol://user:pass@ip:port (strips protocol)
type DefaultParser struct{}

// NewDefaultParser creates a new default parser
func NewDefaultParser() *DefaultParser {
	return &DefaultParser{}
}

// Parse parses proxy data in various formats and normalizes to user:pass@ip:port or ip:port
func (p *DefaultParser) Parse(data []byte) ([]domain.Proxy, error) {
	var proxies []domain.Proxy
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse and normalize the proxy
		proxyInfo, err := domain.ParseProxyInfo(line)
		if err != nil {
			// Skip invalid entries silently
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
