package quality

import (
	"math"
	"testing"
	"time"
)

func recentSamples(now time.Time, n int, success bool, latency int) []Sample {
	values := []Sample{}
	for i := 0; i < n; i++ {
		ms := latency
		values = append(values, Sample{At: now.Add(-time.Duration(i) * time.Second), Success: success, FirstContentMS: &ms, Model: "test", FailureReason: "UPSTREAM"})
	}
	return values
}
func TestQualityPenaltiesAndRecovery(t *testing.T) {
	now := time.Now().UTC()
	p := DefaultPolicy()
	p.CooldownSeconds = 0
	initial := State{Baseline: 20, Desired: 20}
	slow := Evaluate(p, initial, Snapshot{Samples: recentSamples(now, 5, true, 12000)}, now)
	if slow.State.Tier != 1 || slow.State.Desired != 30 || slow.P95 == nil || *slow.P95 != 12000 {
		t.Fatalf("slow=%+v", slow)
	}
	failed := Evaluate(p, slow.State, Snapshot{Samples: recentSamples(now.Add(time.Second), 3, false, 0)}, now.Add(time.Second))
	if failed.State.Tier != 2 || failed.State.Desired != 40 {
		t.Fatalf("failed=%+v", failed)
	}
	state := failed.State
	for i := 1; i <= 6; i++ {
		at := now.Add(time.Duration(i+2) * time.Second)
		snapshot := Snapshot{Samples: recentSamples(at, 5, true, 100)}
		result := Evaluate(p, state, snapshot, at)
		// Re-evaluating the same newest probe cannot create a second recovery vote.
		repeat := Evaluate(p, result.State, snapshot, at)
		if repeat.State.RecoveryStreak != result.State.RecoveryStreak || repeat.State.Tier != result.State.Tier {
			t.Fatal("same sample counted twice")
		}
		state = result.State
	}
	if state.Tier != 0 || state.Desired != 20 {
		t.Fatalf("not recovered: %+v", state)
	}
}
func TestUnknownAndManualConflictNeverRestore(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()
	last := 40
	old := State{Baseline: 20, LastApplied: &last, Desired: 40, Tier: 2}
	d := Evaluate(p, old, Snapshot{}, now)
	if d.State.Status != "unknown" || d.State.Desired != 40 || d.State.Tier != 2 {
		t.Fatalf("stale=%+v", d)
	}
	old.Conflict = true
	d = Evaluate(p, old, Snapshot{Samples: recentSamples(now, 10, true, 1)}, now)
	if d.State.Status != "conflict" || d.State.Desired != 40 {
		t.Fatal("manual conflict was overwritten")
	}
}
func TestSingleFailureAndRateLimitAreNotPermanentFailure(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()
	s := State{Baseline: 10, Desired: 10}
	d := Evaluate(p, s, Snapshot{Samples: recentSamples(now, 1, false, 0)}, now)
	if d.State.Tier != 0 || d.HardFailure || d.State.Status != "watching" {
		t.Fatalf("single=%+v", d)
	}
	samples := recentSamples(now, 3, false, 0)
	for i := range samples {
		samples[i].FailureReason = "RATE_LIMIT"
	}
	d = Evaluate(p, s, Snapshot{Samples: samples}, now)
	if d.HardFailure {
		t.Fatal("429 must not pause account")
	}
}
func TestCostAndBalanceNeverBecomeZeroOnMissingData(t *testing.T) {
	p := DefaultPolicy()
	threshold := 10.0
	maxRate := 1.5
	p.LowBalance = &threshold
	p.MaxRate = &maxRate
	now := time.Now()
	base := State{Baseline: 10, Desired: 10}
	rate := 2.0
	previous := 1.0
	balance := 0.0
	d := Evaluate(p, base, Snapshot{Samples: recentSamples(now, 5, true, 1), Rate: &rate, PreviousRate: &previous, RateFresh: true, Balance: &balance, BalanceFresh: true}, now)
	if d.State.Tier != 2 || !d.HardFailure {
		t.Fatalf("financial=%+v", d)
	}
	state := d.State
	p.CooldownSeconds = 0
	for i := 1; i <= 6; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		state = Evaluate(p, state, Snapshot{Samples: recentSamples(at, 5, true, 1)}, at).State
	}
	if state.Tier != 2 {
		t.Fatal("missing financial data caused recovery")
	}
	if DefaultPolicy().Mode != "observe" || DefaultPolicy().AutoPause {
		t.Fatal("unsafe defaults")
	}
	p.ErrorPercent = math.NaN()
	if p.Validate() == nil {
		t.Fatal("accepted NaN")
	}
}

func TestFinancialRiskDoesNotRequireModelProbe(t *testing.T) {
	p := DefaultPolicy()
	limit := 10.0
	zero := 0.0
	p.LowBalance = &limit
	d := Evaluate(p, State{Baseline: 10, Desired: 10}, Snapshot{Balance: &zero, BalanceFresh: true}, time.Now())
	if d.State.Desired != 30 || !d.HardFailure || d.State.Status != "degraded" {
		t.Fatalf("zero balance not handled: %+v", d)
	}
}
