package app

import (
	"context"
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/quality"
	"testing"
	"time"
)

func TestStableHoldDoesNotBlockHardFailureOrOwnedRecovery(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Minute)
	base := engineWork{Pools: []string{"g"}, Old: quality.State{Status: "healthy", LastControlAppliedAt: &old}, Decision: quality.Decision{State: quality.State{Status: "healthy"}}}
	policies := map[string]quality.GroupPolicy{"g": quality.DefaultGroupPolicy()}
	p := base
	configureStableControl(&p, policies, now)
	if !p.ControlsSuppressed {
		t.Fatal("healthy hold missing")
	}
	p = base
	p.Decision.HardFailure = true
	configureStableControl(&p, policies, now)
	if p.ControlsSuppressed {
		t.Fatal("hard failure held")
	}
	p = base
	p.Old.OwnedPause = true
	configureStableControl(&p, policies, now)
	if p.ControlsSuppressed {
		t.Fatal("owned pause recovery held")
	}
	policy := policies["g"]
	policy.RolloutPercent = 0
	policies["g"] = policy
	p = base
	p.Decision.HardFailure = true
	configureStableControl(&p, policies, now)
	if !p.ControlsSuppressed {
		t.Fatal("disabled rollout ignored")
	}
}

func TestPerCycleLimitPromotesThenDefersDemotion(t *testing.T) {
	a, w, remote, _ := newQualityIntegration(t)
	ctx := context.Background()
	remote.backupPriority = 100
	remote.acceptBackupWrites = true
	group, other := seedEnginePool(t, a, w, 100)
	seedEngineSamples(t, a, w, w.ID, 5, false, 100, 0)
	seedEngineSamples(t, a, w, other, 5, true, 100, 0)
	p := quality.DefaultPolicy()
	p.Mode = "priority"
	setTestQualityPolicy(t, a, w.ID, p)
	setTestQualityPolicy(t, a, other, p)
	g := quality.DefaultGroupPolicy()
	g.MaxChanges = 1
	raw, _ := json.Marshal(g)
	engineRegressionSQL(t, a, `INSERT INTO engine_group_policies(group_id,model,config) VALUES($1,'test-model',$2)`, group, raw)
	result, _, _, err := a.runEngine(ctx, w.SiteID, w.OwnerID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed != 1 || remote.backupPriority != 20 || remote.priority != 20 {
		t.Fatalf("partial rollout unsafe: %+v %d/%d", result, remote.priority, remote.backupPriority)
	}
	result, _, _, err = a.runEngine(ctx, w.SiteID, w.OwnerID, w.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed != 1 || remote.priority <= remote.backupPriority {
		t.Fatalf("deferred action lost: %+v %d/%d", result, remote.priority, remote.backupPriority)
	}
	var actions int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM engine_actions`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("missing action trace %d %v", actions, err)
	}
}

func TestActionEffectsRequireComparableEnoughData(t *testing.T) {
	pct, latency := 100.0, 1000
	before := []actionSLI{{ProfileID: "p", Generation: 1, MinimumSamples: 5, Samples: 5, SuccessPercent: &pct, P95: &latency}}
	after := append([]actionSLI(nil), before...)
	after[0].Samples = 2
	if status, _ := compareActionSLI(before, after); status != "unverified" {
		t.Fatal(status)
	}
	after[0].Samples = 5
	bad := 80.0
	after[0].SuccessPercent = &bad
	if status, _ := compareActionSLI(before, after); status != "regressed" {
		t.Fatal(status)
	}
	after[0].SuccessPercent = &pct
	fast := 500
	after[0].P95 = &fast
	if status, _ := compareActionSLI(before, after); status != "improved" {
		t.Fatal(status)
	}
	after[0].Generation = 2
	if status, _ := compareActionSLI(before, after); status != "unverified" {
		t.Fatal(status)
	}
}
