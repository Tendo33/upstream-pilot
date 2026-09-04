package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

type GroupRateRule struct {
	Enabled            bool       `json:"enabled"`
	Mode               string     `json:"mode"`
	Offset             float64    `json:"offset"`
	Expression         *string    `json:"expression,omitempty"`
	LastCalculatedRate *float64   `json:"last_calculated_rate,omitempty"`
	LastAppliedAt      *time.Time `json:"last_applied_at,omitempty"`
	LastError          *string    `json:"last_error,omitempty"`
}

type GroupRateBinding struct {
	ID                   string   `json:"id"`
	AccountID            string   `json:"account_id"`
	AccountName          string   `json:"account_name"`
	SiteID               string   `json:"site_id"`
	SiteName             string   `json:"site_name"`
	Platform             string   `json:"platform"`
	RateMultiplier       *float64 `json:"rate_multiplier,omitempty"`
	SourceRateMultiplier *float64 `json:"source_rate_multiplier,omitempty"`
	Position             int      `json:"position"`
	Available            bool     `json:"available"`
}

type ManagedGroup struct {
	ID             string             `json:"id"`
	SiteID         string             `json:"site_id"`
	SiteName       string             `json:"site_name"`
	RemoteID       int64              `json:"remote_id"`
	Name           string             `json:"name"`
	Platform       *string            `json:"platform,omitempty"`
	Status         *string            `json:"status,omitempty"`
	RateMultiplier *float64           `json:"rate_multiplier,omitempty"`
	MemberCount    int                `json:"member_count"`
	ObservedAt     time.Time          `json:"observed_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	Rule           GroupRateRule      `json:"rule"`
	Bindings       []GroupRateBinding `json:"bindings"`
}

const managedGroupSelect = `
	SELECT g.id,g.site_id,s.name,g.remote_id,g.name,g.platform,g.status,g.rate_multiplier,
	       count(member_account.id),g.observed_at,g.updated_at,
	       COALESCE(rule.enabled,false),COALESCE(rule.mode,'first'),COALESCE(rule.rate_offset,0)::float8,
	       rule.expression,rule.last_calculated_rate,rule.last_applied_at,rule.last_error
	FROM upstream_groups g
	JOIN sites s ON s.id=g.site_id
	LEFT JOIN account_group_memberships m ON m.group_id=g.id
	LEFT JOIN upstream_accounts member_account ON member_account.id=m.account_id AND member_account.deleted_at IS NULL
	LEFT JOIN group_rate_rules rule ON rule.group_id=g.id
`

func (a *App) listGroups(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID := strings.TrimSpace(r.URL.Query().Get("site_id"))
	if siteID != "" {
		if _, err := uuid.Parse(siteID); err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SITE_ID", Message: "站点 ID 无效"}
		}
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	groups, err := a.queryManagedGroups(r.Context(), identity.ID, "", siteID, search)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, groups)
	return nil
}

func (a *App) getGroup(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	groupID, err := requiredID(r, "groupID")
	if err != nil {
		return err
	}
	if _, err := uuid.Parse(groupID); err != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_GROUP_ID", Message: "分组 ID 无效"}
	}
	groups, err := a.queryManagedGroups(r.Context(), identity.ID, groupID, "", "")
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return pgx.ErrNoRows
	}
	writeData(w, http.StatusOK, groups[0])
	return nil
}

func (a *App) queryManagedGroups(ctx context.Context, ownerID, groupID, siteID, search string) ([]ManagedGroup, error) {
	rows, err := a.db.Query(ctx, managedGroupSelect+`
		WHERE s.owner_id=$1 AND g.deleted_at IS NULL
		  AND ($2='' OR g.id=$2::uuid)
		  AND ($3='' OR g.site_id=$3::uuid)
		  AND ($4='' OR g.name ILIKE '%'||$4||'%' OR COALESCE(g.platform,'') ILIKE '%'||$4||'%')
		GROUP BY g.id,s.name,rule.group_id
		ORDER BY s.name,g.name,g.remote_id`, ownerID, groupID, siteID, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]ManagedGroup, 0)
	index := make(map[string]int)
	for rows.Next() {
		var group ManagedGroup
		if err := rows.Scan(
			&group.ID, &group.SiteID, &group.SiteName, &group.RemoteID, &group.Name, &group.Platform, &group.Status,
			&group.RateMultiplier, &group.MemberCount, &group.ObservedAt, &group.UpdatedAt,
			&group.Rule.Enabled, &group.Rule.Mode, &group.Rule.Offset, &group.Rule.Expression,
			&group.Rule.LastCalculatedRate, &group.Rule.LastAppliedAt, &group.Rule.LastError,
		); err != nil {
			return nil, err
		}
		group.Bindings = make([]GroupRateBinding, 0)
		index[group.ID] = len(groups)
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil || len(groups) == 0 {
		return groups, err
	}

	bindingRows, err := a.db.Query(ctx, `
		SELECT binding.id,binding.target_group_id,a.id,a.name,a.site_id,s.name,a.platform,
		       CASE WHEN a.deleted_at IS NULL THEN a.rate_multiplier ELSE NULL END,
		       CASE WHEN a.deleted_at IS NULL THEN a.source_rate_multiplier ELSE NULL END,
		       binding.position,(a.deleted_at IS NULL)
		FROM group_rate_bindings binding
		JOIN upstream_accounts a ON a.id=binding.source_account_id
		JOIN sites s ON s.id=a.site_id
		WHERE binding.owner_id=$1 AND ($2='' OR binding.target_group_id=$2::uuid)
		ORDER BY binding.target_group_id,binding.position`, ownerID, groupID)
	if err != nil {
		return nil, err
	}
	defer bindingRows.Close()
	for bindingRows.Next() {
		var targetID string
		var binding GroupRateBinding
		if err := bindingRows.Scan(
			&binding.ID, &targetID, &binding.AccountID, &binding.AccountName, &binding.SiteID, &binding.SiteName,
			&binding.Platform, &binding.RateMultiplier, &binding.SourceRateMultiplier, &binding.Position, &binding.Available,
		); err != nil {
			return nil, err
		}
		if groupIndex, ok := index[targetID]; ok {
			groups[groupIndex].Bindings = append(groups[groupIndex].Bindings, binding)
		}
	}
	return groups, bindingRows.Err()
}

type groupRateConfigInput struct {
	Enabled    bool     `json:"enabled"`
	Mode       string   `json:"mode"`
	Offset     float64  `json:"offset"`
	Expression *string  `json:"expression"`
	Bindings   []string `json:"bindings"`
	Apply      bool     `json:"apply"`
}

func validateGroupRateConfig(input *groupRateConfigInput) error {
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if input.Mode != "first" && input.Mode != "average" && input.Mode != "min" && input.Mode != "max" && input.Mode != "custom" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RATE_MODE", Message: "倍率规则必须为首个、平均、最低、最高或自定义公式"}
	}
	if !isFinite(input.Offset) || input.Offset < -maxManagedRate || input.Offset > maxManagedRate {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RATE_OFFSET", Message: "倍率偏移必须在 -100000 至 100000 之间"}
	}
	if len(input.Bindings) > 100 {
		return &apiError{Status: http.StatusBadRequest, Code: "TOO_MANY_BINDINGS", Message: "单个分组最多绑定 100 个账号倍率"}
	}
	if input.Enabled && len(input.Bindings) == 0 {
		return &apiError{Status: http.StatusBadRequest, Code: "RATE_BINDING_REQUIRED", Message: "启用倍率规则前必须至少绑定一个账号"}
	}
	if input.Expression != nil {
		trimmed := strings.TrimSpace(*input.Expression)
		input.Expression = nil
		if trimmed != "" {
			if len(trimmed) > 500 {
				return &apiError{Status: http.StatusBadRequest, Code: "FORMULA_TOO_LONG", Message: "自定义公式不能超过 500 个字符"}
			}
			input.Expression = &trimmed
			if err := validateRateExpression(trimmed); err != nil {
				return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RATE_FORMULA", Message: err.Error()}
			}
		}
	}
	if input.Enabled && input.Mode == "custom" && input.Expression == nil {
		return &apiError{Status: http.StatusBadRequest, Code: "RATE_FORMULA_REQUIRED", Message: "启用自定义规则前必须填写公式"}
	}
	return nil
}

func (a *App) updateGroupRateConfig(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	groupID, err := requiredID(r, "groupID")
	if err != nil {
		return err
	}
	if _, err := uuid.Parse(groupID); err != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_GROUP_ID", Message: "分组 ID 无效"}
	}
	var input groupRateConfigInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := validateGroupRateConfig(&input); err != nil {
		return err
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var targetSiteID string
	if err := tx.QueryRow(r.Context(), `
		SELECT g.site_id FROM upstream_groups g JOIN sites s ON s.id=g.site_id
		WHERE g.id=$1 AND s.owner_id=$2 AND g.deleted_at IS NULL FOR UPDATE`, groupID, identity.ID).Scan(&targetSiteID); err != nil {
		return err
	}

	type sourceAccount struct{ ID, SiteID string }
	sources := make([]sourceAccount, 0, len(input.Bindings))
	seen := make(map[string]struct{}, len(input.Bindings))
	for _, rawID := range input.Bindings {
		accountID := strings.TrimSpace(rawID)
		if _, err := uuid.Parse(accountID); err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_ACCOUNT_ID", Message: "绑定账号 ID 无效"}
		}
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		var source sourceAccount
		if err := tx.QueryRow(r.Context(), `
			SELECT a.id,a.site_id FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
			WHERE a.id=$1 AND s.owner_id=$2 AND a.deleted_at IS NULL`, accountID, identity.ID).Scan(&source.ID, &source.SiteID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RATE_SOURCE", Message: "绑定账号不存在或不属于当前用户"}
			}
			return err
		}
		sources = append(sources, source)
	}

	_, err = tx.Exec(r.Context(), `
		INSERT INTO group_rate_rules(group_id,target_site_id,owner_id,enabled,mode,rate_offset,expression,last_error,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,NULL,now())
		ON CONFLICT(group_id) DO UPDATE SET enabled=excluded.enabled,mode=excluded.mode,rate_offset=excluded.rate_offset,
		 expression=excluded.expression,last_error=NULL,updated_at=now()
		WHERE group_rate_rules.owner_id=excluded.owner_id`, groupID, targetSiteID, identity.ID, input.Enabled, input.Mode, input.Offset, input.Expression)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM group_rate_bindings WHERE target_group_id=$1 AND owner_id=$2`, groupID, identity.ID); err != nil {
		return err
	}
	for position, source := range sources {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO group_rate_bindings(id,owner_id,target_group_id,target_site_id,source_account_id,source_site_id,position)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), identity.ID, groupID, targetSiteID, source.ID, source.SiteID, position); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}

	_ = a.audit(r.Context(), identity.ID, identity.ID, targetSiteID, "", "group.rate_rule.update", "success", map[string]any{
		"group_id": groupID, "enabled": input.Enabled, "mode": input.Mode, "offset": input.Offset, "bindings": len(sources),
	})
	if input.Apply && input.Enabled {
		if _, err := a.applyGroupRateRule(r.Context(), groupID, identity.ID, identity.ID); err != nil {
			return &apiError{Status: http.StatusUnprocessableEntity, Code: "GROUP_RATE_APPLY_FAILED", Message: err.Error()}
		}
	}
	groups, err := a.queryManagedGroups(r.Context(), identity.ID, groupID, "", "")
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return pgx.ErrNoRows
	}
	writeData(w, http.StatusOK, groups[0])
	return nil
}

func (a *App) applyGroupRateHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	groupID, err := requiredID(r, "groupID")
	if err != nil {
		return err
	}
	rate, err := a.applyGroupRateRule(r.Context(), groupID, identity.ID, identity.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "GROUP_RATE_APPLY_FAILED", Message: err.Error()}
	}
	groups, err := a.queryManagedGroups(r.Context(), identity.ID, groupID, "", "")
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return pgx.ErrNoRows
	}
	writeData(w, http.StatusOK, map[string]any{"rate_multiplier": rate, "group": groups[0]})
	return nil
}

func (a *App) applyGroupRateRule(ctx context.Context, groupID, ownerFilter, actorID string) (float64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var site SiteSecret
	var groupName string
	var remoteID int64
	var current *float64
	var enabled bool
	var mode string
	var offset float64
	var expression *string
	err = tx.QueryRow(ctx, `
		SELECT s.id,s.owner_id,s.name,s.base_url,s.api_key_ciphertext,s.enabled,
		       g.remote_id,g.name,g.rate_multiplier
		FROM upstream_groups g
		JOIN sites s ON s.id=g.site_id
		WHERE g.id=$1 AND ($2='' OR s.owner_id=$2::uuid) AND g.deleted_at IS NULL
		FOR UPDATE OF g`, groupID, ownerFilter).Scan(
		&site.ID, &site.OwnerID, &site.Name, &site.BaseURL, &site.APIKeyCiphertext, &site.Enabled,
		&remoteID, &groupName, &current,
	)
	if err != nil {
		return 0, err
	}
	err = tx.QueryRow(ctx, `
		SELECT enabled,mode,rate_offset::float8,expression
		FROM group_rate_rules
		WHERE group_id=$1 AND target_site_id=$2 AND owner_id=$3`, groupID, site.ID, site.OwnerID).Scan(
		&enabled, &mode, &offset, &expression,
	)
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, fmt.Errorf("该分组的倍率规则尚未启用")
	}
	rows, err := tx.Query(ctx, `
		SELECT a.rate_multiplier
		FROM group_rate_bindings binding
		JOIN upstream_accounts a ON a.id=binding.source_account_id AND a.site_id=binding.source_site_id
		WHERE binding.target_group_id=$1 AND binding.owner_id=$2 AND a.deleted_at IS NULL AND a.rate_multiplier IS NOT NULL
		ORDER BY binding.position`, groupID, site.OwnerID)
	if err != nil {
		return 0, err
	}
	rates := make([]float64, 0)
	for rows.Next() {
		var rate float64
		if err := rows.Scan(&rate); err != nil {
			rows.Close()
			return 0, err
		}
		rates = append(rates, rate)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	currentRate := 0.0
	if current != nil {
		currentRate = *current
	}
	formula := ""
	if expression != nil {
		formula = *expression
	}
	targetRate, err := calculateManagedRate(mode, offset, formula, currentRate, rates)
	if err != nil {
		_ = tx.Rollback(ctx)
		a.recordGroupRuleFailure(ctx, site, groupID, groupName, err)
		return 0, err
	}
	if current != nil && math.Abs(*current-targetRate) <= 1e-7 {
		if _, err := tx.Exec(ctx, `
			UPDATE group_rate_rules
			SET last_calculated_rate=$2,last_applied_at=now(),last_error=NULL,updated_at=now()
			WHERE group_id=$1`, groupID, targetRate); err != nil {
			return 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return targetRate, nil
	}
	client, err := a.sub2Client(site)
	if err != nil {
		_ = tx.Rollback(ctx)
		a.recordGroupRuleFailure(ctx, site, groupID, groupName, err)
		return 0, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	remote, err := client.UpdateGroup(requestCtx, remoteID, upstream.GroupUpdate{RateMultiplier: &targetRate})
	cancel()
	if err != nil {
		_ = tx.Rollback(ctx)
		a.recordGroupRuleFailure(ctx, site, groupID, groupName, err)
		return 0, err
	}
	if remote.RateMultiplier == nil || math.Abs(*remote.RateMultiplier-targetRate) > 1e-7 {
		err = errors.New("Sub2API 未持久化分组倍率")
		_ = tx.Rollback(ctx)
		a.recordGroupRuleFailure(ctx, site, groupID, groupName, err)
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE upstream_groups SET rate_multiplier=$2,updated_at=now() WHERE id=$1`, groupID, targetRate)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE group_rate_rules SET last_calculated_rate=$2,last_applied_at=now(),last_error=NULL,updated_at=now() WHERE group_id=$1`, groupID, targetRate); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, site.ID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	_ = a.audit(ctx, site.OwnerID, actorID, site.ID, "", "group.rate_rule.apply", "success", map[string]any{
		"group_id": groupID, "group_name": groupName, "rate": targetRate, "mode": mode, "offset": offset, "sources": len(rates),
	})
	return targetRate, nil
}

func (a *App) recordGroupRuleFailure(ctx context.Context, site SiteSecret, groupID, groupName string, cause error) {
	message := truncateError(cause)
	_, _ = a.db.Exec(ctx, `UPDATE group_rate_rules SET last_error=$2,updated_at=now() WHERE group_id=$1`, groupID, message)
	_ = a.audit(ctx, site.OwnerID, "", site.ID, "", "group.rate_rule.apply", "failed", map[string]any{
		"group_id": groupID, "group_name": groupName, "error": message,
	})
}

func (a *App) applyEnabledGroupRulesForSourceAccount(ctx context.Context, accountID string) {
	rows, err := a.db.Query(ctx, `
		SELECT DISTINCT rule.group_id,rule.owner_id
		FROM group_rate_rules rule
		JOIN group_rate_bindings binding ON binding.target_group_id=rule.group_id AND binding.owner_id=rule.owner_id
		JOIN upstream_groups target ON target.id=rule.group_id AND target.site_id=rule.target_site_id
		WHERE rule.enabled AND binding.source_account_id=$1 AND target.deleted_at IS NULL`, accountID)
	if err != nil {
		a.logger.Warn("load dependent group rate rules failed", "account_id", accountID, "error", err)
		return
	}
	type target struct{ groupID, ownerID string }
	targets := make([]target, 0)
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.groupID, &item.ownerID); err != nil {
			rows.Close()
			a.logger.Warn("scan dependent group rate rule failed", "account_id", accountID, "error", err)
			return
		}
		targets = append(targets, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		a.logger.Warn("iterate dependent group rate rules failed", "account_id", accountID, "error", err)
		return
	}
	for _, item := range targets {
		if _, err := a.applyGroupRateRule(ctx, item.groupID, item.ownerID, ""); err != nil && ctx.Err() == nil {
			a.logger.Warn("dependent group rate rule failed", "group_id", item.groupID, "error", err)
		}
	}
}

func (a *App) applyEnabledGroupRulesForSite(ctx context.Context, siteID string) {
	rows, err := a.db.Query(ctx, `
		SELECT DISTINCT rule.group_id,rule.owner_id
		FROM group_rate_rules rule
		JOIN upstream_groups target ON target.id=rule.group_id AND target.site_id=rule.target_site_id
		WHERE rule.enabled AND (rule.target_site_id=$1 OR EXISTS(
		  SELECT 1 FROM group_rate_bindings binding
		  WHERE binding.target_group_id=rule.group_id AND binding.source_site_id=$1
		)) AND target.deleted_at IS NULL`, siteID)
	if err != nil {
		a.logger.Warn("load site group rate rules failed", "site_id", siteID, "error", err)
		return
	}
	type target struct{ groupID, ownerID string }
	targets := make([]target, 0)
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.groupID, &item.ownerID); err != nil {
			rows.Close()
			a.logger.Warn("scan site group rate rule failed", "site_id", siteID, "error", err)
			return
		}
		targets = append(targets, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		a.logger.Warn("iterate site group rate rules failed", "site_id", siteID, "error", err)
		return
	}
	for _, item := range targets {
		if _, err := a.applyGroupRateRule(ctx, item.groupID, item.ownerID, ""); err != nil && ctx.Err() == nil {
			a.logger.Warn("site group rate rule failed", "group_id", item.groupID, "error", err)
		}
	}
}
