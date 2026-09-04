package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type engineWork struct {
	Costs              map[string]ComparableCost
	CostDeadline       *time.Time
	Supplier           supplierMember
	FailureComponent   string
	SettledIntent      *controlIntent
	ControlsSuppressed bool
	SuppressionReason  string
	EffectWindow       int
	ActionBefore       map[string]any
	ActionAfter        map[string]any
	BeforeSLI          []actionSLI
	Native             upstream.NativeAssessment
	RemoteGroups       []int64
	Work               AccountWork
	Policy             quality.Policy
	Old                quality.State
	Decision           quality.Decision
	Snapshot           quality.Snapshot
	Pools              []string
	AppliedControl     map[string]any
	PendingPriority    *int
	PreflightError     error
	ControlsSettled    bool
	PlanID             string
	PlannedFacts       *engineFacts
}

func poolKey(group, model string) string { return group + "/" + model }

// A single site lock covers a complete plan and its writes, including manual
// controls and policy changes. Expiring queue leases cannot duplicate writers.
func (a *App) evaluateQuality(ctx context.Context, w AccountWork, actor string) (quality.Decision, error) {
	_, decisions, failures, err := a.runEngine(ctx, w.SiteID, w.OwnerID, actor)
	return decisions[w.ID], errors.Join(err, failures[w.ID])
}
func (a *App) qualityReconcileSite(ctx context.Context, site, owner, actor string) (ReconcileResult, error) {
	result, _, failures, err := a.runEngine(ctx, site, owner, actor)
	for _, failure := range failures {
		err = errors.Join(err, failure)
	}
	return result, err
}
func (a *App) runEngine(ctx context.Context, site, owner, actor string) (ReconcileResult, map[string]quality.Decision, map[string]error, error) {
	ctx, cancelCycle := context.WithTimeout(ctx, 90*time.Second)
	defer cancelCycle()
	result := ReconcileResult{}
	decisions := map[string]quality.Decision{}
	accountErrors := map[string]error{}
	if _, err := a.siteSecret(ctx, site, owner); err != nil {
		return result, decisions, accountErrors, err
	}
	err := a.withSiteSchedulingLock(ctx, site, func(lock *pgxpool.Conn) error {
		works, policies, err := a.engineSnapshot(ctx, site)
		if err != nil {
			return err
		}
		a.preflightEngine(ctx, works)
		components := engineComponents(works)
		blocked := map[string]error{}
		planID := uuid.NewString()
		now := time.Now().UTC()
		for i := range works {
			p := &works[i]
			p.PlanID = planID
			p.Snapshot = p.Snapshot.At(p.Policy, now)
			if p.PreflightError != nil {
				blocked[components[p.Work.ID]] = p.PreflightError
			} else if p.Decision.State.Conflict {
				blocked[components[p.Work.ID]] = errors.New("共享上游存在人工修改冲突，等待释放或重新配置")
			} else {
				p.Decision = quality.Evaluate(p.Policy, p.Old, p.Snapshot, now)
			}
		}
		for i := range works {
			p := &works[i]
			facts := decisionFacts(*p, p.Decision, now)
			p.PlannedFacts = &facts
		}
		for i := range works {
			configureStableControl(&works[i], policies, now)
		}
		candidates := make([]quality.Candidate, 0, len(works))
		for _, p := range works {
			s := p.Decision.State
			price := p.Snapshot.Rate
			priceBasis := ""
			if p.Supplier.CurrentSource && p.Supplier.Config.Confirmed && p.Supplier.Config.MultiplierBasis != "" {
				priceBasis = "multiplier:" + p.Supplier.Config.MultiplierBasis + "/" + modelForWork(p.Work)
			}
			poolPrices := map[string]*float64{}
			poolBases := map[string]string{}
			for _, pool := range p.Pools {
				c := p.Costs[pool]
				if c.Status == "comparable" {
					poolPrices[pool] = c.USDPerMillion
					poolBases[pool] = c.Basis
				} else if p.Snapshot.RateFresh && priceBasis != "" {
					poolPrices[pool] = price
					poolBases[pool] = priceBasis
				}
			}
			if !p.Snapshot.RateFresh {
				price = nil
			}
			candidates = append(candidates, quality.Candidate{PriceBasis: priceBasis, PoolPrices: poolPrices, PoolPriceBases: poolBases, RiskWorsened: s.Tier > p.Old.Tier, ID: p.Work.ID, Pools: p.Pools, Baseline: s.Baseline, Current: p.Work.Priority, Desired: s.Desired, Tier: s.Tier, Healthy: p.Decision.Eligible, Mutable: p.Policy.Mode == "priority" && !p.ControlsSuppressed && !s.Conflict && s.Status != "unknown", Available: p.PreflightError == nil && p.Work.RemoteStatus == "active" && (p.Native.State == "eligible" || s.OwnedPause), Price: price, Latency: p.Decision.SortingLatency})
		}
		plan := quality.Plan(candidates, policies)
		preserveUnchangedOrdering(works, candidates, plan, components)
		previewCandidates := append([]quality.Candidate(nil), candidates...)
		for i := range previewCandidates {
			previewCandidates[i].Mutable = !works[i].ControlsSuppressed && !works[i].Old.Conflict && works[i].Decision.State.Status != "unknown"
		}
		preview := quality.Plan(previewCandidates, policies)
		preserveUnchangedOrdering(works, previewCandidates, preview, components)
		for i := range works {
			p := &works[i]
			assignment := plan[p.Work.ID]
			if p.Policy.Mode == "observe" {
				assignment = preview[p.Work.ID]
			}
			if block := blocked[components[p.Work.ID]]; block != nil {
				assignment.Priority = p.Work.Priority
				assignment.Error = "关联候选未确认：" + truncateError(block)
			}
			if blocked[components[p.Work.ID]] != nil {
				plan[p.Work.ID] = assignment
				preview[p.Work.ID] = assignment
			}
			p.Decision.State.Desired = assignment.Priority
			p.Decision.State.PlanError = assignment.Error
			p.Decision.State.PlanStrategy = "group/model"
			if assignment.Error != "" {
				p.Decision.State.Reason += "；" + assignment.Error
			}
		}
		costView := map[string]map[string]ComparableCost{}
		for _, p := range works {
			costView[p.Work.ID] = p.Costs
		}
		raw, err := json.Marshal(map[string]any{"assignments": plan, "preview": preview, "costs": costView})
		if err != nil {
			return err
		}
		if _, err = a.db.Exec(ctx, `INSERT INTO engine_plans(site_id,generation,plan) VALUES($1,$2,$3) ON CONFLICT(site_id) DO UPDATE SET generation=excluded.generation,plan=excluded.plan,evaluated_at=now()`, site, planID, raw); err != nil {
			return err
		}
		// Promote healthy candidates before demoting unhealthy ones.
		sort.SliceStable(works, func(i, j int) bool {
			if works[i].Decision.State.Tier != works[j].Decision.State.Tier {
				return works[i].Decision.State.Tier < works[j].Decision.State.Tier
			}
			return works[i].Decision.State.Desired < works[j].Decision.State.Desired
		})

		changes := map[string]int{}
		for i := range works {
			p := &works[i]
			if stableChangeLimitReached(*p, policies, changes) {
				p.ControlsSuppressed = true
				p.SuppressionReason = "本轮调整数量已达到上限"
			}
			if p.ControlsSuppressed {
				p.Decision.State.Reason += "；" + p.SuppressionReason
			}
			result.Evaluated++
			requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			var applied bool
			before := p.Work.Priority
			// Check the lock connection immediately before any remote action.
			if err = lock.Ping(requestCtx); err != nil {
				cancel()
				return err
			}
			err = blocked[components[p.Work.ID]]
			if p.Decision.State.Conflict && p.PreflightError == nil {
				err = nil
			}
			if err == nil && !p.Decision.State.Conflict {
				err = validateEngineEvidence(works, components, p.Work.ID, time.Now().UTC())
			}
			if err == nil {
				before, applied, err = a.applyEngineControls(requestCtx, p, works, policies)
			}
			if err != nil {
				p.Decision.State.Reason += "；写回失败：" + truncateError(err)
			}
			saveErr := a.persistEngineDecision(requestCtx, p.Work, *p, before, applied, err)
			cancel()
			err = errors.Join(err, saveErr)
			if err != nil {
				blocked[components[p.Work.ID]] = fmt.Errorf("关联写回未确认，等待重新规划：%w", err)
			}
			if p.Decision.State.Conflict && !p.Old.Conflict {
				blocked[components[p.Work.ID]] = errors.New("共享上游发现人工修改冲突，等待重新规划")
			}
			decisions[p.Work.ID] = p.Decision
			if applied && saveErr == nil {
				result.Changed++
				for _, pool := range p.Pools {
					changes[pool]++
				}
			}
			if err != nil {
				result.Failed++
				accountErrors[p.Work.ID] = fmt.Errorf("%s: %w", p.Work.Name, err)
				if ctx.Err() != nil {
					break
				}
			}
		}
		if err = a.evaluateActionEffects(ctx, site); err != nil {
			return err
		}
		_, err = a.db.Exec(ctx, `UPDATE sites SET last_reconcile_at=now(),next_reconcile_at=now()+reconcile_interval_seconds*interval '1 second',reconcile_lease_until=NULL WHERE id=$1`, site)
		return err
	})
	return result, decisions, accountErrors, err
}

func (a *App) engineSnapshot(ctx context.Context, site string) ([]engineWork, map[string]quality.GroupPolicy, error) {
	works := []engineWork{}
	policies := map[string]quality.GroupPolicy{}
	rows, err := a.db.Query(ctx, `SELECT a.id::text FROM upstream_accounts a WHERE site_id=$1 AND deleted_at IS NULL ORDER BY id`, site)
	if err != nil {
		return nil, nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	memberships := map[string][]string{}
	remoteGroups := map[string][]int64{}
	rows, err = a.db.Query(ctx, `SELECT m.account_id::text,m.group_id::text,g.remote_id,COALESCE(a.probe_model,''),COALESCE(p.config,'{}'::jsonb) FROM account_group_memberships m JOIN upstream_accounts a ON a.id=m.account_id JOIN upstream_groups g ON g.id=m.group_id LEFT JOIN engine_group_policies p ON p.group_id=g.id AND p.model=COALESCE(a.probe_model,'') WHERE a.site_id=$1 AND a.deleted_at IS NULL AND g.deleted_at IS NULL`, site)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, group, model string
		var remoteGroup int64
		var raw []byte
		if err = rows.Scan(&id, &group, &remoteGroup, &model, &raw); err != nil {
			break
		}
		key := poolKey(group, model)
		memberships[id] = append(memberships[id], key)
		remoteGroups[id] = append(remoteGroups[id], remoteGroup)
		p := quality.DefaultGroupPolicy()
		if err = json.Unmarshal(raw, &p); err != nil {
			break
		}
		policies[key] = p
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	directory, err := a.supplierMembers(ctx, "", site)
	if err != nil {
		return nil, nil, err
	}
	supplierByID := map[string]supplierMember{}
	for _, m := range directory {
		supplierByID[m.ID] = m
	}
	domains := supplierComponents(directory)
	for _, id := range ids {
		p := engineWork{Supplier: supplierByID[id], FailureComponent: domains[id]}
		p.Work, err = a.loadAccountWork(ctx, id, "")
		if err != nil {
			return nil, nil, err
		}
		p.Policy, err = a.loadQualityPolicy(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		p.Old, p.PendingPriority, err = a.loadQualityState(ctx, p.Work)
		if err != nil {
			return nil, nil, err
		}
		p.Snapshot, err = a.qualitySnapshot(ctx, p.Work, p.Policy)
		if err != nil {
			return nil, nil, err
		}
		p.Decision = quality.Evaluate(p.Policy, p.Old, p.Snapshot, time.Now().UTC())
		facts := decisionFacts(p, p.Decision, *p.Decision.State.EvaluatedAt)
		p.PlannedFacts = &facts
		p.Pools = memberships[id]
		p.RemoteGroups = remoteGroups[id]
		p.Native = assessWorkNative(p.Work, p.RemoteGroups, time.Now().UTC())
		works = append(works, p)
	}
	costs, e := a.poolEconomics(ctx, site, works)
	if e != nil {
		return nil, nil, e
	}
	for i := range works {
		works[i].Costs = costs[works[i].Work.ID]
		for _, c := range works[i].Costs {
			if c.ValidUntil != nil && (works[i].CostDeadline == nil || c.ValidUntil.Before(*works[i].CostDeadline)) {
				works[i].CostDeadline = c.ValidUntil
			}
		}
	}
	return works, policies, nil
}

func safeToPause(target engineWork, works []engineWork, policies map[string]quality.GroupPolicy) bool {
	if len(target.Pools) == 0 || !target.Supplier.independentKnown() {
		return false
	}
	for _, pool := range target.Pools {
		domains := map[string]bool{}
		spareByDomain := map[string]int{}
		capacityKnown := true
		for _, p := range works {
			if !p.Supplier.independentKnown() || p.FailureComponent == target.FailureComponent || p.Work.ID == target.Work.ID || p.Native.State != "eligible" || !p.Work.Schedulable || p.Work.RemoteStatus != "active" || !p.Decision.Eligible || p.Decision.State.Conflict || p.Decision.State.PlanError != "" {
				continue
			}
			for _, other := range p.Pools {
				if other == pool {
					domains[p.FailureComponent] = true
					n := p.Work.NativeConstraints
					if n.CapacityVerified && n.Concurrency != nil && n.CurrentConcurrency != nil && n.QueueDepth != nil && *n.QueueDepth == 0 {
						spareByDomain[p.FailureComponent] = max(spareByDomain[p.FailureComponent], max(0, *n.Concurrency-*n.CurrentConcurrency))
					} else {
						capacityKnown = false
					}
					break
				}
			}
		}
		spare := 0
		for _, v := range spareByDomain {
			spare += v
		}
		minimum := policies[pool].MinimumHealthy
		if minimum == 0 {
			minimum = 1
		}
		if len(domains) < minimum || (policies[pool].MinimumSpareSlots > 0 && (!capacityKnown || spare < policies[pool].MinimumSpareSlots)) {
			return false
		}
	}
	return true
}
func qualityEventKey(s quality.State) string { return quality.EventKey(s) }

// Read-only preflight is bounded in parallel; all mutations remain serial.
func (a *App) preflightEngine(ctx context.Context, works []engineWork) {
	if len(works) == 0 {
		return
	}
	runtime := map[int64]upstream.Sub2Account{}
	batched := false
	client, clientErr := a.clientForWork(works[0].Work)
	if clientErr == nil {
		readCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		accounts, e := client.ListAccountRuntime(readCtx)
		stop()
		if e == nil {
			batched = true
			for _, v := range accounts {
				runtime[v.ID] = v
			}
		} else {
			var httpErr *upstream.HTTPError
			if !errors.As(e, &httpErr) || (httpErr.Status != 404 && httpErr.Status != 405) {
				clientErr = e
			}
		}
	}
	if clientErr != nil {
		for i := range works {
			works[i].PreflightError = clientErr
			works[i].Decision.Eligible = false
		}
		return
	}

	var wait sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i := range works {
		wait.Add(1)
		go func(p *engineWork) {
			defer wait.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				p.PreflightError = ctx.Err()
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			client, err := a.clientForWork(p.Work)
			if err == nil {
				var remote upstream.Sub2Account
				var e error
				if batched {
					var ok bool
					remote, ok = runtime[p.Work.RemoteID]
					if !ok {
						e = errEngineReplan
					}
				} else {
					remote, e = client.GetAccount(probeCtx, p.Work.RemoteID)
				}
				err = e
				if e == nil {
					p.Work.Priority = remote.Priority
					p.Work.Schedulable = remote.Schedulable
					p.Work.RemoteStatus = statusText(remote.Status)
					if (remote.Platform != "" && remote.Platform != p.Work.Platform) || (remote.Type != "" && remote.Type != p.Work.AccountType) {
						p.PreflightError = errEngineReplan
						p.Decision.Eligible = false
						return
					}
					if e := a.recordNativeObservation(probeCtx, p.Work, remote); e != nil {
						p.PreflightError = e
						p.Decision.Eligible = false
						return
					}
					p.Work.NativeConstraints = remote.Native
					checked := time.Now().UTC()
					p.Work.NativeCheckedAt = &checked
					p.Native = assessWorkNative(p.Work, p.RemoteGroups, checked)
					if p.Policy.Mode == "priority" {
						expected := p.Old.Baseline
						if p.Old.LastApplied != nil {
							expected = *p.Old.LastApplied
						}
						if remote.Priority != expected && (p.PendingPriority == nil || remote.Priority != *p.PendingPriority) {
							p.Decision.State.Conflict = true
							p.Decision.State.Status = "conflict"
							p.Decision.State.Reason = "上游优先级被人工修改，停止自动覆盖"
							p.Decision.Eligible = false
						}
					}
				}
			}
			if err != nil {
				p.PreflightError = err
				p.Decision.Eligible = false
				p.Decision.State.Status = "unknown"
				p.Decision.State.Reason = "管理读回失败，等待后续评估"
				p.Decision.State.Desired = p.Work.Priority
			}
		}(&works[i])
	}
	wait.Wait()
}

func assessWorkNative(w AccountWork, groups []int64, now time.Time) upstream.NativeAssessment {
	if w.NativeCheckedAt == nil || now.Sub(*w.NativeCheckedAt) > 5*time.Minute {
		return upstream.NativeAssessment{State: "unknown", Reason: "原生资格快照未采集或已过期"}
	}
	model := ""
	if w.ProbeModel != nil {
		model = *w.ProbeModel
	}
	return (upstream.Sub2Account{Native: w.NativeConstraints, Platform: w.Platform, Type: w.AccountType, Status: w.RemoteStatus, Schedulable: w.Schedulable}).NativeEligibility(model, groups, now)
}

func modelForWork(w AccountWork) string {
	if w.ProbeModel != nil {
		return *w.ProbeModel
	}
	return ""
}
