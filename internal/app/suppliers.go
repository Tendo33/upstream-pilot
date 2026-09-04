package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AccountOperations struct {
	MultiplierBasis string   `json:"multiplier_basis"`
	RunwayWarnHours *float64 `json:"runway_warn_hours"`
	Provider        string   `json:"provider"`
	FailureDomain   string   `json:"failure_domain"`
	QuotaPool       string   `json:"quota_pool"`
	Confirmed       bool     `json:"confirmed"`
	Notes           string   `json:"notes"`
}

type supplierMember struct {
	ID                 string            `json:"id"`
	SiteID             string            `json:"site_id"`
	Name               string            `json:"name"`
	SourceGeneration   int64             `json:"source_generation"`
	CurrentSource      bool              `json:"current_source"`
	SourceHint         string            `json:"source_hint"`
	Config             AccountOperations `json:"config"`
	CredentialIdentity string            `json:"-"`
}

func (a *App) supplierMembers(ctx context.Context, owner, site string) ([]supplierMember, error) {
	rows, err := a.db.Query(ctx, `SELECT a.id::text,a.site_id::text,a.name,a.source_generation,COALESCE(o.source_generation=a.source_generation,false),COALESCE(a.observed_source_base_url,''),COALESCE(a.observed_source_credential_fingerprint,''),COALESCE(o.config,'{}') FROM upstream_accounts a JOIN sites s ON s.id=a.site_id LEFT JOIN account_operations o ON o.account_id=a.id WHERE a.deleted_at IS NULL AND ($1='' OR s.owner_id::text=$1) AND($2='' OR a.site_id::text=$2) ORDER BY a.name,a.id`, owner, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []supplierMember{}
	for rows.Next() {
		var m supplierMember
		var raw []byte
		var source, fingerprint string
		if err = rows.Scan(&m.ID, &m.SiteID, &m.Name, &m.SourceGeneration, &m.CurrentSource, &source, &fingerprint, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &m.Config); err != nil {
			return nil, err
		}
		if u, e := url.Parse(source); e == nil {
			m.SourceHint = u.Hostname()
		}
		if fingerprint != "" && source != "" {
			m.CredentialIdentity = canonicalBalanceSourceURL(source, "sub2api") + "/" + fingerprint
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// Shared supplier, failure domain, quota pool OR exact URL/key identity joins
// accounts into one failure component. Distinct hostnames never prove independence.
func supplierComponents(members []supplierMember) map[string]string {
	parent := map[string]string{}
	keyOwner := map[string]string{}
	var root func(string) string
	root = func(id string) string {
		if parent[id] != id {
			parent[id] = root(parent[id])
		}
		return parent[id]
	}
	for _, m := range members {
		parent[m.ID] = m.ID
	}
	for _, m := range members {
		keys := []string{}
		if m.CurrentSource && m.Config.Confirmed {
			for k, v := range map[string]string{"provider": m.Config.Provider, "domain": m.Config.FailureDomain, "quota": m.Config.QuotaPool} {
				if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
					keys = append(keys, k+":"+v)
				}
			}
		}
		if m.CredentialIdentity != "" {
			keys = append(keys, "credential:"+m.CredentialIdentity)
		}
		for _, key := range keys {
			if other, ok := keyOwner[key]; ok {
				x, y := root(m.ID), root(other)
				if x < y {
					parent[y] = x
				} else {
					parent[x] = y
				}
			} else {
				keyOwner[key] = m.ID
			}
		}
	}
	for id := range parent {
		parent[id] = root(id)
	}
	return parent
}
func (m supplierMember) independentKnown() bool {
	return m.CurrentSource && m.Config.Confirmed && m.Config.Provider != "" && m.Config.FailureDomain != "" && m.Config.QuotaPool != ""
}

func (a *App) supplierListHandler(w http.ResponseWriter, r *http.Request) error {
	members, err := a.supplierMembers(r.Context(), identityFrom(r).ID, "")
	if err != nil {
		return err
	}
	components := supplierComponents(members)
	type row struct {
		supplierMember
		Component        string `json:"component"`
		IndependentKnown bool   `json:"independent_known"`
	}
	result := []row{}
	for _, m := range members {
		result = append(result, row{m, components[m.ID], m.independentKnown()})
	}
	writeData(w, 200, result)
	return nil
}
func (a *App) supplierSaveHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	owner := identityFrom(r).ID
	var input struct {
		SourceGeneration int64             `json:"source_generation"`
		Config           AccountOperations `json:"config"`
	}
	if err = decodeJSON(r, &input); err != nil {
		return err
	}
	p := &input.Config
	p.MultiplierBasis = strings.TrimSpace(p.MultiplierBasis)
	if len(p.MultiplierBasis) > 256 || p.RunwayWarnHours != nil && (!isFinite(*p.RunwayWarnHours) || *p.RunwayWarnHours < 0 || *p.RunwayWarnHours > 720) {
		return &apiError{Status: 400, Code: "INVALID_OPERATIONS", Message: "计价依据或续航阈值无效"}
	}
	p.Provider = strings.TrimSpace(p.Provider)
	p.FailureDomain = strings.TrimSpace(p.FailureDomain)
	p.QuotaPool = strings.TrimSpace(p.QuotaPool)
	for _, s := range []string{p.Provider, p.FailureDomain, p.QuotaPool} {
		if utf8.RuneCountInString(s) > 120 {
			return &apiError{Status: 400, Code: "INVALID_SUPPLIER", Message: "供应商、失效域和额度池名称不能超过 120 字"}
		}
	}
	if len(p.Notes) > 2000 || p.Confirmed && (p.Provider == "" || p.FailureDomain == "" || p.QuotaPool == "") {
		return &apiError{Status: 400, Code: "INVALID_SUPPLIER", Message: "确认前请填写供应商、失效域和共享额度池；未知关联不要确认"}
	}
	work, err := a.loadAccountWork(r.Context(), id, owner)
	if err != nil {
		return err
	}
	err = a.withSiteSchedulingLock(r.Context(), work.SiteID, func(_ *pgxpool.Conn) error {
		tx, e := a.db.Begin(r.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback(r.Context())
		var generation int64
		if e = tx.QueryRow(r.Context(), `SELECT source_generation FROM upstream_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&generation); e != nil {
			return e
		}
		if generation != input.SourceGeneration {
			return &apiError{Status: 409, Code: "SOURCE_CHANGED", Message: "账号来源已变化，请刷新后重新确认关联"}
		}
		raw, _ := json.Marshal(input.Config)
		if _, e = tx.Exec(r.Context(), `INSERT INTO account_operations(account_id,source_generation,config) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET source_generation=excluded.source_generation,config=excluded.config,updated_at=now()`, id, generation, raw); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `UPDATE upstream_accounts SET config_generation=config_generation+1 WHERE id=$1`, id); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `UPDATE sites SET next_reconcile_at=now() WHERE id=$1`, work.SiteID); e != nil {
			return e
		}
		return tx.Commit(r.Context())
	})
	if err != nil {
		return err
	}
	_ = a.audit(r.Context(), owner, owner, work.SiteID, id, "supplier.update", "success", map[string]any{"confirmed": p.Confirmed})
	writeData(w, 200, map[string]any{"saved": true, "checked_at": time.Now().UTC()})
	return nil
}
