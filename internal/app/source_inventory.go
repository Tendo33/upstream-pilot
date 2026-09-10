package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/upstream"
)

type sourceAccountRow struct {
	ID                     int64
	Name                   string
	Platform               string
	Type                   string
	Status                 string
	Schedulable            bool
	Priority               int
	Concurrency            int
	LoadFactor             *int
	RateMultiplier         *float64
	UpdatedAt              *time.Time
	Extra                  []byte
	RateLimitResetAt       *time.Time
	OverloadUntil          *time.Time
	TempUnschedulableUntil *time.Time
	ExpiresAt              *time.Time
	AutoPauseOnExpired     bool
	ParentAccountID        *int64
}

func (a *App) loadSourceInventory(ctx context.Context) ([]upstream.Sub2Group, []upstream.Sub2Account, error) {
	if a.sourceDB == nil {
		return nil, nil, &apiError{Status: 503, Code: "SOURCE_DATABASE_REQUIRED", Message: "请配置 PILOT_SUB2API_DATABASE_URL，连接 Sub2API 只读数据库"}
	}
	groups, groupByID, err := a.loadSourceGroups(ctx)
	if err != nil {
		return nil, nil, sourceCapabilityError(err, "Sub2API 数据库缺少 groups 表，无法同步库存")
	}
	rows, err := a.sourceDB.Query(ctx, `
		SELECT id,name,platform,type,status,schedulable,priority,concurrency,load_factor,rate_multiplier,updated_at,
		 COALESCE(extra,'{}'::jsonb),rate_limit_reset_at,overload_until,temp_unschedulable_until,expires_at,auto_pause_on_expired,parent_account_id
		 FROM accounts WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, sourceCapabilityError(err, "Sub2API 数据库缺少 accounts 表，无法同步库存")
	}
	defer rows.Close()
	sourceRows := make([]sourceAccountRow, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var row sourceAccountRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Platform, &row.Type, &row.Status, &row.Schedulable, &row.Priority, &row.Concurrency, &row.LoadFactor, &row.RateMultiplier, &row.UpdatedAt, &row.Extra, &row.RateLimitResetAt, &row.OverloadUntil, &row.TempUnschedulableUntil, &row.ExpiresAt, &row.AutoPauseOnExpired, &row.ParentAccountID); err != nil {
			return nil, nil, err
		}
		sourceRows = append(sourceRows, row)
		ids = append(ids, row.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	memberships, err := a.loadSourceMemberships(ctx, ids)
	if err != nil {
		return nil, nil, sourceCapabilityError(err, "Sub2API 数据库缺少 account_groups 表，无法同步库存")
	}
	accounts := make([]upstream.Sub2Account, 0, len(sourceRows))
	for _, row := range sourceRows {
		account, err := accountFromSourceRow(row, memberships[row.ID], groupByID)
		if err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, account)
	}
	return groups, accounts, nil
}

func (a *App) loadSourceGroups(ctx context.Context) ([]upstream.Sub2Group, map[int64]upstream.Sub2Group, error) {
	rows, err := a.sourceDB.Query(ctx, `SELECT id,name,COALESCE(platform,''),status,rate_multiplier FROM groups WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	groups := make([]upstream.Sub2Group, 0)
	byID := make(map[int64]upstream.Sub2Group)
	for rows.Next() {
		var group upstream.Sub2Group
		var status *string
		if err := rows.Scan(&group.ID, &group.Name, &group.Platform, &status, &group.RateMultiplier); err != nil {
			return nil, nil, err
		}
		if status != nil {
			group.Status = *status
		}
		groups = append(groups, group)
		byID[group.ID] = group
	}
	return groups, byID, rows.Err()
}

func (a *App) loadSourceMemberships(ctx context.Context, accountIDs []int64) (map[int64][]upstream.Sub2AccountGroup, error) {
	result := make(map[int64][]upstream.Sub2AccountGroup, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := a.sourceDB.Query(ctx, `SELECT account_id,group_id,priority FROM account_groups WHERE account_id=ANY($1) ORDER BY account_id,priority,group_id`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, groupID int64
		var priority int
		if err := rows.Scan(&accountID, &groupID, &priority); err != nil {
			return nil, err
		}
		priorityCopy := priority
		result[accountID] = append(result[accountID], upstream.Sub2AccountGroup{GroupID: groupID, Priority: &priorityCopy})
	}
	return result, rows.Err()
}

func accountFromSourceRow(row sourceAccountRow, memberships []upstream.Sub2AccountGroup, groups map[int64]upstream.Sub2Group) (upstream.Sub2Account, error) {
	var extra any
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		extra = map[string]any{}
	}
	attached := make([]map[string]any, 0, len(memberships))
	for _, membership := range memberships {
		item := map[string]any{"group_id": membership.GroupID, "priority": membership.Priority}
		if group, ok := groups[membership.GroupID]; ok {
			item["group"] = group
		}
		attached = append(attached, item)
	}
	var expires any
	if row.ExpiresAt != nil {
		expires = row.ExpiresAt.Unix()
	}
	payload := map[string]any{
		"id":                       row.ID,
		"name":                     row.Name,
		"platform":                 row.Platform,
		"type":                     row.Type,
		"status":                   row.Status,
		"schedulable":              row.Schedulable,
		"priority":                 row.Priority,
		"concurrency":              row.Concurrency,
		"load_factor":              row.LoadFactor,
		"rate_multiplier":          row.RateMultiplier,
		"updated_at":               row.UpdatedAt,
		"extra":                    extra,
		"rate_limit_reset_at":      row.RateLimitResetAt,
		"overload_until":           row.OverloadUntil,
		"temp_unschedulable_until": row.TempUnschedulableUntil,
		"expires_at":               expires,
		"auto_pause_on_expired":    row.AutoPauseOnExpired,
		"parent_account_id":        row.ParentAccountID,
		"account_groups":           attached,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return upstream.Sub2Account{}, fmt.Errorf("encode source account %d: %w", row.ID, err)
	}
	var account upstream.Sub2Account
	if err := json.Unmarshal(raw, &account); err != nil {
		return upstream.Sub2Account{}, fmt.Errorf("decode source account %d: %w", row.ID, err)
	}
	return account, nil
}
