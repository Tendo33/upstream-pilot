package app

// Regression tests for the five confirmed scheduling-engine review findings.
import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
	"time"
)

func engineRegressionSQL(t *testing.T, a *App, q string, args ...any) {
	t.Helper()
	if _, err := a.db.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func TestEngineCancelsStalePendingPromotionAfterNewFailure(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.CooldownSeconds = 0
	p.MinimumSamples = 2
	setTestQualityPolicy(t, a, w.ID, p)
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 40 {
		t.Fatalf("setup priority %d", remote.priority)
	}
	engineRegressionSQL(t, a, `UPDATE probe_attempts SET created_at=now()-interval '1 hour' WHERE account_id=$1`, w.ID)
	remote.rejectWrites = true
	for i := 0; i < 3; i++ {
		seedEngineSamples(t, a, w, w.ID, 1, true, 100, 0)
		_, err := a.evaluateQuality(ctx, w, w.OwnerID)
		if i == 2 && err == nil {
			t.Fatal("expected failed recovery write")
		}
	}
	_, pending, err := a.readQualityState(ctx, w)
	if err != nil || pending == nil || *pending != 30 {
		t.Fatalf("setup pending=%v error=%v", pending, err)
	}
	engineRegressionSQL(t, a, `UPDATE probe_attempts SET created_at=now()-interval '1 hour' WHERE account_id=$1`, w.ID)
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	remote.rejectWrites = false
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Tier != 2 || remote.priority != 40 {
		t.Fatalf("incorrect behavior: tier=%d priority=%d", d.State.Tier, remote.priority)
	}
	_, pending, err = a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil || d.State.Desired != 40 {
		t.Fatalf("obsolete pending not cancelled: %v %+v", pending, d.State)
	}
}
func TestEngineExpiredFinancialBackupCannotAuthorizePause(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	limit := 10.0
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	q := quality.DefaultPolicy()
	q.FreshSeconds = 30
	q.LowBalance = &limit
	setTestQualityPolicy(t, a, other, q)
	engineRegressionSQL(t, a, `INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,checked_at) VALUES($1,$3,'ok',0,now()),($2,$4,'ok',50,now()-interval '29 seconds')`, w.ID, other, qualityTestBalanceKey(t, a, w.ID), qualityTestBalanceKey(t, a, other))
	works, policies, err := a.engineSnapshot(ctx, w.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	var target *engineWork
	var backup engineWork
	for i := range works {
		if works[i].Work.ID == w.ID {
			target = &works[i]
		} else {
			backup = works[i]
		}
	}
	if !backup.Decision.Eligible {
		t.Fatal("setup: fresh backup not eligible")
	}
	wait := time.Until(backup.Snapshot.BalanceAt.Add(30*time.Second)) + 150*time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
	fresh, err := a.qualitySnapshot(ctx, backup.Work, backup.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.BalanceFresh {
		t.Fatal("setup: backup financial sample not expired")
	}
	accepted := a.verifyBackups(ctx, *target, works, policies)
	_, applied, err := a.applyEngineControls(ctx, target, works, policies)
	if !errors.Is(err, errEngineReplan) {
		t.Fatalf("expected evidence replan: %v", err)
	}
	if accepted || applied || !remote.schedulable {
		t.Fatalf("incorrect behavior: accepted=%v applied=%v enabled=%v", accepted, applied, remote.schedulable)
	}
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if !remote.schedulable {
		t.Fatal("obsolete pause was replayed in next cycle")
	}
	view, err := a.qualityView(ctx, backup.Work.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Eligible {
		t.Fatal("dashboard accepted expired financial evidence")
	}
}
func TestEngineUnrelatedObserve404DoesNotBlockDemotion(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	engineRegressionSQL(t, a, `INSERT INTO upstream_accounts(id,site_id,remote_id,name,remote_status,schedulable,priority,probe_model) VALUES($1,$2,9,'unrelated-observe','active',true,20,'different-model')`, uuid.NewString(), w.SiteID)
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil || remote.priority != 40 || d.State.Tier != 2 || len(remote.writes) != 1 {
		t.Fatalf("incorrect behavior: tier=%d priority=%d writes=%v err=%v", d.State.Tier, remote.priority, remote.writes, err)
	}
	if d.State.Desired != 40 {
		t.Fatal("wrong target priority")
	}
}
func TestEngineSpeedStrategyUsesRealTrafficLatency(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.priority = 1
	remote.backupPriority = 100
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET priority=1,health_enabled=false WHERE id=$1`, w.ID)
	g, other := seedEnginePool(t, a, w, 100)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	gp := quality.DefaultGroupPolicy()
	gp.Strategy = quality.SpeedFirst
	raw, _ := json.Marshal(gp)
	engineRegressionSQL(t, a, `INSERT INTO engine_group_policies(group_id,model,config) VALUES($1,'test-model',$2)`, g, raw)
	now := time.Now().UTC()
	for id, ms := range map[string]int{w.ID: 7000, other: 100} {
		v := ms
		raw, _ := json.Marshal(upstream.TrafficSummary{Status: "ok", Model: "test-model", Total: 10, FirstContentSamples: 10, FirstContentAt: &now, FirstContentP95: &v, LatestAt: &now, WindowStart: now.Add(-time.Minute), WindowEnd: now})
		engineRegressionSQL(t, a, `INSERT INTO quality_traffic(account_id,snapshot,checked_at) VALUES($1,$2,now())`, id, raw)
	}
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Eligible || d.P95 != nil || d.SortingLatency == nil || *d.SortingLatency != 7000 || d.LatencySource != "traffic" || remote.priority <= 100 {
		t.Fatalf("incorrect behavior: %+v priority=%d", d, remote.priority)
	}
	view, err := a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.LatencySource != "traffic" || view.SortingLatency == nil || *view.SortingLatency != 7000 {
		t.Fatal("sorting provenance missing from API")
	}
}
func TestEngineCapacityOnlyOwnershipExposesRestoreAndRestores(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.backupPriority = 10
	_, other := seedEnginePool(t, a, w, 10)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	seedEngineSamples(t, a, w, w.ID, 3, true, 12000, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoConcurrency = true
	p.AutoLoadFactor = true
	setTestQualityPolicy(t, a, w.ID, p)
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if remote.concurrency != 4 || remote.loadFactor != 8 || remote.priority != 20 || d.State.LastApplied != nil {
		t.Fatalf("incorrect behavior: %+v priority=%d concurrency=%d load=%d", d.State, remote.priority, remote.concurrency, remote.loadFactor)
	}
	view, err := a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasManagedControls || view.HasPendingControls {
		t.Fatalf("missing ownership flags %+v", view)
	}
	// The restore action must be available even without a priority write.
	req := qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err = a.qualityReleaseHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if remote.concurrency != 8 || remote.loadFactor != 16 || remote.priority != 20 {
		t.Fatal("restore failed to recover capacity baselines")
	}
	view, err = a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.HasManagedControls || view.HasPendingControls || view.Policy.Mode != "observe" {
		t.Fatal("ownership did not clear after restore")
	}
}

func TestEnginePartialIntentAcknowledgesAppliedFieldsAndCancelsDisabledFields(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoConcurrency = true
	p.AutoLoadFactor = true
	setTestQualityPolicy(t, a, w.ID, p)
	remote.rejectWrites = true
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("expected failed write")
	}
	// Only priority reached the target before the upstream response was lost.
	remote.priority = 40
	remote.rejectWrites = false
	p.AutoConcurrency = false
	p.AutoLoadFactor = false
	setTestQualityPolicy(t, a, w.ID, p)
	writes := len(remote.writes)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	state, pending, err := a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied == nil || *state.LastApplied != 40 || pending != nil || remote.concurrency != 8 || remote.loadFactor != 16 || len(remote.writes) != writes {
		t.Fatalf("partial intent incorrectly replayed: state=%+v pending=%v writes=%v", state, pending, remote.writes)
	}
	var applied, queued []byte
	if err = a.db.QueryRow(ctx, `SELECT applied_control,pending_control FROM quality_states WHERE account_id=$1`, w.ID).Scan(&applied, &queued); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err = json.Unmarshal(applied, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields["priority"] != float64(40) || string(queued) != "{}" {
		t.Fatalf("checkpoint incorrect: %s / %s", applied, queued)
	}
}
func TestEngineIntentCheckpointRollbackPreservesRetry(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	remote.rejectWrites = true
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("expected failed write")
	}
	remote.priority = 40
	remote.rejectWrites = false
	engineRegressionSQL(t, a, `CREATE FUNCTION reject_control_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'checkpoint failure'; END $$; CREATE TRIGGER reject_control_checkpoint BEFORE INSERT ON quality_decisions FOR EACH ROW EXECUTE FUNCTION reject_control_checkpoint()`)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("checkpoint error hidden")
	}
	state, pending, err := a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied != nil || pending == nil || *pending != 40 {
		t.Fatal("failed checkpoint discarded pending intent")
	}
	engineRegressionSQL(t, a, `DROP TRIGGER reject_control_checkpoint ON quality_decisions`)
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	state, pending, err = a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastApplied == nil || *state.LastApplied != 40 || pending != nil || len(remote.writes) != 1 {
		t.Fatal("retry did not acknowledge without duplicate RPC")
	}
}
func TestEngineTransitiveSharedPoolFailureStillBlocksDependentWrites(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	third, g := uuid.NewString(), uuid.NewString()
	engineRegressionSQL(t, a, `INSERT INTO upstream_accounts(id,site_id,remote_id,name,remote_status,schedulable,priority,probe_model) VALUES($1,$2,9,'missing','active',true,20,'test-model')`, third, w.SiteID)
	engineRegressionSQL(t, a, `INSERT INTO upstream_groups(id,site_id,remote_id,name) VALUES($1,$2,2,'shared-second')`, g, w.SiteID)
	engineRegressionSQL(t, a, `INSERT INTO account_group_memberships(account_id,group_id,site_id) VALUES($1,$3,$4),($2,$3,$4)`, other, third, g, w.SiteID)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("transitive dependency failure ignored")
	}
	if remote.priority != 20 || len(remote.writes) != 0 {
		t.Fatal("dependent pool wrote despite failed shared member")
	}
}
func TestEngineCapacityPendingOnlyExposesRestore(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.backupPriority = 10
	_, other := seedEnginePool(t, a, w, 10)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	seedEngineSamples(t, a, w, w.ID, 3, true, 12000, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoConcurrency = true
	setTestQualityPolicy(t, a, w.ID, p)
	remote.rejectWrites = true
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("expected rejection")
	}
	view, err := a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State.LastApplied != nil || !view.HasPendingControls {
		t.Fatal("pending capacity restoration unavailable")
	}
	remote.rejectWrites = false
	req := qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err = a.qualityReleaseHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if remote.concurrency != 8 {
		t.Fatal("unapplied capacity target was sent during release")
	}
	view, err = a.qualityView(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.HasPendingControls || view.HasManagedControls {
		t.Fatal("release kept stale ownership")
	}
}

func TestEngineTargetEvidenceCannotExpireDuringBackupReadback(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	p.FreshSeconds = 30
	limit := 10.0
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	engineRegressionSQL(t, a, `INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,checked_at) VALUES($1,$2,'ok',0,now()-interval '29 seconds')`, w.ID, qualityTestBalanceKey(t, a, w.ID))
	works, policies, err := a.engineSnapshot(ctx, w.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	var target *engineWork
	for i := range works {
		if works[i].Work.ID == w.ID {
			target = &works[i]
		}
	}
	if target == nil || !target.Decision.HardFailure {
		t.Fatal("missing valid initial failure evidence")
	}
	remote.backupDelayMS = 1500
	_, _, err = a.applyEngineControls(ctx, target, works, policies)
	if !errors.Is(err, errEngineReplan) {
		t.Fatalf("expected replan after target expiration: %v", err)
	}
	if !remote.schedulable {
		t.Fatal("paused using evidence that expired during backup GET")
	}
}
