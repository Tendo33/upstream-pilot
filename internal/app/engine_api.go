package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (a *App) engineGroupPolicyHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "groupID")
	if err != nil {
		return err
	}
	var site string
	if err = a.db.QueryRow(r.Context(), `SELECT g.site_id::text FROM upstream_groups g JOIN sites s ON s.id=g.site_id WHERE g.id=$1 AND s.owner_id=$2 AND g.deleted_at IS NULL`, id, identityFrom(r).ID).Scan(&site); err != nil {
		return err
	}
	input := struct {
		Model string `json:"model"`
		quality.GroupPolicy
	}{GroupPolicy: quality.DefaultGroupPolicy()}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	input.Model = strings.TrimSpace(input.Model)
	if len(input.Model) > 256 {
		return &apiError{Status: 400, Code: "INVALID_MODEL", Message: "模型名称过长"}
	}
	if err = input.Validate(); err != nil {
		return &apiError{Status: 400, Code: "INVALID_STRATEGY", Message: err.Error()}
	}
	raw, _ := json.Marshal(input.GroupPolicy)
	err = a.withSiteSchedulingLock(r.Context(), site, func(_ *pgxpool.Conn) error {
		tx, err := a.db.Begin(r.Context())
		if err != nil {
			return err
		}
		defer tx.Rollback(r.Context())
		if _, err = tx.Exec(r.Context(), `INSERT INTO engine_group_policies(group_id,model,config) VALUES($1,$2,$3) ON CONFLICT(group_id,model) DO UPDATE SET config=excluded.config,updated_at=now()`, id, input.Model, raw); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, site); err != nil {
			return err
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		return err
	}
	writeData(w, 200, input)
	return nil
}
func (a *App) engineReferenceHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), id, identityFrom(r).ID)
	if err != nil {
		return err
	}
	var input struct {
		Rate float64 `json:"rate"`
	}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	if !isFinite(input.Rate) || input.Rate < 0 {
		return &apiError{Status: 400, Code: "INVALID_RATE", Message: "采购基准无效"}
	}
	err = a.withSiteSchedulingLock(r.Context(), work.SiteID, func(_ *pgxpool.Conn) error {
		p, err := a.loadQualityPolicy(r.Context(), id)
		if err != nil {
			return err
		}
		command, err := a.db.Exec(r.Context(), `UPDATE upstream_accounts SET price_reference_rate=observed_cost_rate WHERE id=$1 AND observed_cost_rate=$2 AND price_status='ok' AND source_generation=$4 AND price_source_generation=$4 AND last_rate_sync_at>now()-$3*interval '1 second'`, id, input.Rate, p.FreshSeconds, work.SourceGeneration)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return &apiError{Status: 409, Code: "STALE_PRICE", Message: "采购价格已变化或过期，请刷新后再确认"}
		}
		_, err = a.db.Exec(r.Context(), `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, work.SiteID)
		return err
	})
	if err != nil {
		return err
	}
	_ = a.audit(r.Context(), work.OwnerID, work.OwnerID, work.SiteID, id, "quality.price_reference", "success", map[string]any{"rate": input.Rate})
	writeData(w, 200, map[string]any{"reference_rate": input.Rate})
	return nil
}

func (a *App) writeEngineGroups(w http.ResponseWriter, r *http.Request, raw []byte) error {
	groups := []map[string]any{}
	if err := json.Unmarshal(raw, &groups); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT m.group_id::text,a.id::text FROM account_group_memberships m JOIN upstream_accounts a ON a.id=m.account_id JOIN sites s ON s.id=a.site_id WHERE s.owner_id=$1 AND s.enabled AND a.deleted_at IS NULL`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	members := map[string][]string{}
	for rows.Next() {
		var g, id string
		if err = rows.Scan(&g, &id); err != nil {
			break
		}
		members[g] = append(members[g], id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	views := map[string]qualityAccountView{}
	for _, g := range groups {
		id := g["id"].(string)
		model, _ := g["model"].(string)
		healthy, degraded := 0, 0
		for _, account := range members[id] {
			v, ok := views[account]
			if !ok {
				v, err = a.qualityView(r.Context(), account, identityFrom(r).ID)
				if err != nil {
					return err
				}
				views[account] = v
			}
			m := ""
			if v.Account.ProbeModel != nil {
				m = *v.Account.ProbeModel
			}
			if m != model {
				continue
			}
			if v.Eligible {
				healthy++
			}
			if v.State.Tier > 0 {
				degraded++
			}
		}
		g["healthy"] = healthy
		g["degraded"] = degraded
		p := quality.DefaultGroupPolicy()
		var policy []byte
		if err = a.db.QueryRow(r.Context(), `SELECT COALESCE((SELECT config FROM engine_group_policies WHERE group_id=$1 AND model=$2),'{}'::jsonb)`, id, model).Scan(&policy); err != nil {
			return err
		}
		if err = json.Unmarshal(policy, &p); err != nil {
			return err
		}
		g["policy"] = p
		g["checked_at"] = time.Now().UTC()
	}
	writeData(w, 200, groups)
	return nil
}
