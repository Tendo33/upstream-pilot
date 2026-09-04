package upstream

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root", raw: " https://example.com///?token=secret#fragment ", want: "https://example.com"},
		{name: "path", raw: "HTTPS://example.com/panel/", want: "https://example.com/panel"},
		{name: "port", raw: "http://example.com:8080/", want: "http://example.com:8080"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(test.raw)
			if err != nil {
				t.Fatalf("NormalizeBaseURL() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NormalizeBaseURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeBaseURLRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"", "example.com", "ftp://example.com", "https://user:pass@example.com", "http://"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NormalizeBaseURL(raw); err == nil {
				t.Fatalf("NormalizeBaseURL(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestBlockedIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{ip: "127.0.0.1", blocked: true},
		{ip: "0.0.0.1", blocked: true},
		{ip: "10.0.0.1", blocked: true},
		{ip: "100.64.0.1", blocked: true},
		{ip: "169.254.169.254", blocked: true},
		{ip: "192.0.2.1", blocked: true},
		{ip: "198.18.0.1", blocked: true},
		{ip: "2001:db8::1", blocked: true},
		{ip: "::127.0.0.1", blocked: true},
		{ip: "8.8.8.8", blocked: false},
		{ip: "2606:4700:4700::1111", blocked: false},
	}
	for _, test := range tests {
		if got := blockedIP(net.ParseIP(test.ip)); got != test.blocked {
			t.Errorf("blockedIP(%s) = %v, want %v", test.ip, got, test.blocked)
		}
	}
}

func TestHTTPClientPrivateAddressPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	response, err := NewHTTPClient(true).Get(server.URL)
	if err != nil {
		t.Fatalf("private-enabled client failed: %v", err)
	}
	response.Body.Close()

	_, err = NewHTTPClient(false).Get(server.URL)
	if err == nil || !strings.Contains(err.Error(), "private or non-routable") {
		t.Fatalf("private-restricted client error = %v", err)
	}
	transport := NewHTTPClient(false).Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("restricted transport must not permit proxy bypass")
	}
}

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.com")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	response, err := NewHTTPClient(true).Get(server.URL)
	if err != nil {
		t.Fatalf("GET redirect: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusFound)
	}
}

func TestReadResponseLimit(t *testing.T) {
	response := &http.Response{Body: http.NoBody}
	if _, err := readResponse(response); err != nil {
		t.Fatalf("empty response: %v", err)
	}
	response = &http.Response{Body: ioNopCloser{strings.NewReader(strings.Repeat("x", maxResponseBytes+1))}}
	if _, err := readResponse(response); err == nil {
		t.Fatal("oversized response unexpectedly succeeded")
	}
}

type ioNopCloser struct {
	*strings.Reader
}

func (ioNopCloser) Close() error { return nil }

func TestResponseErrorSanitizesDetail(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusBadGateway}
	err := responseError("test", http.MethodGet, "/path", response, []byte("first\r\nsecond\x00third"))
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T", err)
	}
	if httpErr.Detail != "first second third" {
		t.Fatalf("detail = %q", httpErr.Detail)
	}
}
