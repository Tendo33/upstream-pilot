package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
)

func TestSourceChangeSettlesButNeverReplaysOldIntent(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unapplied", true: "observed side effect"}[observed], func(t *testing.T) {
			a, w, remote, _ := newQualityIntegration(t)
			ctx := context.Background()
			p := quality.DefaultPolicy()
			p.Mode = "priority"
			setTestQualityPolicy(t, a, w.ID, p)
			seedEngineSamples(t, a, w, w.ID, 5, false, 100, 0)
			remote.rejectWrites = true
			if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
				t.Fatal("expected interrupted control")
			}
			_, pending, err := a.readQualityState(ctx, w)
			if err != nil || pending == nil {
				t.Fatal("missing pending", err)
			}
			if observed {
				remote.priority = *pending
			}
			remote.rejectWrites = false
			beforeWrites := len(remote.writes)
			engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_base_url='https://replacement.example.test' WHERE id=$1`, w.ID)
			if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
				t.Fatal(err)
			}
			fresh, _ := a.loadAccountWork(ctx, w.ID, w.OwnerID)
			state, left, err := a.readQualityState(ctx, fresh)
			if err != nil || left != nil || len(remote.writes) != beforeWrites {
				t.Fatal("old intent replayed or not settled", err)
			}
			if observed && (state.LastApplied == nil || *state.LastApplied != remote.priority) {
				t.Fatal("observed side effect lost ownership")
			}
			if !observed && remote.priority != 20 {
				t.Fatal("unapplied old target was replayed")
			}
		})
	}
}

func TestBackupEnteringCooldownAfterPreflightBlocksPause(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	var cooling atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if cooling.Load() && strings.HasSuffix(r.URL.Path, "/accounts/8") {
			_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 8, "status": "active", "schedulable": true, "priority": 20, "temp_unschedulable_until": time.Now().Add(time.Hour)}})
			return
		}
		remote.serve(out, r)
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	w, _ = a.loadAccountWork(ctx, w.ID, w.OwnerID)
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	zero, limit := 0.0, 10.0
	balances, _ := a.loadAccountBalanceWork(ctx, w.OwnerID, []string{w.ID})
	if err := a.saveAccountBalanceSnapshots(ctx, accountBalanceCacheKey(balances[0]), balances, upstream.BalanceResult{Status: "ok", Remaining: &zero}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	works, policies, err := a.engineSnapshot(ctx, w.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	a.preflightEngine(ctx, works)
	var target *engineWork
	for i := range works {
		if works[i].Work.ID == w.ID {
			target = &works[i]
		}
	}
	if target == nil || !safeToPause(*target, works, policies) {
		t.Fatal("initial backup not eligible")
	}
	cooling.Store(true)
	_, _, err = a.applyEngineControls(ctx, target, works, policies)
	if err == nil || !remote.schedulable {
		t.Fatal("stale preflight authorized pause", err)
	}
}

func TestCollectorIncidentsAndPendingAge(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	for range 2 {
		if err := a.recordIncident(ctx, w, "collector_traffic", "真实请求采集权限不足"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.recordIncident(ctx, w, "collector_traffic", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.loadQualityState(ctx, w); err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `UPDATE quality_states SET pending_since=now()-interval '10 minutes',pending_priority=30 WHERE account_id=$1`, w.ID)
	p := engineWork{Policy: quality.DefaultPolicy(), Decision: quality.Decision{State: quality.State{Baseline: 20, Desired: 20, Status: "unknown"}}}
	for range 2 {
		if err := a.persistEngineDecision(ctx, w, p, 20, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"collector_traffic_failed", "collector_traffic_recovered", "pending_control_failed"} {
		var count int
		if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE account_id=$1 AND kind=$2`, w.ID, kind).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: %d %v", kind, count, err)
		}
	}
}

func TestSourceGenerationChangesOnlyWithIdentity(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET config_generation=config_generation+1,priority=30,observed_at=now() WHERE id=$1`, w.ID)
	fresh, err := a.loadAccountWork(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SourceGeneration != w.SourceGeneration {
		t.Fatal("inventory/config change invalidated source")
	}
	for _, change := range []string{
		`observed_source_base_url='https://first.example.test'`,
		`observed_source_credential_fingerprint=repeat('a',64)`,
		`source_type='newapi'`,
		`source_group='paid'`,
		`source_mapping_fingerprint='another-mapping'`,
		`probe_model='other-model'`,
		`recharge_ratio=2`,
	} {
		old := fresh.SourceGeneration
		engineRegressionSQL(t, a, `UPDATE upstream_accounts SET `+change+` WHERE id=$1`, w.ID)
		fresh, err = a.loadAccountWork(ctx, w.ID, w.OwnerID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.SourceGeneration != old+1 {
			t.Fatalf("generation missing for %s", change)
		}
		engineRegressionSQL(t, a, `UPDATE upstream_accounts SET `+change+` WHERE id=$1`, w.ID)
		same, _ := a.loadAccountWork(ctx, w.ID, w.OwnerID)
		if same.SourceGeneration != fresh.SourceGeneration {
			t.Fatalf("same identity invalidated for %s", change)
		}
	}
	engineRegressionSQL(t, a, `UPDATE sites SET base_url='https://new.example.test' WHERE id=$1`, w.SiteID)
	last, err := a.loadAccountWork(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if last.SourceGeneration != fresh.SourceGeneration+1 {
		t.Fatal("site identity was not propagated")
	}
}

func TestSourceChangeExcludesOldEvidenceAndRejectsLateBalance(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	limit := 10.0
	p.LowBalance = &limit
	works, err := a.loadAccountBalanceWork(ctx, w.OwnerID, []string{w.ID})
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	key := accountBalanceCacheKey(works[0])
	if err = a.saveAccountBalanceSnapshots(ctx, key, works, upstream.BalanceResult{Status: "ok", Remaining: &zero}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	seedEngineSamples(t, a, w, w.ID, 5, true, 100, 0)
	if err = a.persistRateObservation(ctx, w, rateSyncOutcome{SourceRate: 1, EffectiveRate: 1}); err != nil {
		t.Fatal(err)
	}
	before, err := a.qualitySnapshot(ctx, w, p)
	if err != nil || !before.BalanceFresh || !before.RateFresh || len(before.Samples) != 5 {
		t.Fatalf("setup: %+v %v", before, err)
	}
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_base_url='https://replacement.example.test',observed_source_credential_fingerprint=repeat('b',64) WHERE id=$1`, w.ID)
	fresh, err := a.loadAccountWork(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := a.qualitySnapshot(ctx, fresh, p)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Balance != nil || snap.BalanceFresh || snap.RateFresh || len(snap.Samples) != 0 {
		t.Fatalf("old evidence leaked: %+v", snap)
	}
	if d := quality.Evaluate(p, quality.State{Baseline: 20}, snap, time.Now().UTC()); d.HardFailure {
		t.Fatal("old zero balance can pause replacement")
	}
	if err = a.saveAccountBalanceSnapshots(ctx, key, works, upstream.BalanceResult{Status: "ok", Remaining: &zero}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var count int
	if err = a.db.QueryRow(ctx, `SELECT source_generation FROM account_balance_snapshots WHERE account_id=$1`, w.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != w.SourceGeneration {
		t.Fatal("late balance stamped as new source")
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM probe_attempts WHERE account_id=$1 AND source_generation=$2`, w.ID, w.SourceGeneration).Scan(&count); err != nil || count != 5 {
		t.Fatalf("lost history: %d %v", count, err)
	}
	if _, err = a.qualitySnapshot(ctx, w, p); err == nil {
		t.Fatal("stale work consumed newer source")
	}
	if err = a.persistRateObservation(ctx, w, rateSyncOutcome{EffectiveRate: 99}); err == nil {
		t.Fatal("late price accepted")
	}
}

func TestProbeFinishingAfterSourceChangeIsHistoricalOnly(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	started, finish := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		out.Header().Set("Content-Type", "text/event-stream")
		_, _ = out.Write([]byte("data: {\"type\":\"content\",\"text\":\"ok\"}\n\ndata: {\"type\":\"test_complete\",\"success\":true}\n\n"))
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	w, _ = a.loadAccountWork(ctx, w.ID, w.OwnerID)
	done := make(chan error, 1)
	go func() { _, err := a.runProbe(ctx, w, "manual", w.OwnerID); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("probe did not start")
	}
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_credential_fingerprint=repeat('c',64) WHERE id=$1`, w.ID)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fresh, _ := a.loadAccountWork(ctx, w.ID, w.OwnerID)
	snap, err := a.qualitySnapshot(ctx, fresh, quality.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Samples) != 0 || fresh.HealthState != "unknown" {
		t.Fatalf("late probe changed current health: %+v %+v", snap, fresh.HealthState)
	}
	var oldCount int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM probe_attempts WHERE account_id=$1 AND source_generation=$2`, w.ID, w.SourceGeneration).Scan(&oldCount); err != nil || oldCount != 1 {
		t.Fatal("late probe not retained for audit", err)
	}
}

func TestTrafficFinishingAfterSourceChangeCannotOverwriteCurrent(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	started, finish := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		_, _ = out.Write([]byte(`{"code":0,"data":{"items":[],"total":0}}`))
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	w, _ = a.loadAccountWork(ctx, w.ID, w.OwnerID)
	done := make(chan error, 1)
	go func() { done <- a.sampleQualityTraffic(ctx, w) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("traffic did not start")
	}
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_base_url='https://new.example.test' WHERE id=$1`, w.ID)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_traffic WHERE account_id=$1`, w.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late traffic saved: %d %v", count, err)
	}
}

func TestControllerFailureVisibleDeduplicatedAndRecovered(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.backupPriority = 10
	_, other := seedEnginePool(t, a, w, 10)
	seedEngineSamples(t, a, w, w.ID, 5, true, 100, 0)
	seedEngineSamples(t, a, w, other, 5, true, 1000, 0)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	remote.rejectWrites = true
	for range 2 {
		if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
			t.Fatal("missing write error")
		}
	}
	v, err := a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ControllerError == "" || !v.HasPendingControls || v.PendingSince == nil {
		t.Fatalf("failure hidden: %+v", v)
	}
	var count int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE account_id=$1 AND kind='controller_failed'`, w.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicated/missing failure: %d %v", count, err)
	}
	remote.rejectWrites = false
	for range 2 {
		if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	v, err = a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil || v.ControllerError != "" || v.HasPendingControls || v.PendingSince != nil {
		t.Fatal("recovery hidden", err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE account_id=$1 AND kind='controller_recovered'`, w.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicated/missing recovery: %d %v", count, err)
	}
}

func TestAccountAuthTrafficIsNotReportedHealthy(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for range 5 {
			items = append(items, map[string]any{"account_id": 7, "model": "test-model", "kind": "success", "created_at": now}, map[string]any{"account_id": 7, "model": "test-model", "kind": "error", "created_at": now, "status_code": 401, "phase": "account_auth"})
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items, "total": 10}})
	}))
	defer server.Close()
	c, _ := upstream.NewSub2Client(server.URL, "test-key", server.Client())
	traffic, err := c.RecentTraffic(context.Background(), 7, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	d := quality.Evaluate(quality.DefaultPolicy(), quality.State{Baseline: 20}, quality.Snapshot{TrafficFresh: true, TrafficAt: traffic.LatestAt, TrafficTotal: traffic.Total, TrafficFailed: traffic.Failed}, time.Now().UTC())
	if traffic.Total != 10 || traffic.Failed != 5 || traffic.FailureCategories["upstream_auth"] != 5 || d.Eligible {
		t.Fatalf("auth failures discarded: %+v %+v", traffic, d)
	}
}

func TestCoolingBackupCannotPausePrimary(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accounts/8") {
			_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 8, "status": "active", "priority": 20, "schedulable": true, "temp_unschedulable_until": time.Now().Add(time.Hour)}})
			return
		}
		remote.serve(out, r)
	}))
	defer server.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, server.URL)
	w, _ = a.loadAccountWork(ctx, w.ID, w.OwnerID)
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	// Consecutive upstream credential failures are a hard failure.
	seedEngineSamples(t, a, w, w.ID, 5, false, 100, 0)
	engineRegressionSQL(t, a, `UPDATE probe_attempts SET failure_reason='AUTH' WHERE account_id=$1`, w.ID)
	zero := 0.0
	limit := 10.0
	works, _ := a.loadAccountBalanceWork(ctx, w.OwnerID, []string{w.ID})
	if err := a.saveAccountBalanceSnapshots(ctx, accountBalanceCacheKey(works[0]), works, upstream.BalanceResult{Status: "ok", Remaining: &zero}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HardFailure {
		t.Fatal("test did not exercise hard failure")
	}
	if !remote.schedulable {
		t.Fatal("cooling backup authorized primary pause")
	}
}
