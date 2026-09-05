package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/jackc/pgx/v5"
)

func (a *App) loadQualityPolicy(ctx context.Context, id string) (quality.Policy, error) {
	p := quality.DefaultPolicy()
	var raw []byte
	err := a.db.QueryRow(ctx, `SELECT config FROM quality_policies WHERE account_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}

func (a *App) loadQualityState(ctx context.Context, w AccountWork) (quality.State, *int, error) {
	_, err := a.db.Exec(ctx, `INSERT INTO quality_states(account_id,baseline_priority,desired_priority) VALUES($1,$2,$2) ON CONFLICT DO NOTHING`, w.ID, w.Priority)
	if err != nil {
		return quality.State{}, nil, err
	}
	return a.readQualityState(ctx, w)
}

func (a *App) readQualityState(ctx context.Context, w AccountWork) (quality.State, *int, error) {
	var s quality.State
	var risks []byte
	var pending *int
	var generation int64
	err := a.db.QueryRow(ctx, `SELECT baseline_priority,last_applied_priority,pending_priority,desired_priority,tier,recovery_streak,last_sample_at,last_changed_at,status,reason,conflict,owned_pause,risks,evaluated_at,plan_error,plan_strategy,source_generation,last_control_applied_at FROM quality_states WHERE account_id=$1`, w.ID).Scan(&s.Baseline, &s.LastApplied, &pending, &s.Desired, &s.Tier, &s.RecoveryStreak, &s.LastSampleAt, &s.LastChangedAt, &s.Status, &s.Reason, &s.Conflict, &s.OwnedPause, &risks, &s.EvaluatedAt, &s.PlanError, &s.PlanStrategy, &generation, &s.LastControlAppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return quality.State{Baseline: w.Priority, Desired: w.Priority, Status: "unknown", Reason: "等待评估"}, nil, nil
	}
	if err == nil {
		err = json.Unmarshal(risks, &s.Risks)
	}
	if err == nil && generation != w.SourceGeneration {
		s.Risks = nil
		s.Tier = 0
		s.RecoveryStreak = 0
		s.LastSampleAt = nil
		s.LastChangedAt = nil
		s.Status = "unknown"
		s.Reason = "来源已变化，等待当前来源的证据"
	}
	return s, pending, err
}

func (a *App) qualitySnapshot(ctx context.Context, w AccountWork, p quality.Policy) (quality.Snapshot, error) {
	snap := quality.Snapshot{}
	model := ""
	if w.ProbeModel != nil {
		model = *w.ProbeModel
	}
	rows, err := a.db.Query(ctx, `SELECT created_at,success,first_content_ms,COALESCE(duration_ms,latency_ms,0),COALESCE(failure_reason,''),COALESCE(model,'') FROM probe_attempts WHERE account_id=$1 AND source_generation=$4 AND NOT control_error AND COALESCE(model,'')=$2 AND created_at>now()-$3*interval '1 second' ORDER BY created_at DESC LIMIT 1000`, w.ID, model, p.WindowSeconds, w.SourceGeneration)
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var v quality.Sample
		if err = rows.Scan(&v.At, &v.Success, &v.FirstContentMS, &v.DurationMS, &v.FailureReason, &v.Model); err != nil {
			rows.Close()
			return snap, err
		}
		snap.Samples = append(snap.Samples, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snap, err
	}
	var at time.Time
	var status, cacheKey string
	var balanceGeneration int64
	err = a.db.QueryRow(ctx, `SELECT remaining,status,checked_at,cache_key,source_generation FROM account_balance_snapshots WHERE account_id=$1`, w.ID).Scan(&snap.Balance, &status, &at, &cacheKey, &balanceGeneration)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	if err == nil {
		works, loadErr := a.loadAccountBalanceWork(ctx, w.OwnerID, []string{w.ID})
		if loadErr != nil {
			return snap, loadErr
		}
		if len(works) != 1 || balanceGeneration != w.SourceGeneration || works[0].SourceGeneration != w.SourceGeneration || cacheKey != accountBalanceCacheKey(works[0]) {
			snap.Balance = nil
			status = "unknown"
		}
		value := at
		snap.BalanceAt = &value
	}
	snap.BalanceFresh = err == nil && status == "ok" && time.Since(at) <= time.Duration(p.FreshSeconds)*time.Second
	var priceStatus string
	err = a.db.QueryRow(ctx, `SELECT observed_cost_rate,price_reference_rate,last_rate_sync_at,price_status FROM upstream_accounts WHERE id=$1 AND source_generation=$2 AND price_source_generation=$2`, w.ID, w.SourceGeneration).Scan(&snap.Rate, &snap.ReferenceRate, &snap.RateAt, &priceStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	snap.RateFresh = priceStatus == "ok" && snap.RateAt != nil && time.Since(*snap.RateAt) <= time.Duration(p.FreshSeconds)*time.Second
	var raw []byte
	err = a.db.QueryRow(ctx, `SELECT snapshot,checked_at FROM quality_traffic WHERE account_id=$1 AND source_generation=$2`, w.ID, w.SourceGeneration).Scan(&raw, &at)
	if err == nil {
		value := at
		snap.TrafficAt = &value
		var traffic upstream.TrafficSummary
		if json.Unmarshal(raw, &traffic) == nil && (traffic.Status == "ok" || traffic.Status == "partial") {
			snap.TrafficIncomplete = traffic.Incomplete || traffic.Truncated || traffic.Status == "partial"
			snap.TrafficAt = traffic.LatestAt
			snap.TrafficTotal = traffic.Total
			snap.TrafficFailed = traffic.Failed
			snap.TrafficP95 = traffic.FirstContentP95
			snap.TrafficLatencySamples = traffic.FirstContentSamples
			snap.TrafficLatencyAt = traffic.FirstContentAt
			snap.TrafficFresh = time.Since(at) <= time.Duration(p.FreshSeconds)*time.Second && traffic.Model == model && traffic.LatestAt != nil && time.Since(*traffic.LatestAt) <= time.Duration(p.FreshSeconds)*time.Second
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	var current int64
	if err = a.db.QueryRow(ctx, `SELECT source_generation FROM upstream_accounts WHERE id=$1`, w.ID).Scan(&current); err != nil {
		return snap, err
	}
	if current != w.SourceGeneration {
		return quality.Snapshot{}, errEngineReplan
	}
	return snap, nil
}
