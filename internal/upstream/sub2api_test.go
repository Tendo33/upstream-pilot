package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func newSub2TestClient(t *testing.T, handler http.HandlerFunc, urlSuffix string) (*Sub2Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := NewSub2Client(server.URL+urlSuffix, " admin-key ", server.Client())
	if err != nil {
		server.Close()
		t.Fatalf("NewSub2Client: %v", err)
	}
	return client, server
}

func writeSub2Envelope(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": data}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestSub2ClientNormalizesAdminEndpointAndHeaders(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/groups/all" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		writeSub2Envelope(t, w, []map[string]any{{"id": 4, "name": "default", "rate_multiplier": 1.25}})
	}, "/api/v1/admin/")
	defer server.Close()

	groups, err := client.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != 4 || groups[0].RateMultiplier == nil || *groups[0].RateMultiplier != 1.25 {
		t.Fatalf("groups = %#v", groups)
	}
}

func TestSub2ClientListAccountsFollowsReportedTotal(t *testing.T) {
	var mu sync.Mutex
	pages := make([]int, 0, 2)
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		if r.URL.Query().Get("page_size") != "200" || r.URL.Query().Get("include_scheduler_score") != "false" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		items := []map[string]any{}
		if page == 1 {
			// Simulate an older/forked server that clamps the requested page size.
			items = []map[string]any{{"id": 1, "name": "one", "credentials": nil}, {"id": 2, "name": "two", "credentials": nil}}
		} else if page == 2 {
			items = []map[string]any{{"id": 3, "name": "three", "credentials": nil}}
		}
		writeSub2Envelope(t, w, map[string]any{"items": items, "total": 3, "page": page, "page_size": 2})
	}, "")
	defer server.Close()

	accounts, err := client.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 3 || accounts[2].ID != 3 {
		t.Fatalf("accounts = %#v", accounts)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(pages) != "[1 2]" {
		t.Fatalf("pages = %v", pages)
	}
}

func TestSub2ClientObservesCredentialBaseURLsAndUsesExportFallback(t *testing.T) {
	const secret = "exported-api-key-must-not-escape"
	exportRequests := 0
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			writeSub2Envelope(t, w, map[string]any{"items": []map[string]any{
				{"id": 1, "name": "omitted"},
				{"id": 2, "name": "explicit-null", "credentials": nil},
				{"id": 3, "name": "inline", "credentials": map[string]any{"baseUrl": "https://inline.example/v1/?token=drop#fragment", "api_key": secret}},
				{"id": 4, "name": "invalid", "credentials": map[string]any{"base_url": "ftp://invalid.example", "api_key": secret}},
			}, "total": 4})
		case "/api/v1/admin/accounts/data":
			exportRequests++
			if r.URL.Query().Get("ids") != "1" || r.URL.Query().Get("include_proxies") != "false" {
				t.Errorf("export query = %q", r.URL.RawQuery)
			}
			writeSub2Envelope(t, w, map[string]any{"data": map[string]any{"accounts": []map[string]any{
				{"account_id": "1", "credentials": map[string]any{"base_url": " https://fallback.example/v1/?api_key=drop#fragment ", "api_key": secret}},
			}}})
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}, "")
	defer server.Close()

	accounts, err := client.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if exportRequests != 1 {
		t.Fatalf("export requests = %d", exportRequests)
	}
	if len(accounts) != 4 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if !accounts[0].ObservedSourceBaseURLKnown || accounts[0].ObservedSourceBaseURL == nil || *accounts[0].ObservedSourceBaseURL != "https://fallback.example/v1" {
		t.Fatalf("fallback observation = %#v", accounts[0])
	}
	if !accounts[1].SourceCredentialsPresent || !accounts[1].ObservedSourceBaseURLKnown || accounts[1].ObservedSourceBaseURL != nil {
		t.Fatalf("explicit null observation = %#v", accounts[1])
	}
	if !accounts[1].ObservedSourceCredentialFingerprintKnown || accounts[1].ObservedSourceCredentialFingerprint != "" {
		t.Fatalf("explicit null credential must clear the fingerprint: %#v", accounts[1])
	}
	if !accounts[2].ObservedSourceBaseURLKnown || accounts[2].ObservedSourceBaseURL == nil || *accounts[2].ObservedSourceBaseURL != "https://inline.example/v1" {
		t.Fatalf("inline observation = %#v", accounts[2])
	}
	if !accounts[3].SourceCredentialsPresent || accounts[3].ObservedSourceBaseURLKnown {
		t.Fatalf("invalid observation must remain unknown: %#v", accounts[3])
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("api_key")) || bytes.Contains(encoded, []byte("credentials")) {
		t.Fatalf("serialized accounts leaked credentials: %s", encoded)
	}
	if !accounts[2].ObservedSourceCredentialFingerprintKnown || accounts[2].ObservedSourceCredentialFingerprint == "" || accounts[2].ObservedSourceCredentialFingerprint == secret {
		t.Fatalf("credential fingerprint was not captured safely: %#v", accounts[2])
	}
}

func TestSub2ClientMissingExportEndpointKeepsObservationUnknown(t *testing.T) {
	const secret = "error-body-api-key-must-not-escape"
	exportRequests := 0
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			items := make([]map[string]any, 201)
			for index := range items {
				items[index] = map[string]any{"id": index + 1, "name": "omitted"}
			}
			writeSub2Envelope(t, w, map[string]any{"items": items, "total": len(items)})
		case "/api/v1/admin/accounts/data":
			exportRequests++
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"api_key":%q}`, secret)
		default:
			http.NotFound(w, r)
		}
	}, "")
	defer server.Close()

	accounts, err := client.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("optional export failure blocked inventory: %v", err)
	}
	if len(accounts) != 201 || accounts[0].ObservedSourceBaseURLKnown || accounts[0].ObservedSourceBaseURL != nil {
		t.Fatalf("observation should remain unknown: %#v", accounts)
	}
	if exportRequests != 1 {
		t.Fatalf("missing optional export endpoint was retried %d times", exportRequests)
	}
	encoded, _ := json.Marshal(accounts)
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("serialized accounts leaked export error body: %s", encoded)
	}
}

func TestSub2ClientExportFallbackContinuesAfterTransientBatchFailure(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			items := make([]map[string]any, 250)
			for index := range items {
				items[index] = map[string]any{"id": index + 1, "name": fmt.Sprintf("account-%d", index+1)}
			}
			writeSub2Envelope(t, w, map[string]any{"items": items, "total": len(items)})
		case "/api/v1/admin/accounts/data":
			ids := strings.Split(r.URL.Query().Get("ids"), ",")
			if len(ids) > 0 && ids[0] == "1" {
				http.Error(w, "temporary failure", http.StatusBadGateway)
				return
			}
			exported := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				exported = append(exported, map[string]any{"account_id": id, "credentials": map[string]any{"base_url": "https://source.example/" + id}})
			}
			writeSub2Envelope(t, w, map[string]any{"accounts": exported})
		default:
			http.NotFound(w, r)
		}
	}, "")
	defer server.Close()

	accounts, err := client.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if accounts[0].ObservedSourceBaseURLKnown {
		t.Fatalf("failed first batch unexpectedly produced an observation: %#v", accounts[0])
	}
	for _, index := range []int{100, 200, 249} {
		if !accounts[index].ObservedSourceBaseURLKnown || accounts[index].ObservedSourceBaseURL == nil {
			t.Fatalf("later account %d was starved: %#v", index+1, accounts[index])
		}
	}
}

func TestObserveSourceBaseURLValidation(t *testing.T) {
	longURL := "https://example.test/" + strings.Repeat("a", maxObservedSourceURLBytes)
	tests := []struct {
		name  string
		raw   string
		known bool
		want  string
	}{
		{name: "null", raw: `null`, known: true},
		{name: "empty object", raw: `{}`, known: true},
		{name: "snake case", raw: `{"base_url":"https://example.test/root/?token=drop#fragment"}`, known: true, want: "https://example.test/root"},
		{name: "camel case", raw: `{"baseUrl":"http://example.test/"}`, known: true, want: "http://example.test"},
		{name: "empty value", raw: `{"base_url":"  "}`, known: true},
		{name: "unsupported scheme", raw: `{"base_url":"ftp://example.test"}`},
		{name: "userinfo", raw: `{"base_url":"https://user:secret@example.test"}`},
		{name: "non object", raw: `"https://example.test"`},
		{name: "too long", raw: fmt.Sprintf(`{"base_url":%q}`, longURL)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			known, value := observeSourceBaseURL(json.RawMessage(test.raw))
			if known != test.known {
				t.Fatalf("known = %v, want %v", known, test.known)
			}
			if test.want == "" {
				if value != nil {
					t.Fatalf("value = %q, want nil", *value)
				}
			} else if value == nil || *value != test.want {
				t.Fatalf("value = %v, want %q", value, test.want)
			}
		})
	}
}

func TestSub2ClientRejectsMalformedList(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSub2Envelope(t, w, map[string]any{"unexpected": true})
	}, "")
	defer server.Close()
	if _, err := client.ListGroups(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected upstream list") {
		t.Fatalf("ListGroups error = %v", err)
	}
}

func TestSub2ClientRejectsNullList(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("null"))
	}, "")
	defer server.Close()
	if _, err := client.ListGroups(context.Background()); err == nil {
		t.Fatal("null list unexpectedly succeeded")
	}
}

func TestSub2ClientSupportsLegacyDirectJSON(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/groups/all":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "name": "legacy"}})
		case "/api/v1/admin/system/version":
			_ = json.NewEncoder(w).Encode("0.1.2")
		default:
			http.NotFound(w, r)
		}
	}, "")
	defer server.Close()
	groups, err := client.ListGroups(context.Background())
	if err != nil || len(groups) != 1 || groups[0].Name != "legacy" {
		t.Fatalf("ListGroups = %#v, %v", groups, err)
	}
	if version := client.Version(context.Background()); version != "0.1.2" {
		t.Fatalf("Version = %q", version)
	}
}

func TestSub2ClientUpdateAccountContract(t *testing.T) {
	priority := 999
	rate := 0.75
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/admin/accounts/42" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body AccountUpdate
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Priority == nil || *body.Priority != priority || body.RateMultiplier == nil || *body.RateMultiplier != rate {
			t.Errorf("body = %#v", body)
		}
		writeSub2Envelope(t, w, map[string]any{"id": 42, "priority": priority, "rate_multiplier": rate, "schedulable": true})
	}, "")
	defer server.Close()

	account, err := client.UpdateAccount(context.Background(), 42, AccountUpdate{Priority: &priority, RateMultiplier: &rate})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if account.Priority != priority || account.RateMultiplier == nil || *account.RateMultiplier != rate {
		t.Fatalf("account = %#v", account)
	}
}

func TestSub2ClientRejectsInvalidUpdatesBeforeRequest(t *testing.T) {
	client, server := newSub2TestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("unexpected request")
	}, "")
	defer server.Close()
	if _, err := client.UpdateAccount(context.Background(), 1, AccountUpdate{}); err == nil {
		t.Fatal("empty update unexpectedly succeeded")
	}
	invalid := math.Inf(1)
	if _, err := client.UpdateAccount(context.Background(), 1, AccountUpdate{RateMultiplier: &invalid}); err == nil {
		t.Fatal("infinite rate unexpectedly succeeded")
	}
}

func TestSub2ClientRejectsMismatchedAccountResponse(t *testing.T) {
	priority := 1
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSub2Envelope(t, w, map[string]any{"id": 2, "priority": priority})
	}, "")
	defer server.Close()
	if _, err := client.UpdateAccount(context.Background(), 1, AccountUpdate{Priority: &priority}); err == nil || !strings.Contains(err.Error(), "mismatched account ID") {
		t.Fatalf("UpdateAccount error = %v", err)
	}
}

func TestSub2ClientParsesRealAccountTestSSE(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/7/test" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["model_id"] != "gpt-5.4" {
			t.Errorf("model_id = %q", body["model_id"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"test_start\",\"model\":\"gpt-5.4\"}\n\n" +
			"data: {\"type\":\"content\",\"text\":\"ok\"}\n\n" +
			"data: {\"type\":\"test_complete\",\"success\":true}\n\n"))
	}, "")
	defer server.Close()

	result, err := client.TestAccount(context.Background(), 7, " gpt-5.4 ")
	if err != nil {
		t.Fatalf("TestAccount: %v", err)
	}
	if !result.Success || result.Model != "gpt-5.4" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSub2ClientTreatsEnvelopeFailureAsError(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 422, "message": "model unavailable"})
	}, "")
	defer server.Close()
	if _, err := client.TestAccount(context.Background(), 7, "gpt-test"); err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("TestAccount error = %v", err)
	}
}

func TestSub2ClientProbeBillingRealEnvelope(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/9/upstream-billing-probe" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeSub2Envelope(t, w, map[string]any{
			"account_id": 9,
			"snapshot": map[string]any{
				"status": "ok",
				"data":   map[string]any{"effective_rate_multiplier": 0.625},
			},
		})
	}, "")
	defer server.Close()

	result, err := client.ProbeBilling(context.Background(), 9)
	if err != nil {
		t.Fatalf("ProbeBilling: %v", err)
	}
	if result.Status != "ok" || result.EffectiveRateMultiplier != 0.625 || result.Endpoint != "/accounts/9/upstream-billing-probe" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSub2ClientProbeBillingRequiresEffectiveRate(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSub2Envelope(t, w, map[string]any{"snapshot": map[string]any{"status": "ok", "data": map[string]any{}}})
	}, "")
	defer server.Close()
	if _, err := client.ProbeBilling(context.Background(), 9); err == nil || !strings.Contains(err.Error(), "effective_rate_multiplier") {
		t.Fatalf("ProbeBilling error = %v", err)
	}
}

func TestSub2ClientProbeBillingRejectsMismatchedAccount(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSub2Envelope(t, w, map[string]any{
			"account_id": 10,
			"snapshot":   map[string]any{"status": "ok", "data": map[string]any{"effective_rate_multiplier": 1}},
		})
	}, "")
	defer server.Close()
	if _, err := client.ProbeBilling(context.Background(), 9); err == nil || !strings.Contains(err.Error(), "mismatched account ID") {
		t.Fatalf("ProbeBilling error = %v", err)
	}
}

func TestSub2HTTPErrorPreservesStatusWithoutLeakingBodyInErrorString(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret upstream detail", http.StatusUnauthorized)
	}, "")
	defer server.Close()
	_, err := client.ListGroups(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "secret upstream detail") || httpErr.Detail != "secret upstream detail" {
		t.Fatalf("unexpected error detail behavior: %q / %q", err.Error(), httpErr.Detail)
	}
}

func TestSub2ClientAccountUsageStatsReadsTodayWindow(t *testing.T) {
	client, server := newSub2TestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/usage/stats" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("account_id") != "101" || r.URL.Query().Get("period") != "today" || r.URL.Query().Get("nocache") != "true" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		writeSub2Envelope(t, w, map[string]any{
			"total_requests":              4,
			"total_input_tokens":          10,
			"total_output_tokens":         2,
			"total_cache_creation_tokens": 5,
			"total_cache_read_tokens":     85,
		})
	}, "")
	defer server.Close()

	stats, err := client.AccountUsageStats(context.Background(), 101)
	if err != nil {
		t.Fatalf("AccountUsageStats: %v", err)
	}
	if stats.TotalInputTokens != 10 || stats.TotalCacheCreationTokens != 5 || stats.TotalCacheReadTokens != 85 {
		t.Fatalf("stats = %#v", stats)
	}
}
