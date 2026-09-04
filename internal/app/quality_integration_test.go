package app

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub2api-upstream-manager/internal/config"
	"sub2api-upstream-manager/internal/database"
	"sub2api-upstream-manager/internal/quality"
)

type qualityTestRemote struct {
	mu            sync.Mutex
	priority      int
	rate          float64
	success       bool
	schedulable   bool
	writes        []map[string]any
	notifications int
	rejectWrites  bool
	adminDenied   bool
}

func (s *qualityTestRemote) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
	switch {
	case r.URL.Path == "/notify":
		s.notifications++
		w.WriteHeader(204)
	case strings.HasSuffix(r.URL.Path, "/upstream-billing-probe"):
		send(map[string]any{"snapshot": map[string]any{"status": "ok", "data": map[string]any{"effective_rate_multiplier": s.rate}}})
	case strings.HasSuffix(r.URL.Path, "/test"):
		if s.adminDenied {
			w.WriteHeader(401)
			send(map[string]any{"message": "invalid admin key"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if s.success {
			_, _ = io.WriteString(w, "data: {\"type\":\"test_start\",\"model\":\"test-model\"}\n\ndata: {\"type\":\"content\",\"text\":\"ok\"}\n\ndata: {\"type\":\"test_complete\",\"success\":true}\n\n")
		} else {
			_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":\"503 upstream unavailable\",\"http_status\":503}\n\n")
		}
	case strings.HasSuffix(r.URL.Path, "/ops/requests"):
		send(map[string]any{"items": []any{}, "total": 0})
	case strings.HasSuffix(r.URL.Path, "/schedulable"):
		var data map[string]any
		_ = json.NewDecoder(r.Body).Decode(&data)
		s.writes = append(s.writes, data)
		s.schedulable = data["schedulable"] == true
		send(map[string]any{"id": 7, "schedulable": s.schedulable, "priority": s.priority})
	case strings.HasSuffix(r.URL.Path, "/accounts/7"):
		if r.Method == http.MethodPut {
			var data map[string]any
			_ = json.NewDecoder(r.Body).Decode(&data)
			s.writes = append(s.writes, data)
			if s.rejectWrites {
				w.WriteHeader(503)
				return
			}
			if value, ok := data["priority"].(float64); ok {
				s.priority = int(value)
			}
		}
		send(map[string]any{"id": 7, "priority": s.priority, "rate_multiplier": 1.0, "schedulable": s.schedulable, "status": "active"})
	default:
		http.NotFound(w, r)
	}
}

func newQualityIntegration(t *testing.T) (*App, AccountWork, *qualityTestRemote, string) {
	t.Helper()
	dsn := os.Getenv("SUB2UPSTREAM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUB2UPSTREAM_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "quality_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	app, err := New(config.Config{MasterKey: make([]byte, 32), LogDir: t.TempDir(), AllowPrivateUpstreams: true, Workers: 2, PublicURL: "http://localhost"}, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	remote := &qualityTestRemote{priority: 20, rate: 1, success: false, schedulable: true}
	server := httptest.NewServer(http.HandlerFunc(remote.serve))
	t.Cleanup(server.Close)
	owner, site, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
	secret, err := app.cipher.Encrypt("test-key", "site:"+site)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,email,password_hash,role) VALUES($1,'operator@example.test','unused','admin')`, []any{owner}},
		{`INSERT INTO sites(id,owner_id,name,base_url,api_key_ciphertext) VALUES($1,$2,'test-site',$3,$4)`, []any{site, owner, server.URL, secret}},
		{`INSERT INTO upstream_accounts(id,site_id,remote_id,name,platform,account_type,remote_status,schedulable,priority,rate_multiplier,health_enabled,probe_model) VALUES($1,$2,7,'test-upstream','openai','apikey','active',true,20,1,true,'test-model')`, []any{id, site}},
	} {
		if _, err = pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	work, err := app.loadAccountWork(ctx, id, owner)
	if err != nil {
		t.Fatal(err)
	}
	return app, work, remote, server.URL
}

func setTestQualityPolicy(t *testing.T, a *App, id string, p quality.Policy) {
	t.Helper()
	raw, _ := json.Marshal(p)
	if _, err := a.db.Exec(context.Background(), `INSERT INTO quality_policies(account_id,config) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET config=$2`, id, raw); err != nil {
		t.Fatal(err)
	}
}

func TestQualityObserveDemoteRecoverAndManualOverride(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	p.CooldownSeconds = 0
	p.MinimumSamples = 2
	setTestQualityPolicy(t, a, w.ID, p)
	for i := 0; i < 3; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Desired != 40 || len(remote.writes) != 0 {
		t.Fatalf("observe writes=%v decision=%+v", remote.writes, d)
	}
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	d, err = a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if remote.priority != 40 || len(remote.writes) != 1 || len(remote.writes[0]) != 1 {
		t.Fatalf("priority-only writes=%v", remote.writes)
	}
	// Let the observation window expire, then feed six distinct healthy samples.
	_, err = a.db.Exec(ctx, `UPDATE probe_attempts SET created_at=now()-interval '1 hour' WHERE account_id=$1`, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	remote.success = true
	for i := 0; i < 6; i++ {
		if _, err = a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
		if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	if remote.priority != 20 {
		t.Fatalf("recovery priority=%d", remote.priority)
	}
	remote.priority = 77
	d, err = a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.State.Conflict || remote.priority != 77 {
		t.Fatalf("manual override lost: %+v", d)
	}
	// Cross-owner API access is rejected by the application's ownership query.
	if _, err = a.qualityView(ctx, w.ID, uuid.NewString()); !errorsIsNoRows(err) {
		t.Fatalf("tenant isolation=%v", err)
	}
}

func errorsIsNoRows(err error) bool { return err == pgx.ErrNoRows }

func TestQualityPriceCollectionDoesNotWriteAndReleaseIsSafe(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	if _, err := a.runRateSync(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	remote.rate = 2
	for i := 0; i < 2; i++ {
		if _, err := a.runRateSync(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM upstream_price_history WHERE account_id=$1`, w.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(remote.writes) != 0 {
		t.Fatalf("price history=%d writes=%v", count, remote.writes)
	}
	remote.success = true
	for i := 0; i < 5; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.CooldownSeconds = 0
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 30 {
		t.Fatalf("price rise was not demoted: %d", remote.priority)
	}
	remote.priority = 91
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.priority != 91 {
		t.Fatal("overwrote manual priority")
	}
}

func TestQualityInterruptedWriteRecoveryAndNotifications(t *testing.T) {
	a, w, remote, url := newQualityIntegration(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	remote.rejectWrites = true
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err == nil {
		t.Fatal("expected write failure")
	}
	remote.rejectWrites = false
	remote.priority = 40 // Remote committed before a lost response.
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil || d.State.Conflict {
		t.Fatalf("pending recovery=%+v %v", d, err)
	}
	sealed, err := a.cipher.Encrypt(url+"/notify", "quality-alert:"+w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO quality_alert_settings(owner_id,enabled,webhook_ciphertext) VALUES($1,true,$2)`, w.OwnerID, sealed)
	if err != nil {
		t.Fatal(err)
	}
	a.sendQualityNotifications(ctx)
	var delivered int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE owner_id=$1 AND delivered_at IS NOT NULL`, w.OwnerID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered < 1 || remote.notifications != delivered {
		t.Fatalf("notifications=%d delivered=%d", remote.notifications, delivered)
	}
	before := remote.notifications
	a.sendQualityNotifications(ctx)
	if remote.notifications != before {
		t.Fatal("delivered event sent twice")
	}
}

func TestQualitySchedulerCollectsWithoutRemoteWrites(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `UPDATE sites SET next_inventory_at=now()+interval '1 hour',next_reconcile_at=now()+interval '1 hour' WHERE id=$1`, w.SiteID); err != nil {
		t.Fatal(err)
	}
	scheduler := a.NewScheduler()
	task, err := scheduler.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "probe" {
		t.Fatalf("task=%+v", task)
	}
	scheduler.execute(ctx, task)
	var count int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM probe_attempts WHERE account_id=$1`, w.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(remote.writes) > 0 {
		t.Fatalf("background probe count=%d writes=%v", count, remote.writes)
	}
}

func TestQualityNotificationDeduplicatesCountersButReportsNewRisk(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE account_id=$1`, w.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("same failure generated %d notifications", count)
	}
	p := quality.DefaultPolicy()
	limit := 10.0
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.db.Exec(ctx, `INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,checked_at) VALUES($1,'test','ok',1,now())`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE account_id=$1`, w.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("new low-balance risk not reported: %d", count)
	}
}

func qualityHandlerRequest(method, path, id, owner, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("accountID", id)
	return r.WithContext(context.WithValue(withIdentity(r.Context(), Identity{User: User{ID: owner, Role: "admin"}}), chi.RouteCtxKey, route))
}

func TestQualityPolicyAPIGroupAggregationAndRelease(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	payload, _ := json.Marshal(map[string]any{"mode": p.Mode, "monitoring": map[string]any{"enabled": true, "model": "test-model", "interval_seconds": 120, "timeout_seconds": 45, "collect_rate": true}})
	req := qualityHandlerRequest(http.MethodPut, "/quality/"+w.ID+"/policy", w.ID, w.OwnerID, string(payload))
	if err := a.qualityPolicyHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	updated, err := a.loadAccountWork(ctx, w.ID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProbeIntervalSeconds != 120 || updated.ProbeTimeoutSeconds != 45 || !updated.RateSyncEnabled {
		t.Fatalf("monitoring=%+v", updated)
	}
	invalid := qualityHandlerRequest(http.MethodPut, "/quality/policy", w.ID, w.OwnerID, `{"mode":"invalid"}`)
	if a.qualityPolicyHandler(httptest.NewRecorder(), invalid) == nil {
		t.Fatal("invalid policy accepted")
	}
	group := uuid.NewString()
	if _, err = a.db.Exec(ctx, `INSERT INTO upstream_groups(id,site_id,remote_id,name) VALUES($1,$2,1,'test-group')`, group, w.SiteID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, `INSERT INTO account_group_memberships(account_id,group_id,site_id) VALUES($1,$2,$3)`, w.ID, group, w.SiteID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err = a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	req = qualityHandlerRequest("GET", "/quality/groups", "", w.OwnerID, "")
	if err = a.qualityGroupHandler(response, req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), `"test-model"`) || !strings.Contains(response.Body.String(), `"healthy": 0`) && !strings.Contains(response.Body.String(), `"healthy":0`) {
		t.Fatalf("groups=%s", response.Body)
	}
	response = httptest.NewRecorder()
	req = qualityHandlerRequest("GET", "/quality?model=test-model", w.ID, w.OwnerID, "")
	if err = a.qualityListHandler(response, req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body.String(), "test-upstream") {
		t.Fatalf("quality=%s", response.Body)
	}
	remote.priority = 91
	req = qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":true}`)
	if err = a.qualityReleaseHandler(httptest.NewRecorder(), req); err == nil {
		t.Fatal("release overwrote manual change")
	}
	req = qualityHandlerRequest("POST", "/quality/release", w.ID, w.OwnerID, `{"restore":false}`)
	if err = a.qualityReleaseHandler(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	policy, err := a.loadQualityPolicy(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != "observe" || remote.priority != 91 {
		t.Fatal("release did not preserve current value")
	}
}

func TestQualityPauseRequiresIndependentSwitchAndHealthyModelBackup(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	group := uuid.NewString()
	other := uuid.NewString()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO upstream_groups(id,site_id,remote_id,name) VALUES($1,$2,1,'pool')`, []any{group, w.SiteID}},
		{`INSERT INTO account_group_memberships(account_id,group_id,site_id) VALUES($1,$2,$3)`, []any{w.ID, group, w.SiteID}},
		{`INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,checked_at) VALUES($1,'test','ok',0,now())`, []any{w.ID}},
	} {
		if _, err := a.db.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.CooldownSeconds = 0
	limit := 10.0
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if !remote.schedulable {
		t.Fatal("default policy paused account")
	}
	p.AutoPause = true
	setTestQualityPolicy(t, a, w.ID, p)
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if !remote.schedulable {
		t.Fatal("last healthy backup protection failed")
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO upstream_accounts(id,site_id,remote_id,name,remote_status,schedulable,priority,probe_model) VALUES($1,$2,8,'backup','active',true,20,'other-model')`, []any{other, w.SiteID}},
		{`INSERT INTO account_group_memberships(account_id,group_id,site_id) VALUES($1,$2,$3)`, []any{other, group, w.SiteID}},
		{`INSERT INTO quality_states(account_id,baseline_priority,desired_priority,status,last_sample_at) VALUES($1,20,20,'healthy',now())`, []any{other}},
	} {
		if _, err := a.db.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if !remote.schedulable {
		t.Fatal("different model counted as backup")
	}
	if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET probe_model='test-model' WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
		t.Fatal(err)
	}
	if remote.schedulable {
		t.Fatal("explicit pause with healthy backup not applied")
	}
	remote.success = true
	if _, err := a.db.Exec(ctx, `UPDATE account_balance_snapshots SET remaining=50,checked_at=now() WHERE account_id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.evaluateQuality(ctx, w, w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	if !remote.schedulable {
		t.Fatal("owned pause was not recovered")
	}
}

func TestQualityAdminAuthenticationFailureIsNotSupplierFailure(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.adminDenied = true
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	setTestQualityPolicy(t, a, w.ID, p)
	for i := 0; i < 4; i++ {
		if _, err := a.runProbe(ctx, w, "manual", w.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	d, err := a.evaluateQuality(ctx, w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Status != "unknown" || d.State.Desired != 20 || len(remote.writes) > 0 {
		t.Fatalf("admin failure affected supplier: %+v writes=%v", d, remote.writes)
	}
}
