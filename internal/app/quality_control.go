package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub2api-upstream-manager/internal/quality"
	"sub2api-upstream-manager/internal/upstream"
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
	var s quality.State
	var pending *int
	err = a.db.QueryRow(ctx, `SELECT baseline_priority,last_applied_priority,pending_priority,desired_priority,tier,recovery_streak,last_sample_at,last_changed_at,status,reason,conflict,owned_pause FROM quality_states WHERE account_id=$1`, w.ID).Scan(&s.Baseline, &s.LastApplied, &pending, &s.Desired, &s.Tier, &s.RecoveryStreak, &s.LastSampleAt, &s.LastChangedAt, &s.Status, &s.Reason, &s.Conflict, &s.OwnedPause)
	return s, pending, err
}

func (a *App) qualitySnapshot(ctx context.Context, w AccountWork, p quality.Policy) (quality.Snapshot, error) {
	snap := quality.Snapshot{}
	model := ""
	if w.ProbeModel != nil {
		model = *w.ProbeModel
	}
	rows, err := a.db.Query(ctx, `SELECT created_at,success,first_content_ms,COALESCE(duration_ms,latency_ms,0),COALESCE(failure_reason,''),COALESCE(model,'') FROM probe_attempts WHERE account_id=$1 AND NOT control_error AND COALESCE(model,'')=$2 AND created_at>now()-$3*interval '1 second' ORDER BY created_at DESC LIMIT 1000`, w.ID, model, p.FreshSeconds)
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
	var status string
	err = a.db.QueryRow(ctx, `SELECT remaining,status,checked_at FROM account_balance_snapshots WHERE account_id=$1`, w.ID).Scan(&snap.Balance, &status, &at)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	snap.BalanceFresh = err == nil && status == "ok" && time.Since(at) <= time.Duration(p.FreshSeconds)*time.Second
	// Repeated successful collection refreshes last_rate_sync_at; change history
	// contains distinct prices, so a price rise does not disappear next poll.
	err = a.db.QueryRow(ctx, `SELECT effective_rate,checked_at FROM upstream_price_history WHERE account_id=$1 ORDER BY checked_at DESC LIMIT 1`, w.ID).Scan(&snap.Rate, &at)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	var lastPriceRead *time.Time
	if e := a.db.QueryRow(ctx, `SELECT last_rate_sync_at FROM upstream_accounts WHERE id=$1`, w.ID).Scan(&lastPriceRead); e != nil {
		return snap, e
	}
	snap.RateFresh = err == nil && lastPriceRead != nil && time.Since(*lastPriceRead) <= time.Duration(p.FreshSeconds)*time.Second
	err = a.db.QueryRow(ctx, `SELECT effective_rate FROM upstream_price_history WHERE account_id=$1 ORDER BY checked_at DESC OFFSET 1 LIMIT 1`, w.ID).Scan(&snap.PreviousRate)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	var raw []byte
	err = a.db.QueryRow(ctx, `SELECT snapshot,checked_at FROM quality_traffic WHERE account_id=$1`, w.ID).Scan(&raw, &at)
	if err == nil {
		var traffic upstream.TrafficSummary
		if json.Unmarshal(raw, &traffic) == nil && traffic.Status == "ok" {
			snap.TrafficTotal = traffic.Total
			snap.TrafficFailed = traffic.Failed
			snap.TrafficP95 = traffic.FirstContentP95
			snap.TrafficFresh = time.Since(at) <= time.Duration(p.FreshSeconds)*time.Second && traffic.Model == model
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snap, err
	}
	return snap, nil
}

// evaluateQuality is the sole automated writer of priority and scheduling.
// A per-account advisory lock also serializes policy edits and manual release.
func (a *App) evaluateQuality(ctx context.Context, w AccountWork, actor string) (quality.Decision, error) {
	var d quality.Decision
	err := a.withAccountSchedulingLock(ctx, w.ID, func(_ *pgxpool.Conn) error {
		var err error
		p, err := a.loadQualityPolicy(ctx, w.ID)
		if err != nil {
			return err
		}
		old, pending, err := a.loadQualityState(ctx, w)
		if err != nil {
			return err
		}
		snap, err := a.qualitySnapshot(ctx, w, p)
		if err != nil {
			return err
		}
		d = quality.Evaluate(p, old, snap, time.Now().UTC())
		s := d.State
		before := w.Priority
		applied := false
		var applyErr error
		if p.Mode == "priority" && !s.Conflict && s.Status != "unknown" {
			client, err := a.clientForWork(w)
			if err != nil {
				return err
			}
			remote, err := client.GetAccount(ctx, w.RemoteID)
			if err != nil {
				applyErr = err
			} else {
				before = remote.Priority
				expected := s.Baseline
				if s.LastApplied != nil {
					expected = *s.LastApplied
				}
				// Resolve an interrupted write from observed remote state before retrying.
				if pending != nil && remote.Priority == *pending {
					s.LastApplied = pending
					expected = *pending
					_, err = a.db.Exec(ctx, `UPDATE quality_states SET last_applied_priority=$2,pending_priority=NULL WHERE account_id=$1`, w.ID, *pending)
					if err != nil {
						return err
					}
				}
				if remote.Priority != expected {
					s.Conflict = true
					s.Status = "conflict"
					s.Reason = "上游优先级已被人工或其他工具修改，停止自动覆盖"
				} else if remote.Priority != s.Desired {
					_, err = a.db.Exec(ctx, `UPDATE quality_states SET pending_priority=$2 WHERE account_id=$1`, w.ID, s.Desired)
					if err != nil {
						return err
					}
					updated, err := client.UpdateAccount(ctx, w.RemoteID, upstream.AccountUpdate{Priority: &s.Desired})
					if err != nil {
						applyErr = err
					} else if updated.Priority != s.Desired {
						applyErr = errors.New("上游未保存目标优先级")
					} else {
						value := s.Desired
						s.LastApplied = &value
						applied = true
						_, err = a.db.Exec(ctx, `UPDATE quality_states SET last_applied_priority=$2,pending_priority=NULL WHERE account_id=$1`, w.ID, value)
						if err != nil {
							return err
						}
						_, err = a.db.Exec(ctx, `UPDATE upstream_accounts SET priority=$2,updated_at=now() WHERE id=$1`, w.ID, value)
						if err != nil {
							return err
						}
					}
				}
				if applyErr == nil && p.AutoPause && !s.Conflict {
					applyErr = a.applyQualityScheduling(ctx, w, client, remote, d, &s)
				}
			}
		}
		if applyErr != nil {
			s.Reason += "；写回失败：" + truncateError(applyErr)
		}
		_, err = a.db.Exec(ctx, `UPDATE quality_states SET desired_priority=$2,tier=$3,recovery_streak=$4,last_sample_at=$5,last_changed_at=$6,status=$7,reason=$8,conflict=$9,owned_pause=$10,evaluated_at=now() WHERE account_id=$1`, w.ID, s.Desired, s.Tier, s.RecoveryStreak, s.LastSampleAt, s.LastChangedAt, s.Status, s.Reason, s.Conflict, s.OwnedPause)
		if err != nil {
			return err
		}
		changed := old.Status != s.Status || old.Desired != s.Desired || qualityEventKey(old) != qualityEventKey(s) || applied || applyErr != nil
		if changed {
			errText := ""
			if applyErr != nil {
				errText = truncateError(applyErr)
			}
			_, err = a.db.Exec(ctx, `INSERT INTO quality_decisions(id,account_id,owner_id,mode,status,reason,before_priority,desired_priority,applied,error) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, uuid.NewString(), w.ID, w.OwnerID, p.Mode, s.Status, s.Reason, before, s.Desired, applied, errText)
			if err != nil {
				return err
			}
			_ = a.audit(ctx, w.OwnerID, actor, w.SiteID, w.ID, "quality.evaluate", "success", map[string]any{"mode": p.Mode, "reason": s.Reason, "before": before, "desired": s.Desired, "applied": applied, "error": errText})
			if (qualityEventKey(old) != qualityEventKey(s)) && s.Status != "watching" && !(old.Status == "unknown" && s.Status == "healthy") {
				_, err = a.db.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), w.OwnerID, w.ID, s.Status, fmt.Sprintf("%s：%s（优先级 %d → %d，模式 %s）", w.Name, s.Reason, before, s.Desired, p.Mode))
				if err != nil {
					return err
				}
			}
		}
		d.State = s
		return applyErr
	})
	return d, err
}

// Group rows serialize automated pauses across different member accounts.
// This prevents two controllers from simultaneously removing each other's backup.
func (a *App) applyQualityScheduling(ctx context.Context, w AccountWork, client *upstream.Sub2Client, remote upstream.Sub2Account, d quality.Decision, s *quality.State) error {
	if statusText(remote.Status) != "active" {
		return nil
	}
	if !d.HardFailure && !(s.OwnedPause && s.Tier == 0 && s.Status == "healthy") {
		return nil
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM upstream_groups WHERE site_id=$1 ORDER BY id FOR UPDATE`, w.SiteID); err != nil {
		return err
	}
	if d.HardFailure && remote.Schedulable {
		var safe bool
		err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM account_group_memberships m WHERE m.account_id=$1 AND NOT EXISTS(SELECT 1 FROM account_group_memberships other JOIN upstream_accounts a ON a.id=other.account_id JOIN quality_states q ON q.account_id=a.id WHERE other.group_id=m.group_id AND a.id<>$1 AND a.deleted_at IS NULL AND a.remote_status='active' AND COALESCE(a.probe_model,'')=COALESCE((SELECT probe_model FROM upstream_accounts WHERE id=$1),'') AND a.schedulable AND q.status='healthy' AND q.last_sample_at>now()-interval '10 minutes'))`, w.ID).Scan(&safe)
		if err != nil {
			return err
		}
		if !safe {
			s.Reason += "；保留调度：分组缺少已验证健康备用"
			return tx.Commit(ctx)
		}
		updated, err := client.SetSchedulable(ctx, w.RemoteID, false)
		if err != nil {
			return err
		}
		if updated.Schedulable {
			return errors.New("上游未暂停调度")
		}
		s.OwnedPause = true
		if _, err = tx.Exec(ctx, `UPDATE upstream_accounts SET schedulable=false,managed_hold=true WHERE id=$1`, w.ID); err != nil {
			return err
		}
	} else if !d.HardFailure && s.OwnedPause && s.Tier == 0 && s.Status == "healthy" {
		if !remote.Schedulable {
			updated, err := client.SetSchedulable(ctx, w.RemoteID, true)
			if err != nil {
				return err
			}
			if !updated.Schedulable {
				return errors.New("上游未恢复调度")
			}
		}
		s.OwnedPause = false
		if _, err = tx.Exec(ctx, `UPDATE upstream_accounts SET schedulable=true,managed_hold=false WHERE id=$1`, w.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE quality_states SET owned_pause=$2 WHERE account_id=$1`, w.ID, s.OwnedPause); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) qualityReconcileSite(ctx context.Context, siteID, ownerFilter, actorID string) (ReconcileResult, error) {
	if _, err := a.siteSecret(ctx, siteID, ownerFilter); err != nil {
		return ReconcileResult{}, err
	}
	rows, err := a.db.Query(ctx, `SELECT id::text FROM upstream_accounts WHERE site_id=$1 AND deleted_at IS NULL ORDER BY id`, siteID)
	if err != nil {
		return ReconcileResult{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return ReconcileResult{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{}
	for _, id := range ids {
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		w, err := a.loadAccountWork(requestCtx, id, ownerFilter)
		if err == nil {
			var d quality.Decision
			d, err = a.evaluateQuality(requestCtx, w, actorID)
			if d.State.LastApplied != nil && *d.State.LastApplied != w.Priority {
				result.Changed++
			}
		}
		cancel()
		result.Evaluated++
		if err != nil {
			result.Failed++
			a.logger.Warn("quality evaluation failed", "account_id", id, "error", err)
		}
	}
	_, err = a.db.Exec(ctx, `UPDATE sites SET last_reconcile_at=now(),next_reconcile_at=now()+reconcile_interval_seconds*interval '1 second',reconcile_lease_until=NULL WHERE id=$1`, siteID)
	return result, err
}

// Notifications reflect stable risk categories, never changing counters.
func qualityEventKey(s quality.State) string {
	parts := []string{s.Status}
	if s.Status == "unknown" {
		return "unknown"
	}
	tests := []struct {
		key   string
		terms []string
	}{
		{"failure", []string{"连续", "错误率"}},
		{"slow", []string{"首字"}},
		{"balance", []string{"余额"}},
		{"cost", []string{"价格", "成本"}},
		{"write_error", []string{"写回失败"}},
	}
	for _, test := range tests {
		for _, term := range test.terms {
			if strings.Contains(s.Reason, term) {
				parts = append(parts, test.key)
				break
			}
		}
	}
	return strings.Join(parts, ":")
}
