package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrafficFiltersAccountModelAndTimeWithoutInventingLatency(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("account_id") != "7" || r.URL.Query().Get("model") != "model-a" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total": 6, "items": []map[string]any{
			{"account_id": 7, "model": "model-a", "kind": "success", "created_at": now},
			{"account_id": 7, "model": "model-a", "kind": "error", "status_code": 503, "created_at": now},
			{"account_id": 7, "model": "model-a", "kind": "error", "status_code": 400, "created_at": now},
			{"account_id": 8, "model": "model-a", "kind": "success", "created_at": now},
			{"account_id": 7, "model": "model-b", "kind": "success", "created_at": now},
			{"account_id": 7, "model": "model-a", "kind": "success", "created_at": now.Add(-time.Hour)},
		}}})
	}))
	defer server.Close()
	client, _ := NewSub2Client(server.URL, "test-key", server.Client())
	result, err := client.RecentTraffic(context.Background(), 7, "model-a")
	if err != nil || result.Total != 2 || result.Failed != 1 || result.FirstContentP95 != nil {
		t.Fatalf("traffic=%+v err=%v", result, err)
	}
}

func TestTrafficUnsupportedIsNotHealthyZero(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, _ := NewSub2Client(server.URL, "test-key", server.Client())
	result, err := client.RecentTraffic(context.Background(), 7, "test")
	if err != nil || result.Status != "unsupported" || result.FirstContentP95 != nil {
		t.Fatalf("unsupported=%+v %v", result, err)
	}
}

func TestTrafficLatencyTracksOnlyMatchingMeasuredRequests(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	measured := now.Add(-time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total": 4, "items": []map[string]any{
			{"account_id": 7, "model": "test-model", "kind": "success", "created_at": measured, "time_to_first_token_ms": 100},
			{"account_id": 7, "model": "test-model", "kind": "success", "created_at": now},
			{"account_id": 7, "model": "other-model", "kind": "success", "created_at": now, "time_to_first_token_ms": 1},
			{"account_id": 8, "model": "test-model", "kind": "success", "created_at": now, "time_to_first_token_ms": 1},
		}}})
	}))
	defer server.Close()
	client, _ := NewSub2Client(server.URL, "test-key", server.Client())
	result, err := client.RecentTraffic(context.Background(), 7, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.FirstContentSamples != 1 || result.FirstContentP95 == nil || *result.FirstContentP95 != 100 || result.FirstContentAt == nil || !result.FirstContentAt.Equal(measured) || result.LatestAt == nil || !result.LatestAt.Equal(now) {
		t.Fatalf("wrong latency provenance: %+v", result)
	}
}
