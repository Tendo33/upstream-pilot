package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The balance collector only records facts and episodes. All sends now use the
// same event queue, subscriptions and delivery receipts as quality and price.
func (a *App) evaluateBalanceNotifications(ctx context.Context, now time.Time) error {
	works, err := a.loadAllAccountBalanceWork(ctx)
	if err != nil {
		return err
	}
	snapshots, err := a.loadAllAccountBalanceSnapshots(ctx)
	if err != nil {
		return err
	}
	rules := map[string]notificationRules{}
	for _, w := range works {
		rule, ok := rules[w.OwnerID]
		if !ok {
			rule, err = a.loadNotificationRules(ctx, w.OwnerID)
			if err != nil {
				return err
			}
			rules[w.OwnerID] = rule
		}
		if !rule.BalanceEnabled {
			continue
		}
		b, ok := snapshots[w.ID]
		if !ok || b.SourceGeneration != w.SourceGeneration || b.CacheKey != accountBalanceCacheKey(w) || b.Status != "ok" || b.Unit == "" || b.Remaining == nil || !isFinite(*b.Remaining) || now.Sub(b.CheckedAt) > balanceSnapshotMaxAge || b.CheckedAt.After(now.Add(time.Second)) {
			continue
		}
		if err = a.recordBalanceNotification(ctx, w, b, rule, now); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) recordBalanceNotification(ctx context.Context, w accountBalanceWork, b accountBalanceSnapshot, rule notificationRules, now time.Time) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var name string
	var live bool
	if err = tx.QueryRow(ctx, `SELECT a.source_generation,a.name,(a.deleted_at IS NULL AND s.enabled) FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE a.id=$1 AND s.owner_id=$2 FOR SHARE OF a`, w.ID, w.OwnerID).Scan(&current, &name, &live); err != nil {
		return err
	}
	if !live || current != w.SourceGeneration {
		return nil
	}
	var enabled, snoozed bool
	if err = tx.QueryRow(ctx, `SELECT balance_enabled,COALESCE(balance_snoozed_until>$2,false),balance_threshold,balance_cooldown_seconds FROM notification_rules WHERE owner_id=$1 FOR SHARE`, w.OwnerID, now).Scan(&enabled, &snoozed, &rule.BalanceThreshold, &rule.BalanceCooldownSeconds); err != nil {
		return err
	}
	if !enabled || snoozed {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO notification_balance_states(account_id,source_generation,unit) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w.ID, current, b.Unit)
	if err != nil {
		return err
	}
	var active bool
	var generation int64
	var unit string
	var last *time.Time
	if err = tx.QueryRow(ctx, `SELECT source_generation,active,unit,last_event_at FROM notification_balance_states WHERE account_id=$1 FOR UPDATE`, w.ID).Scan(&generation, &active, &unit, &last); err != nil {
		return err
	}
	same := generation == current && unit == b.Unit
	low := *b.Remaining < rule.BalanceThreshold
	kind := ""
	message := ""
	if low && (!same || !active || last == nil || now.Sub(*last) >= time.Duration(rule.BalanceCooldownSeconds)*time.Second) {
		kind = "balance_low"
		message = fmt.Sprintf("%s：余额 %s %s，低于阈值 %s；采集于 %s", name, formatBalanceAlertNumber(*b.Remaining), b.Unit, formatBalanceAlertNumber(rule.BalanceThreshold), b.CheckedAt.UTC().Format(time.RFC3339))
	} else if same && active && !low {
		kind = "balance_recovered"
		message = fmt.Sprintf("%s：余额恢复至 %s %s", name, formatBalanceAlertNumber(*b.Remaining), b.Unit)
	} else if !same && active {
		kind = "balance_source_changed"
		message = name + "：余额来源或单位变化，旧预警关闭，当前数值重新评估"
	}
	if kind != "" {
		detail, _ := json.Marshal(map[string]any{"remaining": b.Remaining, "unit": b.Unit, "threshold": rule.BalanceThreshold, "sample_at": b.CheckedAt})
		if _, err = tx.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message,context) VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), w.OwnerID, w.ID, kind, message, detail); err != nil {
			return err
		}
		last = &now
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_balance_states SET source_generation=$2,active=$3,unit=$4,last_event_at=$5 WHERE account_id=$1`, w.ID, current, low, b.Unit, last); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) recordPriceNotificationTx(ctx context.Context, tx pgx.Tx, w AccountWork, rate float64) error {
	var threshold float64
	var cooldown int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT price_rise_percent FROM notification_rules WHERE owner_id=$1),5),COALESCE((SELECT price_cooldown_seconds FROM notification_rules WHERE owner_id=$1),3600)`, w.OwnerID).Scan(&threshold, &cooldown); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notification_price_states(account_id,source_generation,reference_rate) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w.ID, w.SourceGeneration, rate); err != nil {
		return err
	}
	var reference float64
	var generation int64
	var last *time.Time
	if err := tx.QueryRow(ctx, `SELECT source_generation,reference_rate,last_event_at FROM notification_price_states WHERE account_id=$1 FOR UPDATE`, w.ID).Scan(&generation, &reference, &last); err != nil {
		return err
	}
	now := time.Now().UTC()
	if generation != w.SourceGeneration || rate < reference {
		_, err := tx.Exec(ctx, `UPDATE notification_price_states SET source_generation=$2,reference_rate=$3,last_event_at=CASE WHEN source_generation<>$2 THEN NULL ELSE last_event_at END WHERE account_id=$1`, w.ID, w.SourceGeneration, rate)
		return err
	}
	rises := rate > reference && (reference == 0 || 100*(rate-reference)/reference >= threshold)
	if !rises || last != nil && now.Sub(*last) < time.Duration(cooldown)*time.Second {
		return nil
	}
	var percent *float64
	if reference > 0 {
		v := 100 * (rate - reference) / reference
		percent = &v
	}
	detail, _ := json.Marshal(map[string]any{"previous_rate": reference, "current_rate": rate, "rise_percent": percent, "threshold_percent": threshold, "basis": "procurement_multiplier"})
	message := fmt.Sprintf("%s：采购倍率 %.4f → %.4f", w.Name, reference, rate)
	if percent != nil {
		message += fmt.Sprintf("，上涨 %.1f%%", *percent)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message,context) VALUES($1,$2,$3,'price_change',$4,$5)`, uuid.NewString(), w.OwnerID, w.ID, message, detail); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE notification_price_states SET reference_rate=$2,last_event_at=$3 WHERE account_id=$1`, w.ID, rate, now)
	return err
}

func compactAlertText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-1]) + "…"
}
func formatBalanceAlertNumber(value float64) string {
	precision := 4
	if math.Abs(value) >= 1000 {
		precision = 0
	} else if math.Abs(value) >= 1 {
		precision = 2
	}
	formatted := strconv.FormatFloat(value, 'f', precision, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	return formatted
}
