package quality

import (
	"testing"
	"time"
)

func TestIncompleteTrafficCanDegradeButCannotCertifyHealthy(t *testing.T) {
	now := time.Now().UTC()
	p := DefaultPolicy()
	p.PriceRisePercent = 0
	s := Snapshot{TrafficFresh: true, TrafficIncomplete: true, TrafficAt: &now, TrafficTotal: 100, TrafficFailed: 0}
	old := State{Baseline: 10, Desired: 10}
	if d := Evaluate(p, old, s, now); d.State.Status == "healthy" || d.Eligible {
		t.Fatalf("partial feed certified healthy: %+v", d)
	}
	s.TrafficFailed = 3
	if d := Evaluate(p, old, s, now); d.State.Tier > 0 {
		t.Fatalf("small error sample below policy threshold caused demotion: %+v", d)
	}
	s.TrafficTotal = 5
	s.TrafficFailed = 5
	d := Evaluate(p, old, s, now)
	if d.State.Status != "degraded" || d.State.Tier == 0 || d.HardFailure {
		t.Fatalf("observed failures ignored or caused hard pause: %+v", d)
	}
	s.TrafficFailed = 0
	for i := 1; i <= 5; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		s.TrafficAt = &at
		d = Evaluate(p, d.State, s, at)
	}
	if d.State.Tier == 0 {
		t.Fatal("incomplete successes cleared previous risk")
	}
}

func TestTrafficOnlyHealthKeepsItsEvidenceTimestamp(t *testing.T) {
	now := time.Now().UTC()
	p := DefaultPolicy()
	p.PriceRisePercent = 0
	s := Snapshot{TrafficFresh: true, TrafficAt: &now, TrafficTotal: 10}
	d := Evaluate(p, State{Baseline: 10, Desired: 10}, s, now)
	if !d.Eligible || d.State.LastSampleAt == nil || !d.State.LastSampleAt.Equal(now) {
		t.Fatalf("traffic evidence lost: %+v", d)
	}
}
