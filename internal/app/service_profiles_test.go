package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func seedServiceProfile(t *testing.T, a *App, w AccountWork, base string, budget int) string {
	t.Helper()
	group, _ := seedEnginePool(t, a, w, 20)
	id := uuid.NewString()
	p := defaultServiceProfile()
	p.Name = "Synthetic group check"
	p.Model = "test-model"
	p.Protocol = "chat"
	p.Stream = false
	p.GroupKeyConfirmed = true
	p.BaseURL = base
	p.Budget.DailyRequests = budget
	raw, _ := json.Marshal(p)
	key, err := a.cipher.Encrypt("private-test-group-key", "service-profile:"+id)
	if err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `INSERT INTO service_profiles(id,group_id,config,key_ciphertext) VALUES($1,$2,$3,$4)`, id, group, raw, key)
	return id
}

func TestCanaryReservationsAreConcurrentAndBudgetSafe(t *testing.T) {
	a, w, _, url := newQualityIntegration(t)
	id := seedServiceProfile(t, a, w, url, 1)
	var won atomic.Int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := a.reserveCanary(context.Background(), id, w.OwnerID, false); err == nil {
				won.Add(1)
			}
		}()
	}
	wait.Wait()
	if won.Load() != 1 {
		t.Fatalf("concurrent reservations=%d", won.Load())
	}
	engineRegressionSQL(t, a, `UPDATE service_canary_runs SET status='passed',completed_at=now() WHERE profile_id=$1`, id)
	_, err := a.reserveCanary(context.Background(), id, w.OwnerID, false)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "CANARY_BUDGET" {
		t.Fatalf("daily cap bypassed: %v", err)
	}
	var count int
	if err = a.db.QueryRow(context.Background(), `SELECT count(*) FROM service_canary_runs WHERE profile_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("extra charged attempts reserved", count, err)
	}
}

func TestCanaryCannotCertifyGroupFromAccountProbe(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	remote.success = true
	account, err := a.runProbe(context.Background(), w, "manual", w.OwnerID)
	if err != nil || !account.Success {
		t.Fatal("account probe setup", err)
	}
	var calls atomic.Int32
	entry := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { calls.Add(1); out.WriteHeader(503) }))
	defer entry.Close()
	id := seedServiceProfile(t, a, w, entry.URL, 2)
	result, err := a.runServiceCanary(context.Background(), id, w.OwnerID, false)
	if err != nil || result.Success || result.HTTPStatus != 503 || calls.Load() != 1 {
		t.Fatalf("group failure hidden: %+v %v", result, err)
	}
	var status string
	if err = a.db.QueryRow(context.Background(), `SELECT status FROM service_canary_runs WHERE profile_id=$1`, id).Scan(&status); err != nil || status != "failed" {
		t.Fatal("final outcome not recorded", err)
	}
}

func TestCanaryLateResultKeepsOriginalProfileGeneration(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	started, finish := make(chan struct{}), make(chan struct{})
	entry := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		_, _ = out.Write([]byte(`{"model":"test-model","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer entry.Close()
	id := seedServiceProfile(t, a, w, entry.URL, 5)
	done := make(chan error, 1)
	go func() { _, err := a.runServiceCanary(context.Background(), id, w.OwnerID, false); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("not started")
	}
	engineRegressionSQL(t, a, `UPDATE service_profiles SET generation=generation+1 WHERE id=$1`, id)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p, err := a.loadServiceProfile(context.Background(), id, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if p.LastProbeAt != nil {
		t.Fatal("late result overwrote current profile state")
	}
	var generation int64
	var snapshot []byte
	if err = a.db.QueryRow(context.Background(), `SELECT generation,profile_snapshot FROM service_canary_runs WHERE profile_id=$1`, id).Scan(&generation, &snapshot); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || strings.Contains(string(snapshot), "private-test-group-key") {
		t.Fatal("lost lineage or exposed key")
	}
	public, _ := json.Marshal(p)
	if strings.Contains(string(public), p.KeyCiphertext) || strings.Contains(string(public), "private-test-group-key") {
		t.Fatal("profile API exposes key")
	}
}

func TestCanaryOwnershipAndUnknownCostGuard(t *testing.T) {
	a, w, _, url := newQualityIntegration(t)
	id := seedServiceProfile(t, a, w, url, 5)
	if _, err := a.reserveCanary(context.Background(), id, uuid.NewString(), false); err == nil {
		t.Fatal("cross-owner canary allowed")
	}
	p := defaultServiceProfile()
	p.Name = "test"
	p.GroupKeyConfirmed = true
	p.Model = "test"
	daily := 1.0
	p.Budget.DailyCost = &daily
	if p.Validate() == nil {
		t.Fatal("unknown cost accepted with monetary cap")
	}
	cost := .1
	p.Budget.RequestCostReserve = &cost
	p.Budget.Currency = "USD"
	p.Budget.CostBasis = "Synthetic documented rate"
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.reserveCanary(context.Background(), id, w.OwnerID, false); err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `UPDATE service_canary_runs SET status='failed',completed_at=now() WHERE profile_id=$1`, id)
	raw, _ := json.Marshal(p)
	engineRegressionSQL(t, a, `UPDATE service_profiles SET config=$2 WHERE id=$1`, id, raw)
	_, err := a.reserveCanary(context.Background(), id, w.OwnerID, false)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "COST_NOT_COMPARABLE" {
		t.Fatalf("unknown historical costs erased: %v", err)
	}
}

func TestAccountProfilesUseEphemeralMatchedCredentials(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	var calls atomic.Int32
	var exportKey atomic.Value
	exportKey.Store("source-test-key")
	provider := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer source-test-key" {
			t.Error("wrong supplier credential")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "supplier-model" {
			t.Error("direct profile applied an unexpected model mapping")
		}
		_, _ = out.Write([]byte(`{"model":"supplier-model","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	exporter := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accounts/data") {
			_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"accounts": []any{map[string]any{"id": 7, "credentials": map[string]any{"base_url": provider.URL, "api_key": exportKey.Load().(string)}}}}})
			return
		}
		remote.serve(out, r)
	}))
	defer exporter.Close()
	engineRegressionSQL(t, a, `UPDATE sites SET base_url=$2 WHERE id=$1`, w.SiteID, exporter.URL)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("source-test-key")))
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_base_url=$2,observed_source_credential_fingerprint=$3 WHERE id=$1`, w.ID, provider.URL, fingerprint)
	id := uuid.NewString()
	config := defaultServiceProfile()
	config.Name = "Account direct"
	config.Model = "supplier-model"
	config.Protocol = "chat"
	config.Stream = false
	config.DirectSourceConfirmed = true
	config.Budget.DailyRequests = 2
	raw, _ := json.Marshal(config)
	engineRegressionSQL(t, a, `INSERT INTO service_profiles(id,account_id,config) VALUES($1,$2,$3)`, id, w.ID, raw)
	result, err := a.runServiceCanary(ctx, id, w.OwnerID, false)
	if err != nil || !result.Success || calls.Load() != 1 {
		t.Fatalf("direct profile failed: %+v %v", result, err)
	}
	p, err := a.loadServiceProfile(ctx, id, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyCiphertext != "" || p.GroupID != "" || p.AccountID != w.ID {
		t.Fatal("supplier key persisted or scope mixed")
	}
	exportKey.Store("rotated-source-test-key")
	result, err = a.runServiceCanary(ctx, id, w.OwnerID, false)
	if err == nil || !result.ControlError || calls.Load() != 1 {
		t.Fatal("mismatched source was probed", result, err)
	}
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET observed_source_credential_fingerprint=repeat('d',64) WHERE id=$1`, w.ID)
	fresh, err := a.loadServiceProfile(ctx, id, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation <= p.Generation {
		t.Fatal("source change did not invalidate account profile evidence")
	}
}

func TestServiceProfileEditRequiresCurrentGeneration(t *testing.T) {
	a, w, _, url := newQualityIntegration(t)
	id := seedServiceProfile(t, a, w, url, 5)
	p, err := a.loadServiceProfile(context.Background(), id, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	engineRegressionSQL(t, a, `UPDATE service_profiles SET generation=generation+1 WHERE id=$1`, id)
	body, _ := json.Marshal(map[string]any{"id": id, "group_id": p.GroupID, "generation": p.Generation, "config": p.Config})
	request := qualityHandlerRequest("POST", "/service-profiles", id, w.OwnerID, string(body))
	recorder := httptest.NewRecorder()
	err = a.saveServiceProfileHandler(recorder, request)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("stale editor overwrote profile: %v", err)
	}
}
