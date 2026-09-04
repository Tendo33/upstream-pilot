package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"math"
)

func (a *App) persistRateObservation(ctx context.Context, w AccountWork, value rateSyncOutcome) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var generation int64
	var previous *float64
	if err = tx.QueryRow(ctx, `SELECT config_generation,observed_cost_rate FROM upstream_accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, w.ID).Scan(&generation, &previous); err != nil {
		return err
	}
	if generation != w.ConfigGeneration {
		return errors.New("采价期间账号配置发生变化，放弃旧结果")
	}
	if previous == nil || math.Abs(*previous-value.EffectiveRate) > 1e-7 {
		_, err = tx.Exec(ctx, `INSERT INTO upstream_price_history(id,account_id,source_rate,effective_rate,endpoint) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), w.ID, value.SourceRate, value.EffectiveRate, value.Endpoint)
		if err != nil {
			return err
		}
		if previous != nil {
			_, err = tx.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message) VALUES($1,$2,$3,'price_change',$4)`, uuid.NewString(), w.OwnerID, w.ID, fmt.Sprintf("%s：采购倍率 %.4f → %.4f", w.Name, *previous, value.EffectiveRate))
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET observed_cost_rate=$2,source_rate_multiplier=$3,source_rate_endpoint=$4,price_reference_rate=COALESCE(price_reference_rate,$2),price_status='ok',last_rate_attempt_at=now(),last_rate_sync_at=now(),source_credential_state=CASE WHEN $5 THEN 'valid' ELSE source_credential_state END,source_credential_checked_at=CASE WHEN $5 THEN now() ELSE source_credential_checked_at END,next_rate_sync_at=now()+rate_sync_interval_seconds*interval '1 second',last_error=NULL,work_lease_until=NULL,updated_at=now() WHERE id=$1`, w.ID, value.EffectiveRate, value.SourceRate, value.Endpoint, w.SourceType == "newapi")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, w.SiteID)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_ = a.audit(ctx, w.OwnerID, "", w.SiteID, w.ID, "account.rate_collect", "success", map[string]any{"effective_rate": value.EffectiveRate})
	return nil
}

// Decisions and notifications are one local transaction. Remote RPCs are
// acknowledged by this transaction only after readback; pending intents remain
// available if the transaction rolls back.
func (a *App) persistEngineDecision(ctx context.Context, w AccountWork, p engineWork, before int, applied bool, applyErr error) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	s := p.Decision.State
	raw, err := json.Marshal(s.Risks)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE quality_states SET desired_priority=$2,tier=$3,recovery_streak=$4,last_sample_at=$5,last_changed_at=$6,status=$7,reason=$8,conflict=$9,owned_pause=$10,risks=$11,evaluated_at=now(),plan_error=$12,plan_strategy=$13 WHERE account_id=$1`, w.ID, s.Desired, s.Tier, s.RecoveryStreak, s.LastSampleAt, s.LastChangedAt, s.Status, s.Reason, s.Conflict, s.OwnedPause, raw, s.PlanError, s.PlanStrategy)
	if err != nil {
		return err
	}
	if applied || p.ControlsSettled {
		fields, _ := json.Marshal(p.AppliedControl)
		_, err = tx.Exec(ctx, `UPDATE quality_states SET last_applied_priority=$2,pending_priority=NULL,applied_control=$3,pending_control='{}' WHERE account_id=$1`, w.ID, s.LastApplied, fields)
		if err != nil {
			return err
		}
		if s.LastApplied != nil {
			_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET priority=$2,updated_at=now() WHERE id=$1`, w.ID, *s.LastApplied)
			if err != nil {
				return err
			}
		}
	}
	if p.Old.OwnedPause != s.OwnedPause {
		_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET managed_hold=$2,schedulable=NOT $2 WHERE id=$1`, w.ID, s.OwnedPause)
		if err != nil {
			return err
		}
	}
	changed := p.Old.Status != s.Status || p.Old.Desired != s.Desired || qualityEventKey(p.Old) != qualityEventKey(s) || applied || p.ControlsSettled || applyErr != nil
	if changed {
		errorText := ""
		if applyErr != nil {
			errorText = truncateError(applyErr)
		}
		detail, _ := json.Marshal(map[string]any{"risks": s.Risks, "plan_error": s.PlanError, "controls": p.AppliedControl})
		_, err = tx.Exec(ctx, `INSERT INTO quality_decisions(id,account_id,owner_id,mode,status,reason,before_priority,desired_priority,applied,error,detail) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, uuid.NewString(), w.ID, w.OwnerID, p.Policy.Mode, s.Status, s.Reason, before, s.Desired, applied, errorText, detail)
		if err != nil {
			return err
		}
		if qualityEventKey(p.Old) != qualityEventKey(s) && s.Status != "watching" && !(p.Old.Status == "unknown" && s.Status == "healthy") {
			_, err = tx.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), w.OwnerID, w.ID, s.Status, fmt.Sprintf("%s：%s（优先级 %d → %d，%s）", w.Name, s.Reason, before, s.Desired, p.Policy.Mode))
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
