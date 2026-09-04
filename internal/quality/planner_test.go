package quality

import (
	"testing"
	"time"
)

func TestPlanRanksBadPrimaryBehindHealthyBackup(t *testing.T) {
	nodes := []Candidate{{ID: "bad", Pools: []string{"group:model"}, Baseline: 1, Current: 1, Desired: 21, Tier: 2, Mutable: true, Available: true}, {ID: "good", Pools: []string{"group:model"}, Baseline: 100, Current: 100, Desired: 100, Healthy: true, Available: true}}
	result := Plan(nodes, nil)
	if result["bad"].Priority <= 100 || result["good"].Priority != 100 {
		t.Fatalf("plan=%+v", result)
	}
	nodes[1].Mutable = true
	result = Plan(nodes, nil)
	if result["bad"].Priority <= result["good"].Priority {
		t.Fatalf("mutable plan=%+v", result)
	}
}
func TestPlanStrategiesAndSharedAccountConflict(t *testing.T) {
	cheap, expensive := 1.0, 2.0
	slow, fast := 1000, 100
	nodes := []Candidate{{ID: "a", Pools: []string{"g"}, Baseline: 20, Current: 20, Healthy: true, Mutable: true, Available: true, Price: &cheap, Latency: &slow}, {ID: "b", Pools: []string{"g"}, Baseline: 20, Current: 20, Healthy: true, Mutable: true, Available: true, Price: &expensive, Latency: &fast}}
	price := DefaultGroupPolicy()
	price.Strategy = PriceFirst
	speed := price
	speed.Strategy = SpeedFirst
	p := Plan(nodes, map[string]GroupPolicy{"g": price})
	if p["a"].Priority >= p["b"].Priority {
		t.Fatal(p)
	}
	p = Plan(nodes, map[string]GroupPolicy{"g": speed})
	if p["b"].Priority >= p["a"].Priority {
		t.Fatal(p)
	}
	balanced := DefaultGroupPolicy()
	balanced.PriceWeight = 3
	balanced.SpeedWeight = 1
	p = Plan(nodes, map[string]GroupPolicy{"g": balanced})
	if p["a"].Priority >= p["b"].Priority {
		t.Fatal("balanced weights ignored", p)
	}
	balanced.PriceWeight = 1
	balanced.SpeedWeight = 3
	p = Plan(nodes, map[string]GroupPolicy{"g": balanced})
	if p["b"].Priority >= p["a"].Priority {
		t.Fatal("balanced speed weight ignored", p)
	}
	for i := range nodes {
		nodes[i].Pools = []string{"g1", "g2"}
	}
	p = Plan(nodes, map[string]GroupPolicy{"g1": price, "g2": speed})
	if p["a"].Error == "" || p["b"].Priority != 20 {
		t.Fatal(p)
	}
}
func TestPlanFixedConflictDoesNotAffectOtherPool(t *testing.T) {
	nodes := []Candidate{{ID: "bad", Pools: []string{"g"}, Current: 0, Tier: 2, Available: true}, {ID: "good", Pools: []string{"g"}, Current: 100, Healthy: true, Mutable: true, Available: true}, {ID: "isolated", Current: 50, Desired: 60, Mutable: true, Available: true, Tier: 1}}
	result := Plan(nodes, nil)
	if result["good"].Error == "" || result["good"].Priority != 100 || result["isolated"].Priority != 60 {
		t.Fatal(result)
	}
}
func TestSlowLowSampleCountAndLatchedPriceRecovery(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.CooldownSeconds = 0
	d := Evaluate(p, State{Baseline: 20, Desired: 20}, Snapshot{Samples: recentSamples(now, 4, true, 35000)}, now)
	if d.State.Tier == 0 {
		t.Fatal("slow stream not detected")
	}
	rate, ref := 2.0, 1.0
	d = Evaluate(p, State{Baseline: 20, Desired: 20}, Snapshot{Samples: recentSamples(now, 5, true, 100), Rate: &rate, ReferenceRate: &ref, RateFresh: true, RateAt: &now}, now)
	for i := 1; i <= 10; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		d = Evaluate(p, d.State, Snapshot{Samples: recentSamples(at, 5, true, 100)}, at)
	}
	if d.State.Tier == 0 || len(d.State.Risks) == 0 || d.State.Risks[0].Kind != "price" {
		t.Fatalf("unknown price released hold: %+v", d)
	}
	at := now.Add(time.Minute)
	rate = 1
	d = Evaluate(p, d.State, Snapshot{Samples: recentSamples(at, 5, true, 100), Rate: &rate, ReferenceRate: &ref, RateFresh: true, RateAt: &at}, at)
	if d.State.Tier != 0 {
		t.Fatalf("fresh accepted price did not recover: %+v", d)
	}
}

func TestBackupNeedsFreshConfiguredFinancialConstraints(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	minimum, maximum := 10.0, 2.0
	p.LowBalance = &minimum
	p.MaxRate = &maximum
	snap := Snapshot{Samples: recentSamples(now, 5, true, 100)}
	if Evaluate(p, State{}, snap, now).Eligible {
		t.Fatal("unknown constrained finances certified as backup")
	}
	balance, rate := 50.0, 1.0
	snap.Balance = &balance
	snap.BalanceFresh = true
	snap.BalanceAt = &now
	snap.Rate = &rate
	snap.RateFresh = true
	snap.RateAt = &now
	if !Evaluate(p, State{}, snap, now).Eligible {
		t.Fatal("fresh valid constrained backup rejected")
	}
}
