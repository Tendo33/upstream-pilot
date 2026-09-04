package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type qualityAccountView struct {
	HasManagedControls bool                     `json:"has_managed_controls"`
	HasPendingControls bool                     `json:"has_pending_controls"`
	SortingLatency     *int                     `json:"sorting_latency_ms"`
	LatencySource      string                   `json:"latency_source"`
	Eligible           bool                     `json:"eligible"`
	ReferenceRate      *float64                 `json:"reference_rate"`
	Account            Account                  `json:"account"`
	Policy             quality.Policy           `json:"policy"`
	State              quality.State            `json:"state"`
	P95                *int                     `json:"first_content_p95_ms"`
	Samples            int                      `json:"sample_count"`
	SuccessPercent     *float64                 `json:"success_percent"`
	Balance            *float64                 `json:"balance"`
	BalanceUnit        string                   `json:"balance_unit"`
	BalanceAt          *time.Time               `json:"balance_at"`
	BalanceFresh       bool                     `json:"balance_fresh"`
	Rate               *float64                 `json:"cost_rate"`
	RateFresh          bool                     `json:"cost_fresh"`
	Traffic            *upstream.TrafficSummary `json:"traffic"`
	TrafficAt          *time.Time               `json:"traffic_at"`
}

func (a *App) qualityListHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	page, err := eventPageParameter(r, "page", 1, 100000)
	if err != nil {
		return err
	}
	size, err := eventPageParameter(r, "page_size", 50, 200)
	if err != nil {
		return err
	}
	site := r.URL.Query().Get("site_id")
	group := r.URL.Query().Get("group_id")
	search := r.URL.Query().Get("search")
	model := r.URL.Query().Get("model")
	_, hasModel := r.URL.Query()["model"]
	filter := ` FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE s.owner_id=$1 AND a.deleted_at IS NULL AND ($2='' OR a.site_id::text=$2) AND ($3='' OR EXISTS(SELECT 1 FROM account_group_memberships m WHERE m.account_id=a.id AND m.group_id::text=$3)) AND ($4='' OR a.name ILIKE '%'||$4||'%' OR a.remote_id::text=$4) AND (NOT $5 OR COALESCE(a.probe_model,'')=$6)`
	var total int
	if err = a.db.QueryRow(r.Context(), `SELECT count(*)`+filter, owner, site, group, search, hasModel, model).Scan(&total); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT a.id::text`+filter+` ORDER BY a.name,a.id LIMIT $7 OFFSET $8`, owner, site, group, search, hasModel, model, size, (page-1)*size)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	items := []qualityAccountView{}
	for _, id := range ids {
		view, err := a.qualityView(r.Context(), id, owner)
		if err != nil {
			return err
		}
		items = append(items, view)
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
	return nil
}

func (a *App) qualityView(ctx context.Context, id, owner string) (qualityAccountView, error) {
	v := qualityAccountView{}
	account, err := a.getAccount(ctx, id, owner)
	if err != nil {
		return v, err
	}
	v.Account = account
	work, err := a.loadAccountWork(ctx, id, owner)
	if err != nil {
		return v, err
	}
	v.Policy, err = a.loadQualityPolicy(ctx, id)
	if err != nil {
		return v, err
	}
	// Read-only views never create or advance control state.
	v.State, _, err = a.readQualityState(ctx, work)
	if err != nil {
		return v, err
	}
	if err = a.db.QueryRow(ctx, `SELECT COALESCE((SELECT last_applied_priority IS NOT NULL OR owned_pause OR applied_control<>'{}'::jsonb FROM quality_states WHERE account_id=$1),false),COALESCE((SELECT pending_priority IS NOT NULL OR COALESCE(pending_control->'to','{}'::jsonb)<>'{}'::jsonb FROM quality_states WHERE account_id=$1),false)`, id).Scan(&v.HasManagedControls, &v.HasPendingControls); err != nil {
		return v, err
	}
	snapshot, err := a.qualitySnapshot(ctx, work, v.Policy)
	if err != nil {
		return v, err
	}
	decision := quality.Evaluate(v.Policy, v.State, snapshot, time.Now().UTC())
	v.P95 = decision.P95
	v.SortingLatency = decision.SortingLatency
	v.LatencySource = decision.LatencySource
	v.Samples = decision.Count
	v.SuccessPercent = decision.SuccessPercent
	v.Eligible = decision.Eligible && !v.State.Conflict && v.State.PlanError == "" && work.RemoteStatus == "active" && work.Schedulable
	// Current evidence is shown separately from the last committed decision time.
	evaluated := v.State.EvaluatedAt
	desired := v.State.Desired
	v.State = decision.State
	v.State.EvaluatedAt = evaluated
	v.State.Desired = desired
	v.ReferenceRate = snapshot.ReferenceRate
	v.Balance = snapshot.Balance
	v.BalanceFresh = snapshot.BalanceFresh
	v.Rate = snapshot.Rate
	v.RateFresh = snapshot.RateFresh
	err = a.db.QueryRow(ctx, `SELECT unit,checked_at FROM account_balance_snapshots WHERE account_id=$1`, id).Scan(&v.BalanceUnit, &v.BalanceAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	var raw []byte
	err = a.db.QueryRow(ctx, `SELECT snapshot,checked_at FROM quality_traffic WHERE account_id=$1`, id).Scan(&raw, &v.TrafficAt)
	if err == nil {
		var t upstream.TrafficSummary
		if err = json.Unmarshal(raw, &t); err != nil {
			return v, err
		}
		v.Traffic = &t
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	return v, nil
}

func (a *App) qualityPolicyHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	if _, err = a.loadAccountWork(r.Context(), id, owner); err != nil {
		return err
	}
	input := struct {
		quality.Policy
		Monitoring *struct {
			Enabled     bool   `json:"enabled"`
			Model       string `json:"model"`
			Interval    int    `json:"interval_seconds"`
			Timeout     int    `json:"timeout_seconds"`
			CollectRate bool   `json:"collect_rate"`
		} `json:"monitoring"`
	}{Policy: quality.DefaultPolicy()}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	policy := input.Policy
	if err = policy.Validate(); err != nil {
		return &apiError{Status: 400, Code: "INVALID_POLICY", Message: err.Error()}
	}
	if m := input.Monitoring; m != nil {
		if m.Interval < 10 || m.Interval > 86400 || m.Timeout < 3 || m.Timeout > 600 || len(m.Model) > 256 {
			return &apiError{Status: 400, Code: "INVALID_MONITOR", Message: "探测间隔、超时或模型设置无效"}
		}
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	err = a.withAccountSchedulingLock(r.Context(), id, func(_ *pgxpool.Conn) error {
		tx, err := a.db.Begin(r.Context())
		if err != nil {
			return err
		}
		defer tx.Rollback(r.Context())
		_, err = tx.Exec(r.Context(), `INSERT INTO quality_policies(account_id,config) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET config=excluded.config,updated_at=now()`, id, raw)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE upstream_accounts SET config_generation=config_generation+1 WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=(SELECT site_id FROM upstream_accounts WHERE id=$1)`, id); err != nil {
			return err
		}
		if m := input.Monitoring; m != nil {
			_, err = tx.Exec(r.Context(), `UPDATE upstream_accounts SET health_enabled=$2,probe_model=NULLIF($3,''),probe_interval_seconds=$4,probe_timeout_seconds=$5,rate_sync_enabled=$6,next_probe_at=now(),next_rate_sync_at=now(),config_generation=config_generation+1,updated_at=now() WHERE id=$1`, id, m.Enabled, m.Model, m.Interval, m.Timeout, m.CollectRate)
			if err != nil {
				return err
			}
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		return err
	}
	_ = a.audit(r.Context(), owner, owner, "", id, "quality.policy.update", "success", map[string]any{"mode": policy.Mode, "auto_pause": policy.AutoPause})
	writeData(w, 200, policy)
	return nil
}

func (a *App) qualityEvaluateHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	work, err := a.loadAccountWork(r.Context(), id, owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := a.evaluateQuality(ctx, work, owner)
	if err != nil {
		return &apiError{Status: 422, Code: "QUALITY_APPLY_FAILED", Message: err.Error()}
	}
	writeData(w, 200, result.State)
	return nil
}

func (a *App) qualityReleaseHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	var input struct {
		Restore bool `json:"restore"`
	}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), id, owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err = a.withAccountSchedulingLock(ctx, id, func(_ *pgxpool.Conn) error {
		p, err := a.loadQualityPolicy(ctx, id)
		if err != nil {
			return err
		}
		state, pending, err := a.loadQualityState(ctx, work)
		if err != nil {
			return err
		}
		// Stop scheduling durably before restoration. If the RPC fails, the
		// background engine cannot undo the operator's interrupted release.
		p.Mode = "observe"
		p.AutoPause = false
		p.AutoLoadFactor = false
		p.AutoConcurrency = false
		stopped, _ := json.Marshal(p)
		if _, err = a.db.Exec(ctx, `INSERT INTO quality_policies(account_id,config) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET config=excluded.config,updated_at=now()`, id, stopped); err != nil {
			return err
		}
		client, err := a.clientForWork(work)
		if err != nil {
			return err
		}
		remote, err := client.GetAccount(ctx, work.RemoteID)
		if err != nil {
			return err
		}
		_ = pending
		if input.Restore {
			remote, err = a.restoreEngineControls(ctx, work, state, client, remote)
			if err != nil {
				return err
			}
		}

		// Releasing never enables an account: scheduling has a separate manual action.
		p.Mode = "observe"
		p.AutoPause = false
		p.AutoLoadFactor = false
		p.AutoConcurrency = false
		raw, _ := json.Marshal(p)
		tx, err := a.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `INSERT INTO quality_policies(account_id,config) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET config=excluded.config,updated_at=now()`, id, raw)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE quality_states SET baseline_priority=$2,desired_priority=$2,last_applied_priority=NULL,pending_priority=NULL,conflict=false,tier=0,recovery_streak=0,owned_pause=false,risks='[]',plan_error='',baseline_control='{}',applied_control='{}',pending_control='{}',status='unknown',reason='已停止接管，当前优先级作为新基准',evaluated_at=now() WHERE account_id=$1`, id, remote.Priority)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET priority=$2,managed_hold=false,config_generation=config_generation+1 WHERE id=$1`, id, remote.Priority)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return err
	}
	_ = a.audit(ctx, owner, owner, work.SiteID, id, "quality.release", "success", map[string]any{"restore": input.Restore})
	writeData(w, 200, map[string]bool{"released": true})
	return nil
}

func (a *App) qualityHistoryHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	if _, err = a.loadAccountWork(r.Context(), id, owner); err != nil {
		return err
	}
	result := map[string]json.RawMessage{}
	queries := map[string]string{
		"probes":    `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT created_at,success,first_content_ms,duration_ms,model,actual_model,stream_complete,failure_reason,http_status,message FROM probe_attempts WHERE account_id=$1 ORDER BY created_at DESC LIMIT 100)v`,
		"prices":    `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT source_rate,effective_rate,checked_at FROM upstream_price_history WHERE account_id=$1 ORDER BY checked_at DESC LIMIT 100)v`,
		"decisions": `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT mode,status,reason,before_priority,desired_priority,applied,error,created_at FROM quality_decisions WHERE account_id=$1 ORDER BY created_at DESC LIMIT 100)v`,
	}
	for name, query := range queries {
		var raw []byte
		if err = a.db.QueryRow(r.Context(), query, id).Scan(&raw); err != nil {
			return err
		}
		result[name] = raw
	}
	writeData(w, 200, result)
	return nil
}

func (a *App) sampleQualityTraffic(ctx context.Context, w AccountWork) error {
	var recent bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quality_traffic WHERE account_id=$1 AND checked_at>now()-interval '60 seconds')`, w.ID).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	client, err := a.clientForWork(w)
	if err != nil {
		return err
	}
	model := ""
	if w.ProbeModel != nil {
		model = *w.ProbeModel
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, sampleErr := client.RecentTraffic(requestCtx, w.RemoteID, model)
	if sampleErr != nil {
		snapshot.Status = "error"
		snapshot.Message = "真实请求采集失败；主动探测继续工作"
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO quality_traffic(account_id,snapshot) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET snapshot=excluded.snapshot,checked_at=now()`, w.ID, raw)
	return err
}

func (a *App) qualityGroupHandler(w http.ResponseWriter, r *http.Request) error {
	var raw []byte
	err := a.db.QueryRow(r.Context(), `WITH members AS (
 SELECT g.id,g.name,g.site_id,a.id AS account_id,COALESCE(a.probe_model,'') AS model,a.schedulable,
 q.status,q.last_sample_at,COALESCE((policy.config->>'fresh_seconds')::integer,600) AS fresh_seconds
 FROM upstream_groups g JOIN sites s ON s.id=g.site_id
 LEFT JOIN account_group_memberships m ON m.group_id=g.id
 LEFT JOIN upstream_accounts a ON a.id=m.account_id AND a.deleted_at IS NULL
 LEFT JOIN quality_states q ON q.account_id=a.id LEFT JOIN quality_policies policy ON policy.account_id=a.id
 WHERE s.owner_id=$1 AND s.enabled AND g.deleted_at IS NULL
 ) SELECT COALESCE(jsonb_agg(v),'[]') FROM (
 SELECT m.id,m.name,m.site_id,m.model,count(m.account_id) AS accounts,
 count(*) FILTER(WHERE m.schedulable) AS schedulable,
 count(*) FILTER(WHERE m.schedulable AND m.status='healthy' AND m.last_sample_at>now()-m.fresh_seconds*interval '1 second') AS healthy,
 count(*) FILTER(WHERE m.status='degraded') AS degraded,
 (SELECT round(100.0*count(*) FILTER(WHERE p.success)/NULLIF(count(*),0),1) FROM probe_attempts p JOIN members other ON other.account_id=p.account_id WHERE other.id=m.id AND other.model=m.model AND COALESCE(p.model,'')=m.model AND NOT p.control_error AND p.created_at>now()-other.fresh_seconds*interval '1 second') AS success_percent,
 (SELECT percentile_disc(0.95) WITHIN GROUP(ORDER BY p.first_content_ms) FROM probe_attempts p JOIN members other ON other.account_id=p.account_id WHERE other.id=m.id AND other.model=m.model AND COALESCE(p.model,'')=m.model AND p.success AND NOT p.control_error AND p.first_content_ms IS NOT NULL AND p.created_at>now()-other.fresh_seconds*interval '1 second') AS first_content_p95_ms
 FROM members m GROUP BY m.id,m.name,m.site_id,m.model ORDER BY m.name,m.model
 )v`, identityFrom(r).ID).Scan(&raw)
	if err != nil {
		return err
	}
	return a.writeEngineGroups(w, r, raw)
}
