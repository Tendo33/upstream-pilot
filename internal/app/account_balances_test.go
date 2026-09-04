package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/langrenjh-alt/S2AM-GO/internal/secret"
)

func TestNormalizeBalanceAccountIDs(t *testing.T) {
	first := uuid.NewString()
	second := uuid.NewString()
	ids, apiErr := normalizeBalanceAccountIDs([]string{" " + first + " ", second, first})
	if apiErr != nil {
		t.Fatalf("normalizeBalanceAccountIDs: %v", apiErr)
	}
	if len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("ids = %#v", ids)
	}

	if _, apiErr := normalizeBalanceAccountIDs([]string{"invalid"}); apiErr == nil || apiErr.Code != "INVALID_ACCOUNT_ID" {
		t.Fatalf("invalid ID error = %#v", apiErr)
	}

	tooMany := make([]string, maxBalanceAccountIDs+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
	}
	if _, apiErr := normalizeBalanceAccountIDs(tooMany); apiErr == nil || apiErr.Code != "TOO_MANY_ACCOUNTS" {
		t.Fatalf("too many error = %#v", apiErr)
	}
}

func TestQueryAccountBalancePrefersSub2APIAdminUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/7/usage" || r.URL.Query().Get("source") != "passive" {
			t.Errorf("request = %s", r.URL.String())
		}
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"five_hour": map[string]any{"utilization": 12.5},
		}})
	}))
	defer server.Close()

	application, work := newBalanceTestApp(t, server, 7)
	result := application.queryAccountBalance(context.Background(), work)
	if result.Status != "ok" || result.Provider != "sub2api-admin" || result.Remaining == nil || *result.Remaining != 87.5 {
		t.Fatalf("result = %#v", result)
	}
}

func TestQueryAccountBalanceFallsBackToCurrentSub2APIExport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts/9/usage":
			http.NotFound(w, r)
		case "/api/v1/admin/accounts/data":
			if r.URL.Query().Get("ids") != "9" || r.URL.Query().Get("include_proxies") != "false" {
				t.Errorf("export query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accounts": []map[string]any{{
				"name": "no ID by design", "credentials": map[string]any{"base_url": serverURL(r) + "/source", "api_key": "account-key"},
			}}}})
		case "/source/v1/usage":
			if r.Header.Get("Authorization") != "Bearer account-key" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"remaining": 42, "unit": "USD"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	application, work := newBalanceTestApp(t, server, 9)
	result := application.queryAccountBalance(context.Background(), work)
	if result.Status != "ok" || result.Provider != "usage" || result.Remaining == nil || *result.Remaining != 42 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSetBalanceCredentialFingerprintUsesDecryptedCredential(t *testing.T) {
	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	firstCiphertext, err := cipher.Encrypt(" shared-token ", "account:a")
	if err != nil {
		t.Fatal(err)
	}
	secondCiphertext, err := cipher.Encrypt("shared-token", "account:b")
	if err != nil {
		t.Fatal(err)
	}
	application := &App{cipher: cipher}
	first := accountBalanceWork{ID: "a", SourceType: "newapi", SourceCredentialCiphertext: firstCiphertext}
	second := accountBalanceWork{ID: "b", SourceType: "newapi", SourceCredentialCiphertext: secondCiphertext}
	application.setBalanceCredentialFingerprint(&first)
	application.setBalanceCredentialFingerprint(&second)
	if first.SourceCredentialFingerprint == "" || first.SourceCredentialFingerprint != second.SourceCredentialFingerprint {
		t.Fatalf("fingerprints differ: %q != %q", first.SourceCredentialFingerprint, second.SourceCredentialFingerprint)
	}
}

func newBalanceTestApp(t *testing.T, server *httptest.Server, remoteID int64) (*App, accountBalanceWork) {
	t.Helper()
	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	const siteID = "00000000-0000-0000-0000-000000000001"
	encrypted, err := cipher.Encrypt("admin-key", "site:"+siteID)
	if err != nil {
		t.Fatal(err)
	}
	return &App{cipher: cipher, httpClient: server.Client()}, accountBalanceWork{
		ID: "00000000-0000-0000-0000-000000000002", SiteID: siteID, SiteName: "test",
		SiteBaseURL: server.URL, SiteAPIKeyCiphertext: encrypted, RemoteID: remoteID, SourceType: "sub2api",
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
