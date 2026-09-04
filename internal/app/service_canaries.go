package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/google/uuid"
)

type reservedCanary struct {
	Remaining int
	ID        string
	Profile   serviceProfileWork
}

func (a *App) reserveCanary(ctx context.Context, id, owner string, scheduled bool, leaseTokens ...string) (reservedCanary, error) {
	var run reservedCanary
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return run, err
	}
	defer tx.Rollback(ctx)
	p, err := scanServiceProfile(tx.QueryRow(ctx, serviceProfileSelect+` WHERE p.id=$1 AND s.owner_id=COALESCE(NULLIF($2,'')::uuid,s.owner_id) AND `+serviceProfileParentLive+` FOR UPDATE OF p`, id, owner))
	if err != nil {
		return run, err
	}
	if scheduled {
		token := ""
		if len(leaseTokens) > 0 {
			token = leaseTokens[0]
		}
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_token::text=$2 AND lease_until>now(),false) FROM service_profiles WHERE id=$1`, id, token).Scan(&valid); err != nil {
			return run, err
		}
		if !valid {
			return run, errors.New("探测租约已变化或过期")
		}
	}
	if scheduled && !p.Enabled {
		return run, errors.New("探测档案已暂停")
	}
	if p.AccountID == "" && (!p.Config.GroupKeyConfirmed || !p.KeyConfigured) {
		return run, &apiError{Status: 400, Code: "GROUP_KEY_REQUIRED", Message: "请先确认分组专用 Key"}
	}
	if p.AccountID != "" && !p.Config.DirectSourceConfirmed {
		return run, &apiError{Status: 400, Code: "DIRECT_SOURCE_REQUIRED", Message: "请确认直接账号探测的出口和凭据读取"}
	}
	if err = p.Config.Validate(); err != nil {
		return run, err
	}
	reject := func(cause error) (reservedCanary, error) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return run, errors.Join(cause, commitErr)
		}
		return run, cause
	}
	if _, err = tx.Exec(ctx, `UPDATE service_canary_runs SET status='abandoned',completed_at=now(),result='{"failure":"execution_interrupted"}' WHERE profile_id=$1 AND status='reserved' AND started_at<now()-$2*interval '1 second'`, id, p.Config.TimeoutSeconds+30); err != nil {
		return run, err
	}
	var inflight int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM service_canary_runs WHERE profile_id=$1 AND status='reserved'`, id).Scan(&inflight); err != nil {
		return run, err
	}
	var requests, tokens, unknownCosts int
	var cost float64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(reserved_tokens),0),COALESCE(sum(reserved_cost),0),count(*) FILTER(WHERE reserved_cost IS NULL OR profile_snapshot->'config'->'budget'->>'currency' IS DISTINCT FROM $2) FROM service_canary_runs WHERE profile_id=$1 AND started_at>=(date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`, id, p.Config.Budget.Currency).Scan(&requests, &tokens, &cost, &unknownCosts); err != nil {
		return run, err
	}
	if inflight > 0 {
		return reject(&apiError{Status: 409, Code: "CANARY_RUNNING", Message: "此档案已有探测在途，请等待结果"})
	}
	b := p.Config.Budget
	if requests >= b.DailyRequests || tokens+p.Config.reservedTokens() > b.DailyTokens {
		return reject(&apiError{Status: 429, Code: "CANARY_BUDGET", Message: "今日请求或 token 预留预算已用完（UTC 日界）"})
	}
	if b.DailyCost != nil && unknownCosts > 0 {
		return reject(&apiError{Status: 409, Code: "COST_NOT_COMPARABLE", Message: "当日已有未知或其他币种的成本，无法确认剩余金额预算"})
	}
	if b.DailyCost != nil && (b.RequestCostReserve == nil || cost+*b.RequestCostReserve > *b.DailyCost+1e-9) {
		return reject(&apiError{Status: 429, Code: "CANARY_COST_BUDGET", Message: "金额预算不足或单次价格未知"})
	}
	run = reservedCanary{ID: uuid.NewString(), Profile: p, Remaining: min(b.DailyRequests-requests-1, (b.DailyTokens-tokens-p.Config.reservedTokens())/p.Config.reservedTokens())}
	if b.DailyCost != nil && b.RequestCostReserve != nil {
		run.Remaining = min(run.Remaining, int((*b.DailyCost-cost-*b.RequestCostReserve) / *b.RequestCostReserve))
	}
	snapshot, _ := json.Marshal(map[string]any{"config": p.Config, "group_id": p.GroupID, "account_id": p.AccountID, "site_base_url": p.SiteBaseURL})
	if _, err = tx.Exec(ctx, `INSERT INTO service_canary_runs(id,profile_id,generation,status,reserved_tokens,reserved_cost,profile_snapshot) VALUES($1,$2,$3,'reserved',$4,$5,$6)`, run.ID, id, p.Generation, p.Config.reservedTokens(), b.RequestCostReserve, snapshot); err != nil {
		return run, err
	}
	if err = tx.Commit(ctx); err != nil {
		return run, err
	}
	return run, nil
}

func (a *App) runServiceCanary(ctx context.Context, id, owner string, scheduled bool, leaseTokens ...string) (upstream.CanaryResult, error) {
	var result upstream.CanaryResult
	run, err := a.reserveCanary(ctx, id, owner, scheduled, leaseTokens...)
	if err != nil {
		return result, err
	}
	p := run.Profile
	requestCtx, requestCancel := context.WithTimeout(ctx, time.Duration(p.Config.TimeoutSeconds)*time.Second)
	base, key := "", ""
	if p.AccountID != "" {
		base, key, err = a.accountCanarySource(requestCtx, p)
	} else {
		key, err = a.cipher.Decrypt(p.KeyCiphertext, "service-profile:"+p.ID)
		base = p.Config.BaseURL
		if base == "" {
			base = p.SiteBaseURL
		}
	}
	if err == nil {
		spec := p.Config.CanarySpec
		spec.TraceID = run.ID
		result, err = upstream.ProbeGateway(requestCtx, a.httpClient, base, key, spec)
	} else {
		result.ControlError = true
		result.Failure = "探测准备失败：" + truncateError(err)
	}
	if ctx.Err() != nil {
		result.ControlError = true
	}
	requestCancel()

	if err != nil && result.Failure == "" {
		result.Failure = "canary_execution_failed"
	}
	// A canceled UI request must not lose a charged attempt. Persist completion
	// independently, retaining its original generation for later audit.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	raw, _ := json.Marshal(result)
	status := "failed"
	if result.Success {
		status = "passed"
	}
	tx, saveErr := a.db.Begin(persistCtx)
	if saveErr != nil {
		return result, &apiError{Status: 503, Code: "CANARY_RESULT_UNCONFIRMED", Message: "探测已发出，但结果未确认保存；请查看记录，勿立即重复请求"}
	}
	defer tx.Rollback(persistCtx)
	command, saveErr := tx.Exec(persistCtx, `UPDATE service_canary_runs SET status=$2,result=$3,completed_at=now() WHERE id=$1 AND status='reserved'`, run.ID, status, raw)
	if saveErr != nil {
		return result, &apiError{Status: 503, Code: "CANARY_RESULT_UNCONFIRMED", Message: "探测已发出，但结果未确认保存；请查看记录，勿立即重复请求"}
	}
	if command.RowsAffected() != 1 {
		return result, &apiError{Status: 409, Code: "CANARY_ALREADY_SETTLED", Message: "此探测已按中断状态结算，迟到结果不替换当前状态"}
	}
	delay := a.nextCanaryDelay(persistCtx, p, result.Success, run.Remaining)
	if _, saveErr = tx.Exec(persistCtx, `UPDATE service_profiles SET last_probe_at=now(),last_error=$3,next_probe_at=now()+$4*interval '1 second' WHERE id=$1 AND generation=$2`, p.ID, p.Generation, result.Failure, int(delay.Seconds())); saveErr != nil {
		return result, &apiError{Status: 503, Code: "CANARY_RESULT_UNCONFIRMED", Message: "探测已发出，但结果未确认保存；请查看记录，勿立即重复请求"}
	}
	if saveErr = tx.Commit(persistCtx); saveErr != nil {
		return result, &apiError{Status: 503, Code: "CANARY_RESULT_UNCONFIRMED", Message: "探测已发出，但结果未确认保存；请查看记录，勿立即重复请求"}
	}
	return result, err
}

func (a *App) runServiceCanaryHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "profileID")
	if err != nil {
		return err
	}
	result, err := a.runServiceCanary(r.Context(), id, identityFrom(r).ID, false)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		if result.Failure == "" {
			return err
		}
	}
	writeData(w, 200, result)
	return nil
}

func (a *App) serviceCanaryHistoryHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "profileID")
	if err != nil {
		return err
	}
	if _, err = a.loadServiceProfile(r.Context(), id, identityFrom(r).ID); err != nil {
		return err
	}
	var raw []byte
	if err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT id,generation,status,reserved_tokens,reserved_cost,result,profile_snapshot,started_at,completed_at FROM service_canary_runs WHERE profile_id=$1 ORDER BY started_at DESC LIMIT 100)v`, id).Scan(&raw); err != nil {
		return err
	}
	writeData(w, 200, json.RawMessage(raw))
	return nil
}
