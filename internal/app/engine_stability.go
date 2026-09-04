package app

import (
	"crypto/sha256"
	"encoding/binary"
	"github.com/Tendo33/upstream-pilot/internal/quality"
	"time"
)

func groupPolicy(policies map[string]quality.GroupPolicy, pool string) quality.GroupPolicy {
	if p, ok := policies[pool]; ok {
		return p
	}
	return quality.DefaultGroupPolicy()
}
func configureStableControl(p *engineWork, policies map[string]quality.GroupPolicy, now time.Time) {
	p.EffectWindow = 0
	for _, pool := range p.Pools {
		policy := groupPolicy(policies, pool)
		p.EffectWindow = max(p.EffectWindow, policy.EffectWindowSeconds)
		sum := sha256.Sum256([]byte(pool + "/" + p.Work.ID))
		bucket := int(binary.BigEndian.Uint32(sum[:4]) % 100)
		if bucket >= policy.RolloutPercent {
			p.ControlsSuppressed = true
			p.SuppressionReason = "不在此分组的自动调整灰度范围"
		}
		if !p.Decision.HardFailure && !p.Old.OwnedPause && p.Old.Status == "healthy" && p.Decision.State.Status == "healthy" && p.Decision.State.Tier == p.Old.Tier && p.Old.LastControlAppliedAt != nil && now.Sub(*p.Old.LastControlAppliedAt) < time.Duration(policy.HoldSeconds)*time.Second {
			p.ControlsSuppressed = true
			p.SuppressionReason = "上次动作仍在最短保持期"
		}
	}
	if p.EffectWindow == 0 {
		p.EffectWindow = quality.DefaultGroupPolicy().EffectWindowSeconds
	}
}

func stableChangeLimitReached(p engineWork, policies map[string]quality.GroupPolicy, changes map[string]int) bool {
	for _, pool := range p.Pools {
		limit := groupPolicy(policies, pool).MaxChanges
		if limit > 0 && changes[pool] >= limit {
			return true
		}
	}
	return false
}

// Avoid changing numbers when they would preserve every existing pool ordering.
// A tier transition still restores/degrades baseline controls as before.
func preserveUnchangedOrdering(works []engineWork, candidates []quality.Candidate, plan map[string]quality.Assignment, components map[string]string) {
	changed := map[string]bool{}
	byPool := map[string][]quality.Candidate{}
	for _, p := range works {
		if p.Old.Tier != p.Decision.State.Tier || p.Old.Status != p.Decision.State.Status || (p.Decision.State.Tier > 0 && p.Work.Priority < p.Decision.State.Desired) || (p.Old.OwnedPause && p.Decision.State.Status == "healthy") {
			changed[components[p.Work.ID]] = true
		}
	}
	for _, c := range candidates {
		if c.Available {
			for _, pool := range c.Pools {
				byPool[pool] = append(byPool[pool], c)
			}
		}
	}
	sign := func(v int) int {
		if v < 0 {
			return -1
		}
		if v > 0 {
			return 1
		}
		return 0
	}
	for _, members := range byPool {
		for i, a := range members {
			for _, b := range members[i+1:] {
				if sign(a.Current-b.Current) != sign(plan[a.ID].Priority-plan[b.ID].Priority) {
					changed[components[a.ID]] = true
				}
			}
		}
	}
	for _, c := range candidates {
		v := plan[c.ID]
		if !changed[components[c.ID]] && v.Error == "" {
			v.Priority = c.Current
			plan[c.ID] = v
		}
	}
}
