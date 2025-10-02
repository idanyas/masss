package domain

import (
	"net"
	"sort"
	"strconv"
)

// proxySortKey contains pre-parsed values for efficient sorting
type proxySortKey struct {
	ipNum    uint32 // IPv4 as uint32 for numeric comparison
	port     uint16
	validIP  bool   // false if IP parse failed
	original string // fallback for string comparison
}

// SortProxies sorts proxies by IP address (numerically) then by port.
// Handles formats: ip:port and user:pass@ip:port
// IPv4 addresses are compared numerically (1.2.3.4 comes before 192.168.1.1)
func SortProxies(proxies []Proxy) {
	if len(proxies) <= 1 {
		return
	}

	// Create combined items (proxy + sort key) so they stay together during sorting
	type sortItem struct {
		proxy Proxy
		key   proxySortKey
	}

	items := make([]sortItem, len(proxies))
	for i, proxy := range proxies {
		items[i] = sortItem{
			proxy: proxy,
			key:   parseProxyForSort(proxy),
		}
	}

	// Sort using pre-parsed keys
	sort.Slice(items, func(i, j int) bool {
		ki, kj := items[i].key, items[j].key

		// If both have valid IPs, compare numerically
		if ki.validIP && kj.validIP {
			if ki.ipNum != kj.ipNum {
				return ki.ipNum < kj.ipNum
			}
			// Same IP, compare by port
			return ki.port < kj.port
		}

		// If one or both invalid, fall back to string comparison
		return ki.original < kj.original
	})

	// Copy sorted proxies back to original slice
	for i, item := range items {
		proxies[i] = item.proxy
	}
}

// parseProxyForSort parses a proxy into sortable components
func parseProxyForSort(proxy Proxy) proxySortKey {
	str := string(proxy)
	key := proxySortKey{
		original: str,
		validIP:  false,
	}

	// Parse the proxy to extract IP and port
	info, err := ParseProxyInfo(str)
	if err != nil {
		return key
	}

	// Parse IP address
	ip := net.ParseIP(info.Host)
	if ip == nil {
		return key
	}

	// Convert to IPv4 (if it's IPv6-mapped IPv4, extract the IPv4 part)
	ip4 := ip.To4()
	if ip4 == nil {
		// IPv6 - fall back to string comparison (rare in proxy lists)
		return key
	}

	// Convert IPv4 to uint32 for numeric comparison
	key.ipNum = uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	key.validIP = true

	// Parse port
	if portNum, err := strconv.ParseUint(info.Port, 10, 16); err == nil {
		key.port = uint16(portNum)
	}

	return key
}

// SortProxiesByProtocol sorts a map of protocol->proxies
func SortProxiesByProtocol(proxyMap map[Protocol][]Proxy) {
	for _, proxies := range proxyMap {
		SortProxies(proxies)
	}
}

// SortProxyResultsByIP sorts ProxyResult slice by public_ip (numerically)
func SortProxyResultsByIP(results []ProxyResult) {
	if len(results) <= 1 {
		return
	}

	// Create combined items (result + parsed IP) so they stay together during sorting
	type sortItem struct {
		result ProxyResult
		ipNum  uint32
		valid  bool
	}

	items := make([]sortItem, len(results))
	for i, result := range results {
		item := sortItem{
			result: result,
			valid:  false,
		}

		// Parse public IP
		ip := net.ParseIP(result.PublicIP)
		if ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				item.ipNum = uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
				item.valid = true
			}
		}

		items[i] = item
	}

	// Sort by public IP numerically
	sort.Slice(items, func(i, j int) bool {
		// If both have valid IPs, compare numerically
		if items[i].valid && items[j].valid {
			return items[i].ipNum < items[j].ipNum
		}
		// Invalid IPs go to the end, compare by string
		if items[i].valid != items[j].valid {
			return items[i].valid
		}
		return items[i].result.PublicIP < items[j].result.PublicIP
	})

	// Copy sorted results back to original slice
	for i, item := range items {
		results[i] = item.result
	}
}
