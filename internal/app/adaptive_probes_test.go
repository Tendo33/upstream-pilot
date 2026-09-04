package app

import (
	"testing"
	"time"
)

func TestAdaptiveCanaryStaysWithinReservedDailySchedule(t *testing.T) {
	p := defaultServiceProfile()
	p.IntervalSeconds = 3600
	p.Objectives.MinimumSamples = 5
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if d := adaptiveCanaryDelay(p, 0, 20, false, false, now); d >= time.Hour {
		t.Fatal("cold start did not use spare budget", d)
	}
	if d := adaptiveCanaryDelay(p, 0, 12, false, false, now); d != time.Hour {
		t.Fatal("burst consumed ordinary schedule", d)
	}
	if d := adaptiveCanaryDelay(p, 5, 20, true, true, now); d != 3*time.Hour {
		t.Fatal("healthy real evidence did not reduce probes", d)
	}
	p.Adaptive = false
	if d := adaptiveCanaryDelay(p, 0, 20, false, false, now); d != time.Hour {
		t.Fatal(d)
	}
}
func TestSamplingWarningsExposeImpossibleWindows(t *testing.T) {
	w := AccountWork{Account: Account{ProbeIntervalSeconds: 300, RateSyncIntervalSeconds: 600}}
	warnings := samplingWarnings(w, 1000, 1800, 30, true, true)
	if len(warnings) < 4 {
		t.Fatalf("warnings=%v", warnings)
	}
}
