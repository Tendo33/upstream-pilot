package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSiteTrafficBatchesAccountsAndMergesUserResults(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	var calls atomic.Int32
	now := time.Now().Add(-time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/ops/requests" {
			calls.Add(1)
			if r.URL.Query().Get("account_id") != "" {
				t.Error("batch used per-account filter")
			}
			_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"total": 2, "items": []map[string]any{{"account_id": 7, "group_id": 1, "request_id": "one", "model": "test-model", "kind": "error", "phase": "upstream", "status_code": 503, "created_at": now}, {"account_id": 8, "group_id": 1, "request_id": "one", "model": "test-model", "kind": "success", "stream_complete": true, "created_at": now}}}})
			return
		}
		remote.serve(out, r)
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	_, _ = seedEnginePool(t, a, w, 20)
	for range 2 {
		if err := a.collectSiteTraffic(ctx, w.SiteID, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	var users, snapshots int
	var outcome string
	if err := a.db.QueryRow(ctx, `SELECT count(*),min(outcome) FROM request_outcome_observations`).Scan(&users, &outcome); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_traffic`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || users != 1 || snapshots != 2 || outcome != "success" {
		t.Fatal(calls.Load(), users, snapshots, outcome)
	}
}

func TestSiteTelemetryLateResponseCannotCrossAddressChange(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-finish
		_, _ = out.Write([]byte(`{"code":0,"data":{"items":[],"total":0}}`))
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	done := make(chan error, 1)
	go func() { done <- a.collectSiteTraffic(ctx, w.SiteID, w.OwnerID) }()
	<-started
	engineRegressionSQL(t, a, `UPDATE sites SET base_url='http://127.0.0.1:1' WHERE id=$1`, w.SiteID)
	close(finish)
	if err := <-done; err == nil {
		t.Fatal("old site response accepted")
	}
	var n int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_traffic`).Scan(&n)
	if n != 0 {
		t.Fatal("late snapshot persisted")
	}
}

func TestTaskLeaseCoalescesKindsAndRejectsOldOwner(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	s := a.NewScheduler()
	first, err := s.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Resource != "site" {
		t.Fatal(first)
	}
	second, err := s.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Resource == first.Resource && second.ID == first.ID {
		t.Fatal("same site queued twice")
	}
	owned := context.WithValue(ctx, taskLeaseContextKey{}, first)
	if err = a.requireTaskLease(owned); err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `UPDATE task_leases SET owner_token=gen_random_uuid() WHERE resource_type=$1 AND resource_id=$2`, first.Resource, first.ID)
	if err = a.requireTaskLease(owned); err == nil {
		t.Fatal("stale owner allowed")
	}
	var nativeWrites int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM engine_actions WHERE account_id=$1`, w.ID).Scan(&nativeWrites)
	if nativeWrites != 0 {
		t.Fatal("read-only lease check wrote control")
	}
}

func TestTaskOlderThanTwoMinutesRemainsOwnedAfterRenewal(t *testing.T) {
	a, _, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	s := a.NewScheduler()
	task, err := s.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `UPDATE task_leases SET started_at=now()-interval '3 minutes',lease_until=now()+interval '2 minutes' WHERE resource_type=$1 AND resource_id=$2`, task.Resource, task.ID)
	owned := context.WithValue(ctx, taskLeaseContextKey{}, task)
	if err = a.requireTaskLease(owned); err != nil {
		t.Fatal(err)
	}
	command, err := a.db.Exec(ctx, `UPDATE task_leases SET lease_until=now()+interval '2 minutes' WHERE resource_type=$1 AND resource_id=$2 AND owner_token=$3 AND lease_until>now()`, task.Resource, task.ID, task.LeaseToken)
	if err != nil || command.RowsAffected() != 1 {
		t.Fatal("renewal failed", err)
	}
	other, err := s.claim(ctx)
	if err == nil && other.Resource == task.Resource && other.ID == task.ID {
		t.Fatal("renewed long task was duplicated")
	}
}
