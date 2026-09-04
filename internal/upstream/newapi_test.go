package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIsNewAPIAuthenticationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unauthorized", err: &HTTPError{System: "NewAPI", Status: http.StatusUnauthorized}, want: true},
		{name: "forbidden", err: &HTTPError{System: "NewAPI", Status: http.StatusForbidden}, want: true},
		{name: "json rejection", err: errors.New("NewAPI rejected request: session expired"), want: true},
		{name: "localized rejection", err: errors.New("NewAPI rejected request: 登录已过期"), want: true},
		{name: "other upstream", err: &HTTPError{System: "Sub2API", Status: http.StatusUnauthorized}, want: false},
		{name: "server error", err: &HTTPError{System: "NewAPI", Status: http.StatusBadGateway, Detail: "upstream unavailable"}, want: false},
		{name: "timeout", err: context.DeadlineExceeded, want: false},
		{name: "missing group", err: errors.New("NewAPI group default was not found"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNewAPIAuthenticationError(test.err); got != test.want {
				t.Fatalf("IsNewAPIAuthenticationError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestParseNewAPIGroups(t *testing.T) {
	rates := parseNewAPIGroups(map[string]any{"data": map[string]any{"groups": map[string]any{
		"default": map[string]any{"ratio": 1.0},
		"vip":     map[string]any{"rate_multiplier": 0.8},
		"auto":    map[string]any{"ratio": "automatic"},
	}}}, "/api/user/self/groups")
	if len(rates) != 2 {
		t.Fatalf("expected 2 rates, got %#v", rates)
	}
	fallback := parseNewAPIGroups(map[string]any{"group_ratio": map[string]any{
		"default": 1.2,
		"invalid": "+Inf",
	}}, "/api/pricing")
	if len(fallback) != 1 || fallback[0].Rate != 1.2 {
		t.Fatalf("unexpected fallback rates: %#v", fallback)
	}
	array := parseNewAPIGroups(map[string]any{"data": map[string]any{"groups": []any{
		map[string]any{"name": "vip", "rate": 0.7},
		map[string]any{"name": "vip", "rate": 0.6},
	}}}, "/api/user/self/groups")
	if len(array) != 1 || array[0].Group != "vip" || array[0].Rate != 0.7 {
		t.Fatalf("unexpected array rates: %#v", array)
	}
	root := parseNewAPIGroups(map[string]any{"default": map[string]any{"ratio": 1}}, "/api/user/self/groups")
	if len(root) != 1 || root[0].Group != "default" || root[0].Rate != 1 {
		t.Fatalf("unexpected root rates: %#v", root)
	}
}

func TestParseNewAPIDirectGroupMapWithDescriptions(t *testing.T) {
	rates := parseNewAPIGroups(map[string]any{"data": map[string]any{
		"codex-Plus": map[string]any{"desc": "plus pool", "ratio": 0.055},
		"default":    map[string]any{"desc": "default pool", "ratio": 0.6},
	}}, "/api/user/self/groups")
	if len(rates) != 2 {
		t.Fatalf("expected 2 direct group rates, got %#v", rates)
	}
	var found bool
	for _, rate := range rates {
		if rate.Group == "codex-Plus" {
			found = rate.Rate == 0.055 && rate.Description == "plus pool"
		}
	}
	if !found {
		t.Fatalf("direct group entry was not preserved: %#v", rates)
	}
}

func TestNumberRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), "NaN", "+Inf"} {
		if rate, ok := number(value); ok {
			t.Errorf("number(%v) = %v, true", value, rate)
		}
	}
	if rate, ok := number(2); !ok || rate != 2 {
		t.Fatalf("number(int) = %v, %v", rate, ok)
	}
}

func TestNormalizeNewAPICredential(t *testing.T) {
	tests := []struct {
		name          string
		credential    string
		userID        string
		authorization string
		cookie        string
		resolvedID    string
	}{
		{name: "token", credential: "pat-token", authorization: "Bearer pat-token"},
		{name: "padded token is not cookie", credential: "YWJjZA==", authorization: "Bearer YWJjZA=="},
		{name: "bearer", credential: "bearer pat-token", authorization: "Bearer pat-token"},
		{name: "raw session cookie", credential: "session=abc", cookie: "session=abc"},
		{name: "session prefix and embedded user", credential: "session:session=abc::42", cookie: "session=abc", resolvedID: "42"},
		{name: "session value prefix", credential: "session:abc", cookie: "session=abc"},
		{name: "cookie prefix", credential: "cookie: session=abc; other=one", cookie: "session=abc; other=one"},
		{name: "configured user", credential: "pat", userID: "7", authorization: "Bearer pat", resolvedID: "7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization, cookie, userID, err := normalizeNewAPICredential(test.credential, test.userID)
			if err != nil {
				t.Fatalf("normalizeNewAPICredential: %v", err)
			}
			if authorization != test.authorization || cookie != test.cookie || userID != test.resolvedID {
				t.Fatalf("got authorization=%q cookie=%q userID=%q", authorization, cookie, userID)
			}
		})
	}
}

func TestNormalizeNewAPICredentialRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		credential string
		userID     string
	}{
		{credential: ""},
		{credential: "token\r\nX-Injected: yes"},
		{credential: "cookie: malformed"},
		{credential: "session:"},
		{credential: "token::42", userID: "41"},
	} {
		if _, _, _, err := normalizeNewAPICredential(test.credential, test.userID); err == nil {
			t.Errorf("credential %q userID %q unexpectedly succeeded", test.credential, test.userID)
		}
	}
}

func TestNewAPIClientSelfGroupsContractWithCookie(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/self/groups" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Cookie") != "session=abc" || r.Header.Get("Authorization") != "" || r.Header.Get("New-Api-User") != "42" {
			t.Errorf("headers = %#v", r.Header)
		}
		if r.Header.Get("User-Agent") != newAPIBrowserUserAgent || r.Header.Get("Referer") != server.URL+"/keys" {
			t.Errorf("browser compatibility headers = %#v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"message": "",
			"data": map[string]any{
				"default": map[string]any{"ratio": 1, "desc": "default"},
				"vip":     map[string]any{"ratio": 0.8, "desc": "VIP"},
				"auto":    map[string]any{"ratio": "automatic", "desc": "automatic"},
			},
		})
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL+"/api/user/self/groups/", "session:session=abc::42", "", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	rates, err := client.ListGroupRates(context.Background())
	if err != nil {
		t.Fatalf("ListGroupRates: %v", err)
	}
	if len(rates) != 2 || rates[0].Group != "default" || rates[1].Group != "vip" || rates[1].Rate != 0.8 {
		t.Fatalf("rates = %#v", rates)
	}
}

func TestNewAPIClientBearerDoesNotAddBrowserHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-token" || r.Header.Get("User-Agent") == newAPIBrowserUserAgent || r.Header.Get("Referer") != "" {
			t.Errorf("headers = %#v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"groups": map[string]any{"default": 1}}})
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL, "pat-token", "", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	if _, err := client.ListGroupRates(context.Background()); err != nil {
		t.Fatalf("ListGroupRates: %v", err)
	}
}

func TestNewAPIClientPricingFallbackRequiresAuthentication(t *testing.T) {
	var mu sync.Mutex
	paths := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer pat-token" || r.Header.Get("New-Api-User") != "9" {
			t.Errorf("headers = %#v", r.Header)
		}
		switch r.URL.Path {
		case "/api/user/self/groups":
			http.NotFound(w, r)
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"id": 9, "group": "vip"}})
		case "/api/pricing":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "group_ratio": map[string]any{"default": 1, "vip": 0.6}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL+"/api/v1", "pat-token", "9", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	rates, err := client.ListGroupRates(context.Background())
	if err != nil {
		t.Fatalf("ListGroupRates: %v", err)
	}
	if len(rates) != 2 || rates[1].Endpoint != "/api/pricing" {
		t.Fatalf("rates = %#v", rates)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(paths, ",") != "/api/user/self/groups,/api/user/self,/api/pricing" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestNewAPIClientDoesNotFallbackOnAuthenticationFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL, "expired-token", "", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	if _, err := client.ListGroupRates(context.Background()); err == nil {
		t.Fatal("ListGroupRates unexpectedly succeeded")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestNewAPIClientResolveRateUsesCurrentGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self/groups":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
				"default": map[string]any{"ratio": 1}, "vip": map[string]any{"ratio": 0.5},
			}})
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"user": map[string]any{"group": "vip"}}})
		}
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL, "token", "", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	rate, err := client.ResolveRate(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveRate: %v", err)
	}
	if rate.Group != "vip" || rate.Rate != 0.5 {
		t.Fatalf("rate = %#v", rate)
	}
}

func TestNewAPIClientRejectsTrailingJSONAndSuccessFalse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "trailing", body: `{"success":true,"data":{}} {}`, want: "invalid JSON"},
		{name: "rejected", body: `{"success":false,"message":"session expired"}`, want: "session expired"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewNewAPIClient(server.URL, "token", "", server.Client())
			if err != nil {
				t.Fatalf("NewNewAPIClient: %v", err)
			}
			_, err = client.ListGroupRates(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
