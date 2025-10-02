package checker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"

	"masss/internal/domain"
)

// createHTTPClient creates an HTTP client configured for the given proxy and protocol
func createHTTPClient(proxyAddr string, protocol domain.Protocol, timeout time.Duration) (*http.Client, error) {
	switch protocol {
	case domain.ProtocolHTTP:
		return createHTTPProxyClient(proxyAddr, timeout)
	case domain.ProtocolSOCKS4:
		return createSOCKS4Client(proxyAddr, timeout)
	case domain.ProtocolSOCKS5:
		return createSOCKS5Client(proxyAddr, timeout)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

// createHTTPProxyClient creates a client for HTTP proxy with optional authentication
func createHTTPProxyClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	// Parse proxy to extract auth
	proxyInfo, err := domain.ParseProxyInfo(proxyAddr)
	if err != nil {
		return nil, err
	}

	// Build proxy URL with auth if present
	var proxyURL *url.URL
	if proxyInfo.HasAuth() {
		proxyURL = &url.URL{
			Scheme: "http",
			User:   url.UserPassword(proxyInfo.Username, proxyInfo.Password),
			Host:   net.JoinHostPort(proxyInfo.Host, proxyInfo.Port),
		}
	} else {
		proxyURL = &url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(proxyInfo.Host, proxyInfo.Port),
		}
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 0, // Disable keep-alive for immediate cleanup
			Resolver: &net.Resolver{
				PreferGo: true,
				Dial:     nil,
			},
		}).DialContext,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       1,
		IdleConnTimeout:       timeout,
		DisableKeepAlives:     true, // Force connection close after each request
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	return client, nil
}

// createSOCKS5Client creates a client for SOCKS5 proxy with optional authentication
func createSOCKS5Client(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	// Parse proxy to extract auth
	proxyInfo, err := domain.ParseProxyInfo(proxyAddr)
	if err != nil {
		return nil, err
	}

	var auth *proxy.Auth
	if proxyInfo.HasAuth() {
		auth = &proxy.Auth{
			User:     proxyInfo.Username,
			Password: proxyInfo.Password,
		}
	}

	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(proxyInfo.Host, proxyInfo.Port), auth, &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 0,
		Resolver: &net.Resolver{
			PreferGo: true,
		},
	})
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			MaxIdleConns:          1,
			MaxIdleConnsPerHost:   1,
			MaxConnsPerHost:       1,
			IdleConnTimeout:       timeout,
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}, nil
}

// createSOCKS4Client creates a client for SOCKS4 proxy with optional username
func createSOCKS4Client(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	// Parse proxy to extract auth
	proxyInfo, err := domain.ParseProxyInfo(proxyAddr)
	if err != nil {
		return nil, err
	}

	dialer := &socks4Dialer{
		proxyAddr: net.JoinHostPort(proxyInfo.Host, proxyInfo.Port),
		timeout:   timeout,
		username:  proxyInfo.Username, // SOCKS4 only supports username, not password
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			MaxIdleConns:          1,
			MaxIdleConnsPerHost:   1,
			MaxConnsPerHost:       1,
			IdleConnTimeout:       timeout,
			DisableKeepAlives:     true,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}, nil
}

// socks4Dialer implements a simple SOCKS4 dialer with optional username (IPv4 only)
type socks4Dialer struct {
	proxyAddr string
	timeout   time.Duration
	username  string
}

func (d *socks4Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   d.timeout,
		KeepAlive: 0,
		Resolver: &net.Resolver{
			PreferGo: true,
		},
	}

	conn, err := dialer.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, err
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close()
		return nil, err
	}

	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	// Resolve host to IPv4 only
	ips, err := net.LookupIP(host)
	if err != nil {
		conn.Close()
		return nil, err
	}

	var ip4 net.IP
	for _, ip := range ips {
		if ip4 = ip.To4(); ip4 != nil {
			break
		}
	}
	if ip4 == nil {
		conn.Close()
		return nil, fmt.Errorf("no IPv4 address for %s", host)
	}

	// Build SOCKS4 request: VER(1) CMD(1) PORT(2) IP(4) USERID(variable) NULL(1)
	req := make([]byte, 0, 128)
	req = append(req,
		4,                              // SOCKS version
		1,                              // CONNECT command
		byte(port>>8),                  // Port high byte
		byte(port&0xff),                // Port low byte
		ip4[0], ip4[1], ip4[2], ip4[3], // IP address
	)

	// Add username if present
	if d.username != "" {
		req = append(req, []byte(d.username)...)
	}
	req = append(req, 0) // Null terminator

	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Read response: VER(1) REP(1) PORT(2) IP(4)
	resp := make([]byte, 8)
	if _, err := conn.Read(resp); err != nil {
		conn.Close()
		return nil, err
	}

	if resp[1] != 90 { // 90 = request granted
		conn.Close()
		return nil, fmt.Errorf("socks4 connection failed: %d", resp[1])
	}

	return conn, nil
}
