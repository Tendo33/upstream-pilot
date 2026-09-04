package app

import (
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"testing"
	"time"
)

func TestServiceObjectivesDoNotHideUnconfirmedRequests(t *testing.T) {
	now := time.Now().UTC()
	p := serviceProfileWork{Config: defaultServiceProfile()}
	latency := 40
	samples := []serviceSample{}
	for range 5 {
		samples = append(samples, serviceSample{At: now.Add(-time.Minute), Result: upstream.CanaryResult{Success: true, Complete: true, FirstContentMS: &latency}})
	}
	if got := evaluateServiceObjectives(p, samples, now); got.Status != "healthy" {
		t.Fatalf("valid evidence: %+v", got)
	}
	samples = append(samples, serviceSample{At: now, Unconfirmed: true})
	if got := evaluateServiceObjectives(p, samples, now); got.Status != "unknown" || got.Unconfirmed != 1 || got.Samples != 5 {
		t.Fatalf("missing outcomes hidden: %+v", got)
	}
}

func TestServiceObjectivesDoNotInventBufferedTTFT(t *testing.T) {
	now := time.Now().UTC()
	p := serviceProfileWork{Config: defaultServiceProfile()}
	p.Config.Stream = false
	samples := []serviceSample{}
	for range 5 {
		samples = append(samples, serviceSample{At: now, Result: upstream.CanaryResult{Success: true, Complete: true, DurationMS: 40}})
	}
	got := evaluateServiceObjectives(p, samples, now)
	if got.Status != "partial" || got.FirstContentP95 != nil {
		t.Fatalf("buffered total mislabeled as TTFT: %+v", got)
	}
}

func TestServiceObjectivesDetectAvailabilityLatencyAndStaleness(t *testing.T) {
	now := time.Now().UTC()
	p := serviceProfileWork{Config: defaultServiceProfile()}
	latency := 50
	samples := []serviceSample{}
	for range 5 {
		samples = append(samples, serviceSample{At: now, Result: upstream.CanaryResult{Success: true, Complete: true, FirstContentMS: &latency}})
	}
	samples[4].Result.Success = false
	if got := evaluateServiceObjectives(p, samples, now); got.Status != "degraded" {
		t.Fatalf("failure missed: %+v", got)
	}
	samples[4].Result.Success = true
	slow := 20000
	samples[4].Result.FirstContentMS = &slow
	if got := evaluateServiceObjectives(p, samples, now); got.Status != "degraded" {
		t.Fatalf("slow request missed: %+v", got)
	}
	if got := evaluateServiceObjectives(p, samples, now.Add(3*time.Hour)); got.Status != "unknown" {
		t.Fatalf("stale success retained: %+v", got)
	}
}
