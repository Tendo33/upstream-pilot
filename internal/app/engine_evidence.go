package app

import (
	"context"
	"errors"
	"github.com/Tendo33/upstream-pilot/internal/quality"
	"time"
)

var errEngineReplan = errors.New("候选证据已变化或过期，等待重新规划")

// Connect shared pools even when a member is currently unobservable. An
// independent pool may proceed; no member can rely on an unverified shared one.
func engineComponents(works []engineWork) map[string]string {
	parent := map[string]string{}
	poolOwner := map[string]string{}
	var root func(string) string
	root = func(id string) string {
		if parent[id] != id {
			parent[id] = root(parent[id])
		}
		return parent[id]
	}
	for _, p := range works {
		parent[p.Work.ID] = p.Work.ID
	}
	for _, p := range works {
		for _, pool := range p.Pools {
			if other, ok := poolOwner[pool]; ok {
				a, b := root(p.Work.ID), root(other)
				if a < b {
					parent[b] = a
				} else {
					parent[a] = b
				}
			} else {
				poolOwner[pool] = p.Work.ID
			}
		}
	}
	for id := range parent {
		parent[id] = root(id)
	}
	return parent
}

type engineFacts struct {
	Status                                   string
	Tier                                     int
	Eligible, Hard, PriceKnown, LatencyKnown bool
	Price                                    float64
	Latency                                  int
	LatencySource                            string
}

func decisionFacts(p engineWork, d quality.Decision, now time.Time) engineFacts {
	snap := p.Snapshot.At(p.Policy, now)
	v := engineFacts{Status: d.State.Status, Tier: d.State.Tier, Eligible: d.Eligible, Hard: d.HardFailure, PriceKnown: snap.RateFresh, LatencySource: d.LatencySource}
	if snap.RateFresh && snap.Rate != nil {
		v.Price = *snap.Rate
	}
	if d.SortingLatency != nil {
		v.LatencyKnown = true
		v.Latency = *d.SortingLatency
	}
	return v
}
func validateEngineEvidence(works []engineWork, components map[string]string, id string, now time.Time) error {
	for _, p := range works {
		if components[p.Work.ID] != components[id] || p.PreflightError != nil {
			continue
		}
		latest := quality.Evaluate(p.Policy, p.Old, p.Snapshot, now)
		if plannedEngineFacts(p) != decisionFacts(p, latest, now) {
			return errEngineReplan
		}
	}
	return nil
}

func (a *App) requireCurrentEvidence(ctx context.Context, p *engineWork) error {
	snap, err := a.qualitySnapshot(ctx, p.Work, p.Policy)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	live := *p
	live.Snapshot = snap
	decision := quality.Evaluate(p.Policy, p.Old, snap, now)
	if plannedEngineFacts(*p) != decisionFacts(live, decision, now) {
		return errEngineReplan
	}
	p.Snapshot = snap
	return nil
}

func plannedEngineFacts(p engineWork) engineFacts {
	if p.PlannedFacts != nil {
		return *p.PlannedFacts
	}
	now := time.Now().UTC()
	if p.Decision.State.EvaluatedAt != nil {
		now = *p.Decision.State.EvaluatedAt
	}
	return decisionFacts(p, p.Decision, now)
}
