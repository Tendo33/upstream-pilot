package app

import (
	"context"
	"encoding/json"
	"time"
)

type actionSLI struct {
	ProfileID      string   `json:"profile_id"`
	Generation     int64    `json:"generation"`
	MinimumSamples int      `json:"minimum_samples"`
	Samples        int      `json:"samples"`
	Unconfirmed    int      `json:"unconfirmed"`
	SuccessPercent *float64 `json:"success_percent"`
	P95            *int     `json:"first_content_p95_ms"`
}

func (a *App) capturePoolSLI(ctx context.Context, pools []string, start, end time.Time) ([]actionSLI, error) {
	result := []actionSLI{}
	if len(pools) == 0 {
		return result, nil
	}
	rows, err := a.db.Query(ctx, `SELECT p.id::text,p.generation,COALESCE((p.config->'objectives'->>'minimum_samples')::int,5),
 count(r.id) FILTER(WHERE r.status IN('passed','failed') AND NOT COALESCE((r.result->>'control_error')::boolean,false)),
 count(r.id) FILTER(WHERE r.status IN('reserved','abandoned') OR COALESCE((r.result->>'control_error')::boolean,false)),
 100.0*count(r.id) FILTER(WHERE r.status='passed')/NULLIF(count(r.id) FILTER(WHERE r.status IN('passed','failed') AND NOT COALESCE((r.result->>'control_error')::boolean,false)),0),
 percentile_disc(0.95) WITHIN GROUP(ORDER BY (r.result->>'first_content_ms')::int) FILTER(WHERE r.status IN('passed','failed') AND r.result->>'first_content_ms' IS NOT NULL AND NOT COALESCE((r.result->>'control_error')::boolean,false))
 FROM service_profiles p LEFT JOIN service_canary_runs r ON r.profile_id=p.id AND r.generation=p.generation AND r.started_at>=$2 AND r.started_at<$3 AND (r.completed_at<=$3 OR r.completed_at IS NULL)
 WHERE p.group_id IS NOT NULL AND (p.group_id::text||'/'||(p.config->>'model'))=ANY($1) AND COALESCE((p.config->>'group_key_confirmed')::boolean,false) AND(p.enabled OR p.last_probe_at IS NOT NULL)
 GROUP BY p.id,p.generation ORDER BY p.id`, pools, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v actionSLI
		if err = rows.Scan(&v.ProfileID, &v.Generation, &v.MinimumSamples, &v.Samples, &v.Unconfirmed, &v.SuccessPercent, &v.P95); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func compareActionSLI(before, after []actionSLI) (string, string) {
	if len(before) == 0 || len(before) != len(after) {
		return "unverified", "缺少相同分组档案的前后观测"
	}
	prior := map[string]actionSLI{}
	for _, v := range before {
		prior[v.ProfileID] = v
	}
	better := false
	for _, current := range after {
		old, ok := prior[current.ProfileID]
		if !ok || old.Generation != current.Generation {
			return "unverified", "观测档案或来源已变化"
		}
		if old.Samples < old.MinimumSamples || current.Samples < current.MinimumSamples || old.Unconfirmed > 0 || current.Unconfirmed > 0 || old.SuccessPercent == nil || current.SuccessPercent == nil {
			return "unverified", "前后窗口样本不足或有未确认请求"
		}
		if *current.SuccessPercent < *old.SuccessPercent-1 {
			return "regressed", "合成入口的成功率较动作前下降"
		}
		if current.P95 != nil && old.P95 != nil {
			if *current.P95 > *old.P95+250 && float64(*current.P95) > 1.1*float64(*old.P95) {
				return "regressed", "合成入口首字延迟较动作前上升"
			}
			better = better || (*current.P95 < *old.P95-250 && float64(*current.P95) < .9*float64(*old.P95))
		}
		better = better || *current.SuccessPercent > *old.SuccessPercent+1
	}
	if better {
		return "improved", "合成入口窗口观测改善，仍需结合真实用户结果判断"
	}
	return "unchanged", "已有足量样本，未观察到明显改善或恶化"
}

func (a *App) evaluateActionEffects(ctx context.Context, site string) error {
	rows, err := a.db.Query(ctx, `SELECT x.id::text,x.account_id::text,x.source_generation,x.pools,x.before_sli,x.created_at,x.window_seconds FROM engine_actions x JOIN upstream_accounts a ON a.id=x.account_id WHERE a.site_id=$1 AND x.checked_at IS NULL AND x.created_at+x.window_seconds*interval '1 second'<=now() ORDER BY x.created_at LIMIT 50`, site)
	if err != nil {
		return err
	}
	type pending struct {
		id, account   string
		generation    int64
		pools, before []byte
		at            time.Time
		window        int
	}
	items := []pending{}
	for rows.Next() {
		var v pending
		if err = rows.Scan(&v.id, &v.account, &v.generation, &v.pools, &v.before, &v.at, &v.window); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		var pools []string
		var before []actionSLI
		if err = json.Unmarshal(v.pools, &pools); err != nil {
			return err
		}
		if err = json.Unmarshal(v.before, &before); err != nil {
			return err
		}
		after, err := a.capturePoolSLI(ctx, pools, v.at, v.at.Add(time.Duration(v.window)*time.Second))
		if err != nil {
			return err
		}
		status, reason := compareActionSLI(before, after)
		var generation int64
		var conflict bool
		if err = a.db.QueryRow(ctx, `SELECT a.source_generation,COALESCE(q.conflict,false) FROM upstream_accounts a LEFT JOIN quality_states q ON q.account_id=a.id WHERE a.id=$1`, v.account).Scan(&generation, &conflict); err != nil {
			return err
		}
		if generation != v.generation {
			status, reason = "unverified", "账号来源已变化，不能归因于此动作"
		} else if conflict {
			status, reason = "unverified", "存在人工修改，不能单独评估此动作"
		}
		raw, _ := json.Marshal(after)
		if _, err = a.db.Exec(ctx, `UPDATE engine_actions SET after_sli=$2,effect_status=$3,effect_reason=$4,checked_at=now() WHERE id=$1 AND checked_at IS NULL`, v.id, raw, status, reason); err != nil {
			return err
		}
	}
	return nil
}
