package app

import (
	"math"
	"testing"
	"time"
)

func TestCacheRateFromSamplesUsesCumulativeForSinglePoint(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	samples := []cacheSample{{
		At: now.Add(-time.Minute), Input: 10, Creation: 5, Read: 85,
	}}
	rate, denom, ok := cacheRateFromSamples(samples, now, time.Hour)
	if !ok || denom != 100 || math.Abs(rate-0.85) > 1e-9 {
		t.Fatalf("rate=%v denom=%d ok=%v", rate, denom, ok)
	}
}

func TestCacheRateFromSamplesUsesWindowDelta(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	samples := []cacheSample{
		{At: now.Add(-90 * time.Minute), Input: 100, Creation: 20, Read: 80},
		{At: now.Add(-30 * time.Minute), Input: 110, Creation: 20, Read: 170},
		{At: now, Input: 130, Creation: 25, Read: 245},
	}
	rate, denom, ok := cacheRateFromSamples(samples, now, time.Hour)
	if !ok {
		t.Fatal("expected windowed cache rate")
	}
	// The last sample at or before the 1h cutoff is -90m.
	if denom != 200 || math.Abs(rate-0.825) > 1e-9 {
		t.Fatalf("rate=%v denom=%d", rate, denom)
	}
}

func TestCacheRateFromSamplesHandlesMidnightReset(t *testing.T) {
	now := time.Date(2026, 8, 24, 0, 20, 0, 0, time.UTC)
	samples := []cacheSample{
		{At: now.Add(-40 * time.Minute), Input: 500, Creation: 10, Read: 400},
		{At: now, Input: 8, Creation: 1, Read: 11},
	}
	rate, denom, ok := cacheRateFromSamples(samples, now, time.Hour)
	if !ok || denom != 20 || math.Abs(rate-0.55) > 1e-9 {
		t.Fatalf("rate=%v denom=%d ok=%v", rate, denom, ok)
	}
}

func TestCacheRateFromSamplesZeroTrafficIsUnknown(t *testing.T) {
	now := time.Now().UTC()
	samples := []cacheSample{
		{At: now.Add(-time.Hour), Input: 10, Creation: 0, Read: 10},
		{At: now, Input: 10, Creation: 0, Read: 10},
	}
	if _, _, ok := cacheRateFromSamples(samples, now, time.Hour); ok {
		t.Fatal("zero-delta window must be unknown")
	}
}

func TestAssignWeightedPrioritiesPrefersHigherCacheRate(t *testing.T) {
	accounts := []reconcileAccount{
		{RemoteID: 1, Priority: 9, Rate: float(1), PriorityEnabled: true, CacheRate: float(0.1)},
		{RemoteID: 2, Priority: 9, Rate: float(1), PriorityEnabled: true, CacheRate: float(0.9)},
		{RemoteID: 3, Priority: 4, Rate: float(0.5), PriorityEnabled: false},
	}
	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: 1, Step: 10, CacheEnabled: true, RateWeight: 0, CacheWeight: 1})
	if accounts[1].Desired != 1 || accounts[0].Desired != 11 {
		t.Fatalf("higher cache rate should rank first: %#v", accounts)
	}
	if accounts[2].Desired != 4 {
		t.Fatalf("disabled account changed: %#v", accounts[2])
	}
}

func TestAssignWeightedPrioritiesKeepsLowRatePreference(t *testing.T) {
	accounts := []reconcileAccount{
		{RemoteID: 1, Priority: 9, Rate: float(2), PriorityEnabled: true, CacheRate: float(1)},
		{RemoteID: 2, Priority: 9, Rate: float(1), PriorityEnabled: true, CacheRate: float(0)},
	}
	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: 1, Step: 1, CacheEnabled: true, RateWeight: 1, CacheWeight: 0})
	if accounts[1].Desired != 1 || accounts[0].Desired != 2 {
		t.Fatalf("rate-only weighting should match low-rate ranking: %#v", accounts)
	}
}

func TestAssignWeightedPrioritiesCanLetCacheOutrankSlightlyHigherRate(t *testing.T) {
	accounts := []reconcileAccount{
		{RemoteID: 1, Priority: 9, Rate: float(1.0), PriorityEnabled: true, CacheRate: float(0.1)},
		{RemoteID: 2, Priority: 9, Rate: float(1.1), PriorityEnabled: true, CacheRate: float(0.95)},
	}
	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: 1, Step: 1, CacheEnabled: true, RateWeight: 1, CacheWeight: 1})
	if accounts[1].Desired != 1 || accounts[0].Desired != 2 {
		t.Fatalf("strong cache should outrank a slightly higher rate: %#v", accounts)
	}
}

func TestAssignWeightedPrioritiesUsesMeanCacheForMissingSamples(t *testing.T) {
	accounts := []reconcileAccount{
		{RemoteID: 1, Priority: 9, Rate: float(1), PriorityEnabled: true, CacheRate: float(1)},
		{RemoteID: 2, Priority: 9, Rate: float(1), PriorityEnabled: true, CacheRate: float(0)},
		{RemoteID: 3, Priority: 9, Rate: float(1), PriorityEnabled: true},
	}
	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: 1, Step: 1, CacheEnabled: true, RateWeight: 0, CacheWeight: 1})
	if accounts[0].Desired != 1 || accounts[2].Desired != 2 || accounts[1].Desired != 3 {
		t.Fatalf("missing cache rate should use the mean of known samples: %#v", accounts)
	}
}
