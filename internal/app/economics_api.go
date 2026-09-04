package app

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
	"time"
)

func (a *App) ownerRunways(ctx context.Context, owner string) (map[string]Runway, error) {
	rows, err := a.db.Query(ctx, `SELECT b.account_id::text,b.checked_at,b.remaining,b.unit FROM balance_observations b JOIN upstream_accounts a ON a.id=b.account_id JOIN sites s ON s.id=a.site_id WHERE ($1='' OR s.owner_id::text=$1) AND a.deleted_at IS NULL AND b.source_generation=a.source_generation AND b.status='ok' AND b.remaining IS NOT NULL AND b.checked_at>now()-interval '24 hours' ORDER BY b.account_id,b.checked_at`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := map[string][]BalancePoint{}
	for rows.Next() {
		var id string
		var p BalancePoint
		if err = rows.Scan(&id, &p.At, &p.Remaining, &p.Unit); err != nil {
			return nil, err
		}
		points[id] = append(points[id], p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]Runway{}
	for id, p := range points {
		result[id] = balanceRunway(p, time.Now())
	}
	resetRows, e := a.db.Query(ctx, `SELECT a.id::text,(a.native_constraints->>'quota_reset_at')::timestamptz FROM upstream_accounts a JOIN sites s ON s.id=a.site_id WHERE ($1='' OR s.owner_id::text=$1) AND a.deleted_at IS NULL AND a.native_checked_at>now()-interval '5 minutes' AND a.native_constraints->>'quota_reset_at' IS NOT NULL`, owner)
	if e != nil {
		return nil, e
	}
	defer resetRows.Close()
	for resetRows.Next() {
		var id string
		var at time.Time
		if e = resetRows.Scan(&id, &at); e != nil {
			return nil, e
		}
		v, ok := result[id]
		if !ok {
			v = Runway{Status: "unknown", Reason: "消耗样本不足"}
		}
		if time.Now().Before(at) {
			v.QuotaResetAt = &at
		}
		result[id] = v
	}
	if e = resetRows.Err(); e != nil {
		return nil, e
	}
	return result, nil
}

func (a *App) economicsHandler(w http.ResponseWriter, r *http.Request) error {
	var cards []byte
	owner := identityFrom(r).ID
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT c.account_id,a.name,c.model,c.source_generation,c.source_generation=a.source_generation AS current_source,c.config,c.updated_at FROM model_price_cards c JOIN upstream_accounts a ON a.id=c.account_id JOIN sites s ON s.id=a.site_id WHERE s.owner_id=$1 AND a.deleted_at IS NULL ORDER BY a.name,c.model)v`, owner).Scan(&cards)
	if err != nil {
		return err
	}
	runways, err := a.ownerRunways(r.Context(), owner)
	if err != nil {
		return err
	}
	writeData(w, 200, map[string]any{"cards": json.RawMessage(cards), "runways": runways})
	return nil
}
func (a *App) savePriceCardHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	var input struct {
		Model            string    `json:"model"`
		SourceGeneration int64     `json:"source_generation"`
		Card             PriceCard `json:"card"`
	}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" || len(input.Model) > 256 {
		return &apiError{Status: 400, Code: "INVALID_MODEL", Message: "请填写该账号对外模型名称"}
	}
	if err = input.Card.Validate(); err != nil {
		return &apiError{Status: 400, Code: "INVALID_PRICE_CARD", Message: err.Error()}
	}
	wk, err := a.loadAccountWork(r.Context(), id, owner)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(input.Card)
	err = a.withSiteSchedulingLock(r.Context(), wk.SiteID, func(_ *pgxpool.Conn) error {
		tx, e := a.db.Begin(r.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback(r.Context())
		var gen int64
		if e = tx.QueryRow(r.Context(), `SELECT source_generation FROM upstream_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&gen); e != nil {
			return e
		}
		if gen != input.SourceGeneration {
			return &apiError{Status: 409, Code: "SOURCE_CHANGED", Message: "来源已变化，请刷新后重新录入价格"}
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO model_price_cards(account_id,model,source_generation,config) VALUES($1,$2,$3,$4) ON CONFLICT(account_id,model) DO UPDATE SET source_generation=excluded.source_generation,config=excluded.config,updated_at=now()`, id, input.Model, gen, raw); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `UPDATE upstream_accounts SET config_generation=config_generation+1 WHERE id=$1`, id); e != nil {
			return e
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		return err
	}
	_ = a.audit(r.Context(), owner, owner, wk.SiteID, id, "price_card.save", "success", map[string]any{"model": input.Model, "currency": input.Card.Currency, "basis": input.Card.Basis})
	writeData(w, 200, map[string]any{"saved": true})
	return nil
}

func (a *App) evaluateRunwayAlerts(ctx context.Context) error {
	runways, err := a.ownerRunways(ctx, "")
	if err != nil {
		return err
	}
	for id, r := range runways {
		w, e := a.loadAccountWork(ctx, id, "")
		if e != nil {
			continue
		}
		var configRaw []byte
		_ = a.db.QueryRow(ctx, `SELECT COALESCE((SELECT config FROM account_operations WHERE account_id=$1),'{}')`, id).Scan(&configRaw)
		var config AccountOperations
		_ = json.Unmarshal(configRaw, &config)
		threshold := 6.0
		if config.RunwayWarnHours != nil {
			threshold = *config.RunwayWarnHours
		}
		message := ""
		if threshold > 0 && r.Status == "estimated" && r.HoursLow != nil && *r.HoursLow < threshold {
			message = "余额续航低于预警阈值，请补充余额或准备独立备用"
		}
		if e = a.recordIncident(ctx, w, "balance_runway", message); e != nil {
			return e
		}
	}
	return nil
}
