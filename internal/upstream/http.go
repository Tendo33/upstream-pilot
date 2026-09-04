package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBytes   = 2 << 20
	maxCredentialBytes = 16 << 10
	maxErrorRunes      = 500
)

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), // deprecated 6to4 relay anycast
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),        // deprecated IPv4-compatible addresses
	netip.MustParsePrefix("100::/64"),     // discard-only
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can otherwise obscure the IPv4 destination
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), // IETF protocol assignments and transition mechanisms
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds an IPv4 destination
	netip.MustParsePrefix("fec0::/10"), // deprecated IPv6 site-local range
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("invalid upstream URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("invalid upstream URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("upstream URL must use http or https")
	}
	if u.Opaque != "" {
		return "", errors.New("invalid upstream URL")
	}
	if u.User != nil {
		return "", errors.New("upstream URL must not contain credentials")
	}
	u.Scheme = scheme
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	if u.RawPath != "" {
		u.RawPath = strings.TrimRight(u.RawPath, "/")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func NewHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DialContext = dialer.DialContext
	if !allowPrivate {
		// A forward proxy can connect to an otherwise blocked destination, so the
		// restricted client deliberately bypasses proxy environment variables.
		transport.Proxy = nil
		transport.DialContext = restrictedDialContext(dialer, net.DefaultResolver)
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return true
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func restrictedDialContext(dialer *net.Dialer, resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve upstream host %q: %w", host, err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("upstream host %q resolved to no addresses", host)
		}

		// Validate the complete answer set before dialing. Dialing the validated
		// literal address pins this request to the DNS result and closes the
		// resolve-then-redial DNS rebinding window.
		for _, resolved := range addresses {
			if blockedIP(resolved.IP) {
				return nil, fmt.Errorf("upstream address %s is private or non-routable", resolved.String())
			}
		}
		var lastErr error
		for _, resolved := range addresses {
			resolvedHost := resolved.IP.String()
			if resolved.Zone != "" {
				resolvedHost += "%" + resolved.Zone
			}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolvedHost, port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("dial upstream host %q: %w", host, lastErr)
	}
}

func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("upstream response exceeds 2 MiB")
	}
	return body, nil
}

func responseError(system, method, path string, response *http.Response, body []byte) error {
	detail := compactUntrustedText(string(body), maxErrorRunes)
	return &HTTPError{System: system, Status: response.StatusCode, Method: method, Path: path, Detail: detail}
}

func compactUntrustedText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func validateHeaderValue(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(value) > maxCredentialBytes {
		return fmt.Errorf("%s is too long", label)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains invalid control characters", label)
		}
	}
	return nil
}

func defaultHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return NewHTTPClient(false)
}

func trimURLSuffix(baseURL string, suffixes ...string) string {
	for _, suffix := range suffixes {
		if strings.HasSuffix(baseURL, suffix) {
			return strings.TrimSuffix(baseURL, suffix)
		}
	}
	return baseURL
}

type HTTPError struct {
	System string
	Status int
	Method string
	Path   string
	Detail string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s HTTP %d for %s %s", e.System, e.Status, e.Method, e.Path)
}

func (e *HTTPError) StatusCode() int { return e.Status }

func (e *HTTPError) FailureDetail() string { return e.Detail }
