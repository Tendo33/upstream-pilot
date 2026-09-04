package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInferSourceTypeHint(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
		credentials string
		extra       string
		want        string
	}{
		{name: "platform", platform: "New-API", want: "newapi"},
		{name: "credential marker", credentials: `{"provider":"one_api"}`, want: "newapi"},
		{name: "explicit sub2api", extra: `{"source_type":"sub2api"}`, want: "sub2api"},
		{name: "native billing", extra: `{"upstream_billing_probe":{"data":{"object":"sub2api.key_billing"}}}`, want: "sub2api"},
		{name: "generic openai", platform: "openai", accountType: "apikey"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := inferSourceTypeHint(test.platform, test.accountType, json.RawMessage(test.credentials), json.RawMessage(test.extra))
			if got != test.want {
				t.Fatalf("inferSourceTypeHint() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProbeNewAPISource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/api/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"system_name": "fixture", "quota_per_unit": 500000},
		})
	}))
	defer server.Close()
	if !ProbeNewAPISource(context.Background(), server.URL+"/tenant/v1", server.Client()) {
		t.Fatal("NewAPI source was not detected")
	}
	if statusURL, err := NewAPIStatusURL(server.URL + "/tenant/api/user/self/groups?ignored=1#fragment"); err != nil || statusURL != server.URL+"/tenant/api/status" {
		t.Fatalf("NewAPIStatusURL() = %q, %v", statusURL, err)
	}
}

func TestProbeNewAPISourceRejectsWeakResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"version": "unrelated"}})
	}))
	defer server.Close()
	if ProbeNewAPISource(context.Background(), server.URL, server.Client()) {
		t.Fatal("weak response was misclassified as NewAPI")
	}
}
