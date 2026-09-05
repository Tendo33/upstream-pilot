package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSplitTrafficSeparatesSupplierRetriesFromFinalResults(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	base := func(id, account int, request string, status int) map[string]any {
		return map[string]any{"id": id, "error_id": id, "account_id": account, "group_id": 1, "request_id": request, "model": "test", "kind": "error", "status_code": status, "phase": "upstream", "type": "rate_limit_error", "error_owner": "provider", "error_source": "upstream_http", "created_at": now}
	}
	attempt := base(1, 7, "recovered", 503)
	failed := base(2, 9, "failed", 429)
	quota := base(3, 9, "quota", 429)
	quota["type"] = "insufficient_quota"
	client := base(4, 9, "client", 429)
	client["error_owner"] = "client"
	success := map[string]any{"account_id": 8, "group_id": 1, "request_id": "recovered", "model": "test", "kind": "success", "stream": false, "created_at": now}
	feeds := map[string][]map[string]any{
		"/api/v1/admin/ops/requests":        {attempt, failed, quota, client, success},
		"/api/v1/admin/ops/upstream-errors": {attempt, attempt, failed, quota, client},
		"/api/v1/admin/ops/request-errors":  {failed, quota, client},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, ok := feeds[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items, "total": len(items)}})
	}))
	defer server.Close()
	c, _ := NewSub2Client(server.URL, "test-key", server.Client())
	b, err := c.RecentSiteTraffic(context.Background())
	if err != nil || b.Status != "ok" {
		t.Fatal(b, err)
	}
	s := SummarizeTraffic(b, 7, "test")
	if s.Total != 1 || s.Failed != 1 {
		t.Fatalf("recovered supplier failure lost or doubled: %+v", s)
	}
	s = SummarizeTraffic(b, 9, "test")
	if s.Total != 2 || s.Failed != 2 || s.FailureCategories["rate_limit"] != 1 || s.FailureCategories["balance"] != 1 || s.ExcludedErrors != 1 {
		t.Fatalf("classification or double count: %+v", s)
	}
	outcomes, missing := FinalRequestOutcomes(b.Records)
	got := map[string]string{}
	for _, o := range outcomes {
		got[o.RequestID] = o.Outcome
	}
	if missing != 0 || got["recovered"] != "success" || got["failed"] != "failure" || got["quota"] != "failure" || got["client"] != "failure" {
		t.Fatal(got, missing)
	}
}

func TestTrafficMissingTotalsStayBoundedAndFailuresSurvivePermissionGap(t *testing.T) {
	var calls atomic.Int32
	now := time.Now().UTC().Add(-time.Second)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/ops/request-errors" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.URL.Path == "/api/v1/admin/ops/upstream-errors" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []map[string]any{{"id": 5, "account_id": 7, "status_code": 503, "phase": "upstream", "model": "test", "created_at": now}}}})
			return
		}
		calls.Add(1)
		items := make([]map[string]any, 100)
		for i := range items {
			items[i] = map[string]any{"account_id": 8, "kind": "success", "model": "test", "created_at": now}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items}})
	}))
	defer s.Close()
	c, _ := NewSub2Client(s.URL, "test-key", s.Client())
	b, err := c.RecentSiteTraffic(context.Background())
	if err == nil || b.Status != "partial" || !b.Truncated || calls.Load() != 3 {
		t.Fatal(b, err, calls.Load())
	}
	v := SummarizeTraffic(b, 7, "test")
	if v.Failed != 1 || !v.Incomplete || v.Status != "partial" {
		t.Fatal(v)
	}
}

func TestSupplierAttemptCannotClaimFinalOutcome(t *testing.T) {
	g := int64(1)
	yes := true
	results, missing := FinalRequestOutcomes([]TrafficRecord{{Source: trafficUpstreamErrors, GroupID: &g, RequestID: "retry", FinalOutcome: "failure", IsFinal: &yes, Kind: "error", CreatedAt: time.Now()}})
	if len(results) != 0 || missing != 0 {
		t.Fatal(results, missing)
	}
}
