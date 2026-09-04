package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryUsageBalanceUsesBearerAndNormalizesV1URL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer account-key" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"remaining": 12.5, "unit": "USD"})
	}))
	defer server.Close()

	result, err := QueryUsageBalance(context.Background(), server.URL+"/v1", " account-key ", server.Client())
	if err != nil {
		t.Fatalf("QueryUsageBalance: %v", err)
	}
	if result.Status != "ok" || result.Provider != "usage" || result.Remaining == nil || *result.Remaining != 12.5 || result.Unit != "USD" {
		t.Fatalf("result = %#v", result)
	}
}

func TestQueryUsageBalanceReportsInvalidAndUnsupportedResponses(t *testing.T) {
	tests := []struct {
		name       string
		payload    map[string]any
		wantStatus string
	}{
		{name: "inactive", payload: map[string]any{"is_active": false, "message": "disabled"}, wantStatus: "invalid"},
		{name: "missing balance", payload: map[string]any{"ok": true}, wantStatus: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(test.payload)
			}))
			defer server.Close()
			result, err := QueryUsageBalance(context.Background(), server.URL, "key", server.Client())
			if err != nil {
				t.Fatalf("QueryUsageBalance: %v", err)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, test.wantStatus)
			}
		})
	}
}

func TestSub2ClientAccountUsageCredentialsExtractsOnlyRequiredFields(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/data" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("ids") != "7,8" || r.URL.Query().Get("include_proxies") != "false" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		writeSub2Envelope(t, w, map[string]any{"accounts": []map[string]any{
			{"account_id": "7", "credentials": map[string]any{"config": map[string]any{"baseUrl": "https://usage.example/v1", "apiKey": "secret-7"}}},
			{"account_id": 8, "credentials": map[string]any{"base_url": "https://missing-key.example"}},
		}})
	}, "")
	defer server.Close()

	credentials, err := client.AccountUsageCredentials(context.Background(), []int64{7, 8, 7})
	if err != nil {
		t.Fatalf("AccountUsageCredentials: %v", err)
	}
	if len(credentials) != 1 || credentials[7].BaseURL != "https://usage.example/v1" || credentials[7].APIKey != "secret-7" {
		t.Fatalf("credentials = %#v", credentials)
	}
	encoded, err := json.Marshal(credentials[7])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("serialized credential = %s", encoded)
	}
}

func TestSub2ClientAccountUsageCredentialsMapsCurrentSingleAccountExport(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/data" || r.URL.Query().Get("ids") != "17" {
			t.Errorf("request = %s", r.URL.String())
		}
		// Current Sub2API DataAccount intentionally has no id/account_id field.
		writeSub2Envelope(t, w, map[string]any{"accounts": []map[string]any{{
			"name": "current export", "platform": "openai", "type": "apikey",
			"credentials": map[string]any{"provider": map[string]any{"serialized": `{"config":{"baseUrl":"https://usage.example/v1","apiKey":"secret-17"}}`}},
		}}})
	}, "")
	defer server.Close()

	credentials, err := client.AccountUsageCredentials(context.Background(), []int64{17})
	if err != nil {
		t.Fatalf("AccountUsageCredentials: %v", err)
	}
	if len(credentials) != 1 || credentials[17].BaseURL != "https://usage.example/v1" || credentials[17].APIKey != "secret-17" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestSub2ClientAccountUsageBalanceUsesPassiveAdminSnapshot(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/7/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("source") != "passive" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		writeSub2Envelope(t, w, map[string]any{
			"five_hour": map[string]any{"utilization": 25},
		})
	}, "")
	defer server.Close()

	result, err := client.AccountUsageBalance(context.Background(), 7)
	if err != nil {
		t.Fatalf("AccountUsageBalance: %v", err)
	}
	if result.Status != "ok" || result.Provider != "sub2api-admin" || result.PlanName != "5h" || result.Endpoint != "/accounts/7/usage" || result.Unit != "%" {
		t.Fatalf("result = %#v", result)
	}
	if result.Remaining == nil || *result.Remaining != 75 || result.Used == nil || *result.Used != 25 || result.Total == nil || *result.Total != 100 {
		t.Fatalf("quota = %#v", result)
	}
}

func TestParseSub2APIUsageBalanceRequestQuotaAndCredits(t *testing.T) {
	requestResult, ok := parseSub2APIUsageBalance(map[string]any{
		"seven_day": map[string]any{"utilization": 40, "used_requests": 8, "limit_requests": 20},
	})
	if !ok || requestResult.Unit != "req" || requestResult.Remaining == nil || *requestResult.Remaining != 12 || requestResult.Used == nil || *requestResult.Used != 8 || requestResult.Total == nil || *requestResult.Total != 20 {
		t.Fatalf("request result = %#v, ok = %t", requestResult, ok)
	}

	creditResult, ok := parseSub2APIUsageBalance(map[string]any{"ai_credits": []any{
		map[string]any{"amount": 3.5}, map[string]any{"amount": "2.25"},
	}})
	if !ok || creditResult.Unit != "credits" || creditResult.Remaining == nil || *creditResult.Remaining != 5.75 {
		t.Fatalf("credit result = %#v, ok = %t", creditResult, ok)
	}
}

func TestNewAPIClientBalanceFallsBackAndConvertsQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("New-Api-User") != "9" {
			t.Errorf("headers = %#v", r.Header)
		}
		switch r.URL.Path {
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"quota_per_unit": 1000}})
		case "/api/subscription/self":
			http.NotFound(w, r)
		case "/api/user/self":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
				"quota": 250, "used_quota": 100, "group": "vip",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewNewAPIClient(server.URL+"/api/v1", "token", "9", server.Client())
	if err != nil {
		t.Fatalf("NewNewAPIClient: %v", err)
	}
	result, err := client.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if result.Status != "ok" || result.Endpoint != "/api/user/self" || result.PlanName != "vip" {
		t.Fatalf("result = %#v", result)
	}
	if result.Remaining == nil || *result.Remaining != 0.25 || result.Used == nil || *result.Used != 0.1 || result.Total == nil || *result.Total != 0.35 {
		t.Fatalf("converted quota = %#v", result)
	}
}

func TestParseNewAPIBalanceSubscriptions(t *testing.T) {
	result, ok := parseNewAPIBalance(map[string]any{"data": map[string]any{"subscriptions": []any{
		map[string]any{"subscription": map[string]any{"amount_total": 1000, "amount_used": 250}, "plan": map[string]any{"title": "Pro"}},
		map[string]any{"amount_remaining": 125},
	}}}, 500)
	if !ok || result.Remaining == nil || *result.Remaining != 1.75 || result.Used == nil || *result.Used != 0.5 || result.Total == nil || *result.Total != 2 || result.PlanName != "Pro" {
		t.Fatalf("result = %#v, ok = %t", result, ok)
	}
}
