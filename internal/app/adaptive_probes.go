package app

import (
	"context"
	"time"
)

func adaptiveCanaryDelay(config ServiceProfileConfig, samples, remaining int, success, realLatency bool, now time.Time) time.Duration {
	base := time.Duration(config.IntervalSeconds) * time.Second
	if !config.Adaptive {
		return base
	}
	if success && samples >= config.Objectives.MinimumSamples && realLatency {
		return min(24*time.Hour, base*3)
	}
	// Reserve room for the ordinary schedule before spending budget on a burst.
	dayEnd := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	normalLeft := int((dayEnd.Sub(now) + base - 1) / base)
	if remaining > normalLeft && (!success || samples < config.Objectives.MinimumSamples) {
		return max(30*time.Second, min(base/3, 2*time.Minute))
	}
	return base
}
func (a *App) nextCanaryDelay(ctx context.Context, p serviceProfileWork, success bool, remaining int) time.Duration {
	var count int
	_ = a.db.QueryRow(ctx, `SELECT count(*) FROM service_canary_runs WHERE profile_id=$1 AND generation=$2 AND status IN('passed','failed') AND started_at>now()-interval '24 hours'`, p.ID, p.Generation).Scan(&count)
	var measured bool
	if p.GroupID != "" {
		_ = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quality_traffic t JOIN upstream_accounts a ON a.id=t.account_id JOIN account_group_memberships m ON m.account_id=a.id WHERE m.group_id=$1 AND COALESCE(a.probe_model,'')=$2 AND t.source_generation=a.source_generation AND t.checked_at>now()-interval '5 minutes' AND t.snapshot->>'status'='ok' AND NOT COALESCE((t.snapshot->>'incomplete')::boolean,false) AND NOT COALESCE((t.snapshot->>'truncated')::boolean,false) AND (t.snapshot->>'first_content_samples')::int>=$3 AND (t.snapshot->>'failed')::int=0)`, p.GroupID, p.Config.Model, p.Config.Objectives.MinimumSamples*3).Scan(&measured)
	}
	return adaptiveCanaryDelay(p.Config, count, remaining, success, measured, time.Now())
}

func samplingWarnings(w AccountWork, minimum, window, fresh int, usesBalance, usesPrice bool) []string {
	warnings := []string{}
	if minimum > 600 {
		warnings = append(warnings, "最少样本数超过单轮供应商成功与失败合计 600 条采集上限")
	}
	if w.ProbeIntervalSeconds > 0 && minimum > window/w.ProbeIntervalSeconds+1 {
		warnings = append(warnings, "按当前探测间隔，统计窗口内无法获得足够主动样本")
	}
	if usesBalance && fresh < 360 {
		warnings = append(warnings, "有效期短于余额常规刷新周期，余额会周期性变为未知")
	}
	if usesPrice && fresh < w.RateSyncIntervalSeconds {
		warnings = append(warnings, "有效期短于采购倍率刷新周期，价格会周期性变为未知")
	}
	return warnings
}
