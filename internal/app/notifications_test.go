package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func notificationRequest(method, owner, key, id, body string) *http.Request {
	r := qualityHandlerRequest(method, "/notifications", "", owner, body)
	route := chi.NewRouteContext()
	route.URLParams.Add(key, id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
}
func seedNotificationChannel(t *testing.T, a *App, w AccountWork, url string, categories []string) string {
	t.Helper()
	id := uuid.NewString()
	purpose := "notification-channel:" + w.OwnerID + ":" + id
	sealed, err := a.cipher.Encrypt(url, purpose)
	if err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `INSERT INTO notification_channels(id,owner_id,name,provider,enabled,categories,webhook_ciphertext,secret_purpose) VALUES($1,$2,'receiver','webhook',true,$3,$4,$5)`, id, w.OwnerID, categories, sealed, purpose)
	return id
}
func seedNotificationEvent(t *testing.T, a *App, w AccountWork, kind string) string {
	t.Helper()
	id := uuid.NewString()
	engineRegressionSQL(t, a, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message) VALUES($1,$2,$3,$4,'synthetic event')`, id, w.OwnerID, w.ID, kind)
	return id
}
func TestNotificationChannelsHaveIndependentRetriesAndLeases(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	var good, bad atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			bad.Add(1)
			http.Error(out, "unavailable", 503)
			return
		}
		good.Add(1)
		out.WriteHeader(204)
	}))
	defer server.Close()
	seedNotificationChannel(t, a, w, server.URL+"/good", []string{"quality"})
	badID := seedNotificationChannel(t, a, w, server.URL+"/bad", []string{"quality"})
	event := seedNotificationEvent(t, a, w, "degraded")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); a.sendNotifications(ctx, "") }()
	}
	wg.Wait()
	for range 4 {
		engineRegressionSQL(t, a, `UPDATE notification_deliveries SET next_attempt_at=now() WHERE channel_id=$1`, badID)
		a.sendNotifications(ctx, "")
	}
	if good.Load() != 1 || bad.Load() != 5 {
		t.Fatal(good.Load(), bad.Load())
	}
	var count int
	var last string
	if err := a.db.QueryRow(ctx, `SELECT count(*),max(last_error) FROM notification_deliveries WHERE event_id=$1 AND status='failed'`, event).Scan(&count, &last); err != nil || count != 1 {
		t.Fatal(count, last, err)
	}
	// A new recipient never receives historical records, including failed ones.
	seedNotificationChannel(t, a, w, server.URL+"/new", []string{"quality"})
	a.sendNotifications(ctx, "")
	if good.Load() != 1 {
		t.Fatal("new channel replayed historical data")
	}
	// A lost fifth claim becomes failed, not permanently in flight.
	engineRegressionSQL(t, a, `UPDATE notification_deliveries SET status='sending',lease_until=now()-interval '1 second' WHERE channel_id=$1`, badID)
	a.sendNotifications(ctx, "")
	var status string
	_ = a.db.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE channel_id=$1`, badID).Scan(&status)
	if status != "failed" {
		t.Fatal(status)
	}
}
func TestNotificationChannelEditsAndOwnership(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	channel := seedNotificationChannel(t, a, w, "http://127.0.0.1:1", []string{"quality"})
	seedNotificationEvent(t, a, w, "degraded")
	body := `{"name":"renamed","provider":"webhook","enabled":true,"categories":["quality"],"revision":0}`
	if err := a.saveNotificationChannelHandler(httptest.NewRecorder(), notificationRequest("PUT", uuid.NewString(), "channelID", channel, body)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-owner edit accepted", err)
	}
	if err := a.saveNotificationChannelHandler(httptest.NewRecorder(), notificationRequest("PUT", w.OwnerID, "channelID", channel, body)); err != nil {
		t.Fatal(err)
	}
	var state, name string
	if err := a.db.QueryRow(ctx, `SELECT status,channel_name FROM notification_deliveries WHERE channel_id=$1`, channel).Scan(&state, &name); err != nil || state != "cancelled" || name != "receiver" {
		t.Fatal(state, name, err)
	}
	if err := a.saveNotificationChannelHandler(httptest.NewRecorder(), notificationRequest("PUT", w.OwnerID, "channelID", channel, body)); err == nil {
		t.Fatal("stale revision accepted")
	}
	secretBody := `{"name":"Feishu","provider":"feishu","enabled":false,"categories":["quality"],"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/synthetic-private-url","signing_secret":"synthetic-private-signature"}`
	if err := a.saveNotificationChannelHandler(httptest.NewRecorder(), notificationRequest("POST", w.OwnerID, "", "", secretBody)); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	if err := a.notificationsHandler(out, notificationRequest("GET", w.OwnerID, "", "", "")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Body.String(), "synthetic-private") || strings.Contains(out.Body.String(), "ciphertext") {
		t.Fatal("channel secrets leaked through API")
	}
}
func TestMessageCenterMigrationPreservesBothLegacyDestinations(t *testing.T) {
	a, w, _, _ := newQualityIntegrationMigrating(t, func(ctx context.Context, pool *pgxpool.Pool) error {
		files, err := os.ReadDir("../database/migrations")
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.IsDir() || f.Name() >= "033_" {
				continue
			}
			raw, err := os.ReadFile("../database/migrations/" + f.Name())
			if err != nil {
				return err
			}
			if _, err = pool.Exec(ctx, string(raw)); err != nil {
				return err
			}
		}
		return nil
	})
	ctx := context.Background()
	q, _ := a.cipher.Encrypt("https://quality.example.test/hook", "quality-alert:"+w.OwnerID)
	b, _ := a.cipher.Encrypt("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic-legacy-key", "balance-alert:"+w.OwnerID)
	engineRegressionSQL(t, a, `INSERT INTO quality_alert_settings(owner_id,enabled,webhook_ciphertext) VALUES($1,true,$2)`, w.OwnerID, q)
	engineRegressionSQL(t, a, `INSERT INTO balance_alert_settings(owner_id,enabled,threshold,cooldown_seconds,webhook_url_ciphertext) VALUES($1,false,12,900,$2)`, w.OwnerID, b)
	event := seedNotificationEvent(t, a, w, "degraded")
	raw, err := os.ReadFile("../database/migrations/033_message_center.sql")
	if err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, string(raw))
	rows, err := a.db.Query(ctx, `SELECT legacy_source,enabled,webhook_ciphertext,secret_purpose,categories FROM notification_channels WHERE owner_id=$1`, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var source, cipher, purpose string
		var enabled bool
		var categories []string
		if err = rows.Scan(&source, &enabled, &cipher, &purpose, &categories); err != nil {
			t.Fatal(err)
		}
		value, e := a.cipher.Decrypt(cipher, purpose)
		if e != nil {
			t.Fatal(e)
		}
		if source == "quality" && (!enabled || value != "https://quality.example.test/hook" || len(categories) != 5) {
			t.Fatal("quality settings changed")
		}
		if source == "balance" && (enabled || !strings.Contains(value, "synthetic-legacy-key") || len(categories) != 1 || categories[0] != "balance") {
			t.Fatal("balance settings changed")
		}
		count++
	}
	rows.Close()
	if count != 2 {
		t.Fatal(count)
	}
	var deliveries int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE event_id=$1`, event).Scan(&deliveries)
	if deliveries != 1 {
		t.Fatal("legacy event lost or sent to balance recipient", deliveries)
	}
	rules, err := a.loadNotificationRules(ctx, w.OwnerID)
	if err != nil || rules.BalanceEnabled || rules.BalanceThreshold != 12 || rules.BalanceCooldownSeconds != 900 {
		t.Fatal(rules, err)
	}
}
func TestBalanceNotificationFreshnessRecoveryAndQueue(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	engineRegressionSQL(t, a, `INSERT INTO notification_rules(owner_id,balance_enabled,balance_threshold,balance_cooldown_seconds) VALUES($1,true,10,300)`, w.OwnerID)
	seedNotificationChannel(t, a, w, "http://127.0.0.1:1", []string{"balance"})
	works, err := a.loadAccountBalanceWork(ctx, w.OwnerID, []string{w.ID})
	if err != nil {
		t.Fatal(err)
	}
	bw := works[0]
	cache := accountBalanceCacheKey(bw)
	engineRegressionSQL(t, a, `INSERT INTO account_balance_snapshots(account_id,cache_key,status,remaining,unit,checked_at,source_generation) VALUES($1,$2,'ok',5,'USD',now(),$3)`, w.ID, cache, w.SourceGeneration)
	for range 2 {
		if err = a.evaluateBalanceNotifications(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE kind='balance_low'`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate low balance", count)
	}
	engineRegressionSQL(t, a, `UPDATE account_balance_snapshots SET remaining=20,checked_at=now()-interval '1 hour' WHERE account_id=$1`, w.ID)
	if err = a.evaluateBalanceNotifications(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE kind='balance_recovered'`).Scan(&count)
	if count != 0 {
		t.Fatal("stale balance recovered")
	}
	engineRegressionSQL(t, a, `UPDATE account_balance_snapshots SET checked_at=now() WHERE account_id=$1`, w.ID)
	for range 2 {
		if err = a.evaluateBalanceNotifications(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE kind='balance_recovered'`).Scan(&count)
	if count != 1 {
		t.Fatal("missing or duplicate recovery", count)
	}
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE attempts=0 AND status='pending'`).Scan(&count)
	if count != 2 {
		t.Fatal("balance bypassed shared queue", count)
	}
}
func TestPriceNotificationsAccumulateAndRespectCooldown(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	emit := func(rate float64) {
		t.Helper()
		if err := a.persistRateObservation(ctx, w, rateSyncOutcome{SourceRate: rate, EffectiveRate: rate}); err != nil {
			t.Fatal(err)
		}
	}
	emit(1)
	emit(1.02)
	emit(1.04)
	emit(1.06)
	emit(1.2)
	var count int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE kind='price_change'`).Scan(&count)
	if count != 1 {
		t.Fatal("price spam or small changes never accumulated", count)
	}
	engineRegressionSQL(t, a, `UPDATE notification_price_states SET last_event_at=now()-interval '2 hours' WHERE account_id=$1`, w.ID)
	emit(1.2)
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM quality_notifications WHERE kind='price_change'`).Scan(&count)
	if count != 2 {
		t.Fatal("suppressed stable price was lost", count)
	}
	var detail []byte
	_ = a.db.QueryRow(ctx, `SELECT context FROM quality_notifications WHERE kind='price_change' ORDER BY created_at DESC LIMIT 1`).Scan(&detail)
	var c map[string]any
	if json.Unmarshal(detail, &c) != nil || c["previous_rate"] == nil || c["site_name"] == nil {
		t.Fatal("missing evidence context")
	}
}

func TestNotificationLeaseFencesLateReceiptsAndChannelEdits(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-finish
		out.WriteHeader(204)
	}))
	defer server.Close()
	channel := seedNotificationChannel(t, a, w, server.URL, []string{"quality"})
	seedNotificationEvent(t, a, w, "degraded")
	done := make(chan struct{})
	go func() { defer close(done); a.sendNotifications(ctx, "") }()
	<-started
	body := `{"name":"renamed","provider":"webhook","enabled":true,"categories":["quality"],"revision":0}`
	err := a.saveNotificationChannelHandler(httptest.NewRecorder(), notificationRequest("PUT", w.OwnerID, "channelID", channel, body))
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "CHANNEL_BUSY" {
		close(finish)
		<-done
		t.Fatal("in-flight destination changed", err)
	}
	engineRegressionSQL(t, a, `UPDATE notification_deliveries SET status='pending',lease_token=gen_random_uuid() WHERE channel_id=$1`, channel)
	close(finish)
	<-done
	var state string
	_ = a.db.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE channel_id=$1`, channel).Scan(&state)
	if state != "pending" {
		t.Fatal("old acknowledgement overwrote new lease", state)
	}
}
func TestNotificationSourceChangeAndDisabledChannelsStopDelivery(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	var sent atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { sent.Add(1); out.WriteHeader(204) }))
	defer server.Close()
	channel := seedNotificationChannel(t, a, w, server.URL, []string{"quality"})
	seedNotificationEvent(t, a, w, "degraded")
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET source_generation=source_generation+1 WHERE id=$1`, w.ID)
	a.sendNotifications(ctx, "")
	var status string
	_ = a.db.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE channel_id=$1`, channel).Scan(&status)
	if status != "cancelled" || sent.Load() != 0 {
		t.Fatal(status, sent.Load())
	}
	engineRegressionSQL(t, a, `UPDATE notification_channels SET enabled=false WHERE id=$1`, channel)
	seedNotificationEvent(t, a, w, "healthy")
	var count int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, channel).Scan(&count)
	if count != 1 {
		t.Fatal("disabled channel accumulated new deliveries", count)
	}
}
