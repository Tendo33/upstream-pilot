package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

func TestDetectAccountSourceTypesSkipsLockedAccounts(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
			"system_name": "NewAPI", "quota_per_unit": 500000,
		}})
	}))
	defer server.Close()

	application := &App{httpClient: server.Client()}
	accounts := []upstream.Sub2Account{{
		ID: 1, Platform: "openai", Type: "apikey", ObservedSourceBaseURL: &server.URL,
	}}
	result := application.detectAccountSourceTypes(context.Background(), accounts, map[int64]string{1: "sub2api"})
	if len(result) != 1 || result[0] != "sub2api" {
		t.Fatalf("result = %#v, want locked sub2api", result)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("locked account made %d source-detection requests", got)
	}
}

func TestDetectAccountSourceTypesProbesUnlockedCandidate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
			"system_name": "NewAPI", "quota_per_unit": 500000,
		}})
	}))
	defer server.Close()

	application := &App{httpClient: server.Client()}
	accounts := []upstream.Sub2Account{{
		ID: 2, Platform: "openai", Type: "apikey", ObservedSourceBaseURL: &server.URL,
	}}
	result := application.detectAccountSourceTypes(context.Background(), accounts, nil)
	if len(result) != 1 || result[0] != "newapi" {
		t.Fatalf("result = %#v, want detected newapi", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("unlocked candidate made %d requests, want 1", got)
	}
}
