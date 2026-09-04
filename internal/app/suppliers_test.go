package app

import (
	"context"
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"testing"
	"time"
)

func TestSupplierIndependenceUsesConfirmedFailureComponents(t *testing.T) {
	m := []supplierMember{{ID: "a", CurrentSource: true, Config: AccountOperations{Provider: "p1", FailureDomain: "d1", QuotaPool: "q1", Confirmed: true}}, {ID: "b", CurrentSource: true, Config: AccountOperations{Provider: "p1", FailureDomain: "d2", QuotaPool: "q2", Confirmed: true}}, {ID: "c", CurrentSource: true, Config: AccountOperations{Provider: "p2", FailureDomain: "d3", QuotaPool: "q2", Confirmed: true}}, {ID: "d", CurrentSource: true, Config: AccountOperations{Provider: "p4", FailureDomain: "d4", QuotaPool: "q4", Confirmed: true}}}
	groups := supplierComponents(m)
	if groups["a"] != groups["b"] || groups["b"] != groups["c"] || groups["d"] == groups["a"] {
		t.Fatal(groups)
	}
	m[3].CredentialIdentity = "shared-key"
	m[0].CredentialIdentity = "shared-key"
	groups = supplierComponents(m)
	if groups["a"] != groups["d"] {
		t.Fatal("same URL/key miscounted as independent")
	}
	m[0].CurrentSource = false
	if m[0].independentKnown() {
		t.Fatal("old source confirmation reused")
	}
}

func TestNativeFallbackZeroDoesNotCertifyCapacity(t *testing.T) {
	now := time.Now()
	limit, current := 8, 0
	w := AccountWork{Account: Account{RemoteStatus: "active", Schedulable: true}, NativeCheckedAt: &now, NativeConstraints: upstream.NativeConstraints{Concurrency: &limit, CurrentConcurrency: &current}}
	v := capacityView(w, now)
	if v.Status != "unknown" || v.VerifiedSpare != nil || v.ReportedSpare == nil || *v.ReportedSpare != 8 {
		t.Fatalf("fallback zero certified: %+v", v)
	}
	queue := 0
	w.NativeConstraints.QueueDepth = &queue
	w.NativeConstraints.CapacityVerified = true
	v = capacityView(w, now)
	if v.Status != "known" || *v.VerifiedSpare != 8 {
		t.Fatal(v)
	}
	current = 8
	if v = capacityView(w, now); v.Status != "unavailable" {
		t.Fatal(v)
	}
}

func TestSameSupplierKeysCannotAuthorizePause(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	_, other := seedEnginePool(t, a, w, 20)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	config := AccountOperations{Provider: w.ID, FailureDomain: "different-label", QuotaPool: "different-pool", Confirmed: true}
	raw, _ := json.Marshal(config)
	engineRegressionSQL(t, a, `UPDATE account_operations SET config=$2 WHERE account_id=$1`, other, raw)
	zero, limit := 0.0, 10.0
	works, _ := a.loadAccountBalanceWork(context.Background(), w.OwnerID, []string{w.ID})
	if err := a.saveAccountBalanceSnapshots(context.Background(), accountBalanceCacheKey(works[0]), works, upstream.BalanceResult{Status: "ok", Remaining: &zero}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	p.AutoPause = true
	p.LowBalance = &limit
	setTestQualityPolicy(t, a, w.ID, p)
	d, err := a.evaluateQuality(context.Background(), w, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HardFailure || !remote.schedulable {
		t.Fatalf("same supplier authorized pause: %+v", d)
	}
}
