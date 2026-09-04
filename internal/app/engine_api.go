package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/quality"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
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
	owner := identityFrom(r).ID
	type fact struct {
		id, group, model                    string
		remoteGroup                         int64
		remoteStatus, accountType, platform string
		sched                               bool
		sourceGen, opsGen                   int64
		native                              upstream.NativeConstraints
		nativeAt                            *time.Time
		qualityStatus                       string
		lastSample, evaluated               *time.Time
		conflict                            bool
		planError                           string
		fresh                               int
	}
	rows, err := a.db.Query(r.Context(), `SELECT a.id::text,m.group_id::text,g.remote_id,COALESCE(a.probe_model,''),a.remote_status,a.schedulable,a.source_generation,a.account_type,a.platform,a.native_constraints,a.native_checked_at,COALESCE(q.status,'unknown'),q.last_sample_at,q.evaluated_at,COALESCE(q.conflict,false),COALESCE(q.plan_error,''),COALESCE((p.config->>'fresh_seconds')::int,600),COALESCE(o.source_generation,-1) FROM account_group_memberships m JOIN upstream_accounts a ON a.id=m.account_id JOIN upstream_groups g ON g.id=m.group_id JOIN sites s ON s.id=a.site_id LEFT JOIN quality_states q ON q.account_id=a.id LEFT JOIN quality_policies p ON p.account_id=a.id LEFT JOIN account_operations o ON o.account_id=a.id WHERE s.owner_id=$1 AND a.deleted_at IS NULL AND g.deleted_at IS NULL`, owner)
	if err != nil {
		return err
	}
	facts := []fact{}
	for rows.Next() {
		var f fact
		var native []byte
		if err = rows.Scan(&f.id, &f.group, &f.remoteGroup, &f.model, &f.remoteStatus, &f.sched, &f.sourceGen, &f.accountType, &f.platform, &native, &f.nativeAt, &f.qualityStatus, &f.lastSample, &f.evaluated, &f.conflict, &f.planError, &f.fresh, &f.opsGen); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(native, &f.native); err != nil {
			rows.Close()
			return err
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	directory, err := a.supplierMembers(r.Context(), owner, "")
	if err != nil {
		return err
	}
	domains := supplierComponents(directory)
	suppliers := map[string]supplierMember{}
	for _, m := range directory {
		suppliers[m.ID] = m
	}
	policyRows, err := a.db.Query(r.Context(), `SELECT p.group_id::text,p.model,p.config FROM engine_group_policies p JOIN upstream_groups g ON g.id=p.group_id JOIN sites s ON s.id=g.site_id WHERE s.owner_id=$1`, owner)
	if err != nil {
		return err
	}
	policies := map[string]quality.GroupPolicy{}
	for policyRows.Next() {
		var group, model string
		var data []byte
		if err = policyRows.Scan(&group, &model, &data); err != nil {
			policyRows.Close()
			return err
		}
		v := quality.DefaultGroupPolicy()
		if err = json.Unmarshal(data, &v); err != nil {
			policyRows.Close()
			return err
		}
		policies[poolKey(group, model)] = v
	}
	err = policyRows.Err()
	policyRows.Close()
	if err != nil {
		return err
	}
	now := time.Now()
	for _, g := range groups {
		id, _ := g["id"].(string)
		model, _ := g["model"].(string)
		healthy, degraded := 0, 0
		independent := map[string]bool{}
		capacityKnown := true
		spare := map[string]int{}
		var evaluated *time.Time
		for _, f := range facts {
			if f.group != id || f.model != model {
				continue
			}
			if f.qualityStatus == "degraded" {
				degraded++
			}
			if f.evaluated != nil && (evaluated == nil || f.evaluated.Before(*evaluated)) {
				v := *f.evaluated
				evaluated = &v
			}
			native := upstream.Sub2Account{Native: f.native, Platform: f.platform, Type: f.accountType, Status: f.remoteStatus, Schedulable: f.sched}.NativeEligibility(model, []int64{f.remoteGroup}, now)
			eligible := f.qualityStatus == "healthy" && f.lastSample != nil && now.Sub(*f.lastSample) <= time.Duration(f.fresh)*time.Second && !f.conflict && f.planError == "" && native.State == "eligible" && f.opsGen == f.sourceGen
			if eligible {
				healthy++
				s := suppliers[f.id]
				if s.independentKnown() {
					d := domains[f.id]
					independent[d] = true
					n := f.native
					if n.CapacityVerified && n.Concurrency != nil && n.CurrentConcurrency != nil && n.QueueDepth != nil && *n.QueueDepth == 0 {
						spare[d] = max(spare[d], max(0, *n.Concurrency-*n.CurrentConcurrency))
					} else {
						capacityKnown = false
					}
				} else {
					capacityKnown = false
				}
			}
		}
		slots := 0
		for _, v := range spare {
			slots += v
		}
		g["healthy"] = healthy
		g["degraded"] = degraded
		g["independent_healthy"] = len(independent)
		g["independent_backups"] = max(0, len(independent)-1)
		g["verified_spare_slots"] = slots
		g["capacity_known"] = capacityKnown && healthy > 0
		g["policy"] = groupPolicy(policies, poolKey(id, model))
		g["checked_at"] = evaluated
	}
	writeData(w, 200, groups)
	return nil
}
