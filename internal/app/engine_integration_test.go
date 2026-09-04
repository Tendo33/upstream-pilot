package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/google/uuid"
)

func seedEngineSamples(t *testing.T, a *App, w AccountWork, id string, n int, success bool, ms, age int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := a.db.Exec(context.Background(), `INSERT INTO probe_attempts(id,owner_id,site_id,account_id,kind,success,model,first_content_ms,duration_ms,failure_reason,created_at) VALUES($1,$2,$3,$4,'manual',$5,'test-model',$6,$6,'UPSTREAM',now()-$7*interval '1 second')`, uuid.NewString(), w.OwnerID, w.SiteID, id, success, ms, age+i)
		if err != nil {
			t.Fatal(err)
		}
	}
}
func seedEnginePool(t *testing.T, a *App, w AccountWork, priority int) (string, string) {
	t.Helper()
	ctx := context.Background()
	g, other := uuid.NewString(), uuid.NewString()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO upstream_groups(id,site_id,remote_id,name) VALUES($1,$2,1,'engine-pool')`, []any{g, w.SiteID}},
		{`INSERT INTO upstream_accounts(id,site_id,remote_id,name,remote_status,schedulable,priority,probe_model) VALUES($1,$2,8,'backup','active',true,$3,'test-model')`, []any{other, w.SiteID, priority}},
		{`INSERT INTO account_group_memberships(account_id,group_id,site_id) VALUES($1,$3,$4),($2,$3,$4)`, []any{w.ID, other, g, w.SiteID}},
	} {
		if _, err := a.db.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return g, other
}
func TestEngineSlowWindowAndPinnedBackupOrdering(t *testing.T) {
	a, w, remote, url := newQualityIntegration(t)
	ctx := context.Background()
	remote.priority = 1
	remote.backupPriority = 100
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET priority=1 WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	request := func() (int, string) {
		resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"test-model"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}
	if status, body := request(); status != 503 || !strings.Contains(body, `"upstream_account_id":7`) {
		t.Fatalf("wrong initial route %d %s", status, body)
	}
	_, other := seedEnginePool(t, a, w, 100)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	// Four slow completed probes, spaced by interval + request duration.
	for i := 0; i < 4; i++ {
		seedEngineSamples(t, a, w, w.ID, 1, true, 35000, 160*i)
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Tier != 1 || d.Count != 4 || remote.priority <= 100 {
		t.Fatalf("slow not placed behind healthy fixed backup: %+v remote=%d", d, remote.priority)
	}
	if status, body := request(); status != 200 || !strings.Contains(body, `"upstream_account_id":8`) {
		t.Fatalf("route did not switch after engine writes %d %s", status, body)
	}
	remote.mu.Lock()
	n := len(remote.writes)
	remote.mu.Unlock()
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if len(remote.writes) != n {
		t.Fatal("identical cycle rewrote upstream")
	}
}
func TestEngineStateDecisionOutboxRollbackAndRemoteAckRetry(t *testing.T) {
	for _, mode := range []string{"observe", "priority"} {
		t.Run(mode, func(t *testing.T) {
			a, w, remote, _ := newQualityIntegration(t)
			ctx := context.Background()
			p := quality.DefaultPolicy()
			p.Mode = mode
			setTestQualityPolicy(t, a, w.ID, p)
			seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
			if _, err := a.db.Exec(ctx, `CREATE FUNCTION reject_engine_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected outbox failure'; END $$; CREATE TRIGGER reject_engine_event BEFORE INSERT ON quality_notifications FOR EACH ROW EXECUTE FUNCTION reject_engine_event()`); err != nil {
				t.Fatal(err)
			}
			if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
				t.Fatal("injected failure was hidden")
			}
			var tier, events, decisions int
			var pending *int
			if err := a.db.QueryRow(ctx, `SELECT tier,pending_priority,(SELECT count(*) FROM quality_decisions),(SELECT count(*) FROM quality_notifications) FROM quality_states WHERE account_id=$1`, w.ID).Scan(&tier, &pending, &decisions, &events); err != nil {
				t.Fatal(err)
			}
			if tier != 0 || events != 0 || decisions != 0 {
				t.Fatalf("partial local commit: tier=%d decisions=%d events=%d", tier, decisions, events)
			}
			if mode == "priority" && (pending == nil || remote.priority != 40) {
				t.Fatal("lost durable pending write")
			}
			if _, err := a.db.Exec(ctx, `DROP TRIGGER reject_engine_event ON quality_notifications`); err != nil {
				t.Fatal(err)
			}
			if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != 1 {
				t.Fatalf("event not retried: %d", events)
			}
			if mode == "priority" && len(remote.writes) != 1 {
				t.Fatal("retry duplicated remote mutation")
			}
		})
	}
}
func TestEngineConcurrentPriceChangeAndStableReference(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	if _, err := a.runRateSync(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	remote.rate = 2
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := a.runRateSync(ctx, w, w.OwnerID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n, events int
	var reference float64
	if err := a.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM upstream_price_history),(SELECT count(*) FROM quality_notifications WHERE kind='price_change'),price_reference_rate FROM upstream_accounts WHERE id=$1`, w.ID).Scan(&n, &events, &reference); err != nil {
		t.Fatal(err)
	}
	if n != 2 || events != 1 || reference != 1 {
		t.Fatalf("duplicate/lost reference %d %d %.2f", n, events, reference)
	}
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET config_generation=config_generation+1 WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.persistRateObservation(ctx, w, rateSyncOutcome{SourceRate: 3, EffectiveRate: 3}); err == nil {
		t.Fatal("stale settings result accepted")
	}
	a.recordRateFailure(ctx, w, errors.New("stale failure"))
	var status string
	if err := a.db.QueryRow(ctx, `SELECT price_status FROM upstream_accounts WHERE id=$1`, w.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ok" {
		t.Fatal("stale error overwrote valid price")
	}
}
func TestEngineStaleBackupMatchesDashboardAndBlocksPause(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	_, other := seedEnginePool(t, a, w, 20)
	p := quality.DefaultPolicy()
	p.FreshSeconds = 30
	setTestQualityPolicy(t, a, other, p)
	seedEngineSamples(t, a, w, other, 5, true, 100, 300)
	if _, err := a.db.Exec(ctx, `INSERT INTO quality_states(account_id,baseline_priority,desired_priority,status,last_sample_at) VALUES($1,20,20,'healthy',now()-interval '300 seconds')`, other); err != nil {
		t.Fatal(err)
	}
	p = quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	limit := 10.0
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.db.Exec(ctx, `INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,checked_at) VALUES($1,'test','ok',0,now())`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if !remote.schedulable {
		t.Fatal("stale backup authorized pause")
	}
	view, err := a.qualityView(ctx, other, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Eligible || view.State.Status != "unknown" {
		t.Fatalf("dashboard disagrees: %+v", view.State)
	}
}
func TestEngineOptionalCapacityControlsAndRestore(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoConcurrency = true
	p.AutoLoadFactor = true
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 40 || remote.concurrency != 2 || remote.loadFactor != 4 {
		t.Fatalf("controls: priority=%d concurrent=%d load=%d", remote.priority, remote.concurrency, remote.loadFactor)
	}
	req := qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err := a.qualityReleaseHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 20 || remote.concurrency != 8 || remote.loadFactor != 16 {
		t.Fatal("release did not restore owned baseline fields")
	}
	p, err := a.loadQualityPolicy(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "observe" || p.AutoConcurrency || p.AutoLoadFactor {
		t.Fatal("release did not stop controls")
	}
}
func TestEnginePriceRiskCannotBeClearedByProbes(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.CooldownSeconds = 0
	setTestQualityPolicy(t, a, w.ID, p)
	for _, rate := range []float64{1, 2} {
		remote.rate = rate
		if _, err := a.runRateSync(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	seedEngineSamples(t, a, w, w.ID, 5, true, 100, 0)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET last_rate_sync_at=now()-interval '1 hour' WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		seedEngineSamples(t, a, w, w.ID, 1, true, 100, 0)
		if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	state, _, err := a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if state.Tier != 1 || len(state.Risks) != 1 || state.Risks[0].Kind != "price" || !state.Risks[0].Unknown || remote.priority != 30 {
		t.Fatalf("lost price hold: %+v", state)
	}
	if _, err = a.runRateSync(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	req := qualityHandlerRequest("POST", "/quality/reference", w.ID, w.OwnerID, `{"rate":2}`)
	if err = a.engineReferenceHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 20 {
		t.Fatal("accepted current reference did not recover")
	}
}
func TestEngineTrafficRunsWhenProbeDisabled(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET health_enabled=false,rate_sync_enabled=false WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `UPDATE sites SET next_inventory_at=now()+interval '1 hour',next_reconcile_at=now()+interval '1 hour' WHERE id=$1`, w.SiteID); err != nil {
		t.Fatal(err)
	}
	scheduler := a.NewScheduler()
	task, err := scheduler.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "traffic" {
		t.Fatalf("not independently scheduled: %+v", task)
	}
	scheduler.execute(ctx, task)
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT snapshot FROM quality_traffic WHERE account_id=$1`, w.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"status": "ok"`) {
		t.Fatalf("traffic=%s", raw)
	}
}
func TestEngineRiskPersistenceSurvivesReload(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	seedEngineSamples(t, a, w, w.ID, 4, true, 35000, 0)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	s, _, err := a.readQualityState(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.Risks)
	if len(s.Risks) != 1 || s.Risks[0].Kind != "slow" || s.EvaluatedAt == nil || time.Since(*s.EvaluatedAt) > time.Minute {
		t.Fatalf("lost structured state: %s", raw)
	}
}

func TestEngineFailedRestoreStopsBackgroundWriter(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	remote.rejectWrites = true
	req := qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err := a.qualityReleaseHandler(httptest.NewRecorder(), req); err == nil {
		t.Fatal("restore failure hidden")
	}
	p, err := a.loadQualityPolicy(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "observe" {
		t.Fatal("failed release left automatic writer active")
	}
	count := len(remote.writes)
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if len(remote.writes) != count {
		t.Fatal("background repeated cancelled control")
	}
	remote.rejectWrites = false
	req = qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err = a.qualityReleaseHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 20 {
		t.Fatal("retry failed to restore baseline")
	}
}

func TestEngineFailedPromotionDoesNotApplyDependentDemotion(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.priority = 1
	remote.backupPriority = 100
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET priority=1 WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	_, other := seedEnginePool(t, a, w, 100)
	seedEngineSamples(t, a, w, w.ID, 3, false, 100, 0)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	setTestQualityPolicy(t, a, other, p)
	// The fixture's account 8 rejects changes by returning its unchanged priority.
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("failed promotion was hidden")
	}
	if remote.priority != 1 || len(remote.writes) != 0 {
		t.Fatal("dependent demotion proceeded after failed promotion")
	}
}
