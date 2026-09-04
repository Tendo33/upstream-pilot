package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/langrenjh-alt/S2AM-GO/internal/health"
	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

const accountUptimeWindowSize = 60

const maxBulkAccountIDs = 2000

var accountSelect = fmt.Sprintf(`
	SELECT a.id,a.site_id,s.name,a.remote_id,a.name,a.platform,a.account_type,a.remote_status,a.schedulable,a.priority,a.rate_multiplier,
	       a.health_enabled,a.probe_interval_seconds,a.probe_timeout_seconds,a.failure_threshold,a.recovery_success_threshold,a.probe_model,
	       a.rate_sync_enabled,a.rate_sync_interval_seconds,a.source_type,a.source_base_url,a.observed_source_base_url,a.source_type_locked,(a.source_credential_ciphertext IS NOT NULL),
	       a.source_credential_state,a.source_credential_checked_at,
	       a.source_user_id,a.source_group,a.recharge_ratio,a.source_rate_multiplier,a.source_rate_endpoint,
	       a.priority_enabled,a.guard_enabled,a.guard_operator,a.guard_priority,a.guard_holding,
	       a.health_state,a.consecutive_failures,a.consecutive_recovery_successes,a.managed_hold,a.last_probe_at,a.last_probe_latency_ms,a.last_success_at,a.last_failure_at,
	       a.last_failure_reason,a.last_failure_http_status,a.last_rate_sync_at,a.last_error,
	       a.cache_rate,a.cache_rate_tokens,a.cache_rate_sampled_at,
	       COALESCE(uptime.successes,0),COALESCE(uptime.total,0),uptime.window_started_at,uptime.window_ended_at,
	       COALESCE(uptime.timeline,''),
	       COALESCE((SELECT jsonb_agg(jsonb_build_object('id',g.id,'remote_id',g.remote_id,'name',g.name,'rate_multiplier',g.rate_multiplier,'priority',m.group_priority) ORDER BY g.name)
	                 FROM account_group_memberships m JOIN upstream_groups g ON g.id=m.group_id WHERE m.account_id=a.id AND g.deleted_at IS NULL),'[]'::jsonb)
	FROM upstream_accounts a
	JOIN sites s ON s.id=a.site_id
	LEFT JOIN LATERAL (
		SELECT COUNT(*) FILTER (WHERE recent.success)::int AS successes,
		       COUNT(*)::int AS total,
		       MIN(recent.created_at) AS window_started_at,
		       MAX(recent.created_at) AS window_ended_at,
		       string_agg(CASE WHEN recent.success THEN 'S' ELSE 'F' END,'' ORDER BY recent.created_at DESC,recent.id DESC) AS timeline
		FROM (
			SELECT attempts.id,attempts.success,attempts.created_at
			FROM probe_attempts attempts
			WHERE attempts.account_id=a.id
			ORDER BY attempts.created_at DESC,attempts.id DESC
			LIMIT %d
		) recent
	) uptime ON true`, accountUptimeWindowSize)

type rowScanner interface{ Scan(...any) error }

func scanAccount(row rowScanner) (Account, error) {
	var account Account
	var groups []byte
	err := row.Scan(
		&account.ID, &account.SiteID, &account.SiteName, &account.RemoteID, &account.Name, &account.Platform, &account.AccountType, &account.RemoteStatus,
		&account.Schedulable, &account.Priority, &account.RateMultiplier, &account.HealthEnabled, &account.ProbeIntervalSeconds, &account.ProbeTimeoutSeconds,
		&account.FailureThreshold, &account.RecoverySuccessThreshold, &account.ProbeModel, &account.RateSyncEnabled, &account.RateSyncIntervalSeconds, &account.SourceType,
		&account.SourceBaseURL, &account.ObservedSourceBaseURL, &account.SourceTypeLocked, &account.SourceCredentialSet,
		&account.SourceCredentialState, &account.SourceCredentialCheckedAt, &account.SourceUserID, &account.SourceGroup, &account.RechargeRatio,
		&account.SourceRateMultiplier, &account.SourceRateEndpoint, &account.PriorityEnabled, &account.GuardEnabled, &account.GuardOperator,
		&account.GuardPriority, &account.GuardHolding, &account.HealthState, &account.ConsecutiveFailures, &account.ConsecutiveRecoverySuccesses, &account.ManagedHold,
		&account.LastProbeAt, &account.LastProbeLatencyMS, &account.LastSuccessAt, &account.LastFailureAt, &account.LastFailureReason,
		&account.LastFailureHTTPStatus, &account.LastRateSyncAt, &account.LastError,
		&account.CacheRate, &account.CacheRateTokens, &account.CacheRateSampledAt,
		&account.UptimeSuccesses, &account.UptimeTotal, &account.UptimeWindowStartedAt, &account.UptimeWindowEndedAt,
		&account.UptimeTimeline, &groups,
	)
	if err != nil {
		return Account{}, err
	}
	account.Groups = make([]GroupSummary, 0)
	if len(groups) > 0 {
		_ = json.Unmarshal(groups, &account.Groups)
	}
	account.UptimeWindowSize = accountUptimeWindowSize
	account.UptimePercent = calculateUptimePercent(account.UptimeSuccesses, account.UptimeTotal)
	return account, nil
}

func calculateUptimePercent(successes, total int) *float64 {
	if total <= 0 {
		return nil
	}
	percent := math.Round(float64(successes)*1000/float64(total)) / 10
	return &percent
}

func (a *App) listAccounts(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID := strings.TrimSpace(r.URL.Query().Get("site_id"))
	if siteID != "" {
		if _, err := uuid.Parse(siteID); err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SITE_ID", Message: "站点 ID 无效"}
		}
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	if len(platform) > 100 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PLATFORM", Message: "平台筛选值过长"}
	}
	groupID := strings.TrimSpace(r.URL.Query().Get("group_id"))
	if groupID != "" {
		if _, err := uuid.Parse(groupID); err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_GROUP_ID", Message: "分组 ID 无效"}
		}
	}
	if state != "" && state != "all" && state != "healthy" && state != "failing" && state != "paused" && state != "unknown" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_STATE", Message: "账号状态筛选值无效"}
	}
	rows, err := a.db.Query(r.Context(), accountSelect+`
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL
		  AND ($2='' OR a.site_id=$2::uuid)
		  AND ($3='' OR a.name ILIKE '%'||$3||'%' OR a.platform ILIKE '%'||$3||'%' OR a.remote_id::text=$3)
		  AND ($4='' OR $4='all' OR a.health_state=$4)
		  AND ($5='' OR lower(btrim(a.platform))=lower(btrim($5)))
		  AND ($6='' OR EXISTS(SELECT 1 FROM account_group_memberships membership WHERE membership.account_id=a.id AND membership.group_id=$6::uuid))
		ORDER BY s.name,a.priority,a.name LIMIT 2000`, identity.ID, siteID, search, state, platform, groupID)
	if err != nil {
		return err
	}
	defer rows.Close()
	accounts := make([]Account, 0)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return err
		}
		accounts = append(accounts, account)
	}
	writeData(w, http.StatusOK, accounts)
	return rows.Err()
}

func (a *App) accountFilterOptions(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	platformRows, err := a.db.Query(r.Context(), `
		SELECT DISTINCT lower(btrim(a.platform)) AS platform FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL AND btrim(a.platform)<>''
		ORDER BY platform`, identity.ID)
	if err != nil {
		return err
	}
	platforms := make([]string, 0)
	for platformRows.Next() {
		var platform string
		if err := platformRows.Scan(&platform); err != nil {
			platformRows.Close()
			return err
		}
		platforms = append(platforms, platform)
	}
	platformRows.Close()
	if err := platformRows.Err(); err != nil {
		return err
	}

	type filterGroup struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		SiteID   string `json:"site_id"`
		SiteName string `json:"site_name"`
	}
	groupRows, err := a.db.Query(r.Context(), `
		SELECT g.id,g.name,g.site_id,s.name FROM upstream_groups g JOIN sites s ON s.id=g.site_id
		WHERE s.owner_id=$1 AND g.deleted_at IS NULL ORDER BY s.name,g.name,g.remote_id`, identity.ID)
	if err != nil {
		return err
	}
	defer groupRows.Close()
	groups := make([]filterGroup, 0)
	for groupRows.Next() {
		var group filterGroup
		if err := groupRows.Scan(&group.ID, &group.Name, &group.SiteID, &group.SiteName); err != nil {
			return err
		}
		groups = append(groups, group)
	}
	if err := groupRows.Err(); err != nil {
		return err
	}
	var invalidSourceCredentials int
	if err := a.db.QueryRow(r.Context(), `
		SELECT count(*) FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL AND a.source_type='newapi' AND a.source_credential_state='invalid'`, identity.ID).Scan(&invalidSourceCredentials); err != nil {
		return err
	}
	writeData(w, http.StatusOK, map[string]any{"platforms": platforms, "groups": groups, "invalid_source_credentials": invalidSourceCredentials})
	return nil
}

func (a *App) getAccount(ctx context.Context, accountID, ownerID string) (Account, error) {
	return scanAccount(a.db.QueryRow(ctx, accountSelect+` WHERE a.id=$1 AND s.owner_id=$2 AND a.deleted_at IS NULL`, accountID, ownerID))
}

type accountSettingsInput struct {
	HealthEnabled            bool    `json:"health_enabled"`
	ProbeIntervalSeconds     int     `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds      int     `json:"probe_timeout_seconds"`
	FailureThreshold         int     `json:"failure_threshold"`
	RecoverySuccessThreshold *int    `json:"recovery_success_threshold"`
	ProbeModel               string  `json:"probe_model"`
	RateSyncEnabled          bool    `json:"rate_sync_enabled"`
	RateSyncIntervalSeconds  int     `json:"rate_sync_interval_seconds"`
	SourceType               string  `json:"source_type"`
	SourceTypeLocked         bool    `json:"source_type_locked"`
	SourceBaseURL            string  `json:"source_base_url"`
	SourceCredential         string  `json:"source_credential"`
	ClearSourceCredential    bool    `json:"clear_source_credential"`
	SourceUserID             string  `json:"source_user_id"`
	SourceGroup              string  `json:"source_group"`
	RechargeRatio            float64 `json:"recharge_ratio"`
	PriorityEnabled          bool    `json:"priority_enabled"`
	GuardEnabled             bool    `json:"guard_enabled"`
	GuardOperator            string  `json:"guard_operator"`
	GuardPriority            int     `json:"guard_priority"`
}

type bulkAccountHealthSettings struct {
	Enabled                  bool   `json:"enabled"`
	ProbeIntervalSeconds     int    `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds      int    `json:"probe_timeout_seconds"`
	FailureThreshold         int    `json:"failure_threshold"`
	RecoverySuccessThreshold int    `json:"recovery_success_threshold"`
	ProbeModel               string `json:"probe_model"`
}

type bulkAccountRateSyncSettings struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
}

type bulkAccountPrioritySettings struct {
	Enabled bool `json:"enabled"`
}

type bulkAccountGuardSettings struct {
	Enabled  bool   `json:"enabled"`
	Operator string `json:"operator"`
	Priority int    `json:"priority"`
}

type bulkAccountSettingsInput struct {
	AccountIDs []string                     `json:"account_ids"`
	Health     *bulkAccountHealthSettings   `json:"health,omitempty"`
	RateSync   *bulkAccountRateSyncSettings `json:"rate_sync,omitempty"`
	Priority   *bulkAccountPrioritySettings `json:"priority,omitempty"`
	Guard      *bulkAccountGuardSettings    `json:"guard,omitempty"`
}

type bulkAccountSettingsResult struct {
	UpdatedCount int       `json:"updated_count"`
	Accounts     []Account `json:"accounts"`
}

type bulkAccountSettingsWork struct {
	ID                  string
	SiteID              string
	SourceType          string
	SourceBaseURL       *string
	SourceCredentialSet bool
	SourceGroup         *string
}

func validateAccountSettings(input *accountSettingsInput) error {
	input.ProbeModel = strings.TrimSpace(input.ProbeModel)
	input.SourceType = strings.ToLower(strings.TrimSpace(input.SourceType))
	input.SourceBaseURL = strings.TrimSpace(input.SourceBaseURL)
	input.SourceCredential = strings.TrimSpace(input.SourceCredential)
	input.SourceUserID = strings.TrimSpace(input.SourceUserID)
	input.SourceGroup = strings.TrimSpace(input.SourceGroup)
	input.GuardOperator = strings.ToLower(strings.TrimSpace(input.GuardOperator))
	if err := validateAccountHealthSettings(input.ProbeIntervalSeconds, input.ProbeTimeoutSeconds, input.FailureThreshold, optionalRecoverySuccessThreshold(input.RecoverySuccessThreshold), input.ProbeModel); err != nil {
		return err
	}
	if err := validateAccountRateSyncInterval(input.RateSyncIntervalSeconds); err != nil {
		return err
	}
	if input.SourceType != "sub2api" && input.SourceType != "newapi" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SOURCE_TYPE", Message: "倍率源类型必须为 Sub2API 或 NewAPI"}
	}
	if input.SourceType == "newapi" && input.SourceBaseURL != "" {
		normalized, err := upstream.NormalizeBaseURL(input.SourceBaseURL)
		if err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SOURCE_URL", Message: "NewAPI 源站地址无效"}
		}
		input.SourceBaseURL = normalized
	}
	if !isFinite(input.RechargeRatio) || input.RechargeRatio <= 0 || input.RechargeRatio > 1_000_000 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RECHARGE_RATIO", Message: "充值倍率必须大于 0"}
	}
	return validateAccountGuardSettings(input.GuardOperator, input.GuardPriority)
}

func optionalRecoverySuccessThreshold(value *int) int {
	if value == nil {
		return 1
	}
	return *value
}

func validateAccountHealthSettings(probeIntervalSeconds, probeTimeoutSeconds, failureThreshold, recoverySuccessThreshold int, probeModel string) error {
	if probeIntervalSeconds < 10 || probeIntervalSeconds > 86400 || probeTimeoutSeconds < 3 || probeTimeoutSeconds > 600 || failureThreshold < 1 || failureThreshold > 100 || recoverySuccessThreshold < 1 || recoverySuccessThreshold > 100 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_PROBE_SETTINGS", Message: "探测间隔、超时、失败阈值或恢复成功阈值超出范围"}
	}
	if len(probeModel) > 200 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_MODEL", Message: "探测模型名称过长"}
	}
	return nil
}

func validateAccountRateSyncInterval(intervalSeconds int) error {
	if intervalSeconds < 30 || intervalSeconds > 604800 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_RATE_INTERVAL", Message: "倍率同步间隔必须为 30 秒到 7 天"}
	}
	return nil
}

func validateAccountGuardSettings(operator string, priority int) error {
	if operator != "gt" && operator != "gte" {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_GUARD_OPERATOR", Message: "分组保护比较方式无效"}
	}
	if priority < 0 || priority > 1_000_000 {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_GUARD_PRIORITY", Message: "分组保护目标优先级超出范围"}
	}
	return nil
}

func normalizeBulkAccountIDs(raw []string) ([]string, *apiError) {
	if len(raw) == 0 {
		return nil, &apiError{Status: http.StatusBadRequest, Code: "ACCOUNT_IDS_REQUIRED", Message: "请至少选择一个账号"}
	}
	if len(raw) > maxBulkAccountIDs {
		return nil, &apiError{Status: http.StatusBadRequest, Code: "TOO_MANY_ACCOUNTS", Message: "单次最多批量编辑 2000 个账号"}
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, &apiError{Status: http.StatusBadRequest, Code: "INVALID_ACCOUNT_ID", Message: "账号 ID 无效"}
		}
		accountID := parsed.String()
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		result = append(result, accountID)
	}
	return result, nil
}

func validateBulkAccountSettings(input *bulkAccountSettingsInput) error {
	if input.Health == nil && input.RateSync == nil && input.Priority == nil && input.Guard == nil {
		return &apiError{Status: http.StatusBadRequest, Code: "SETTINGS_REQUIRED", Message: "请至少勾选一类要批量修改的设置"}
	}
	if input.Health != nil {
		input.Health.ProbeModel = strings.TrimSpace(input.Health.ProbeModel)
		if err := validateAccountHealthSettings(
			input.Health.ProbeIntervalSeconds,
			input.Health.ProbeTimeoutSeconds,
			input.Health.FailureThreshold,
			input.Health.RecoverySuccessThreshold,
			input.Health.ProbeModel,
		); err != nil {
			return err
		}
	}
	if input.RateSync != nil {
		if err := validateAccountRateSyncInterval(input.RateSync.IntervalSeconds); err != nil {
			return err
		}
	}
	if input.Guard != nil {
		input.Guard.Operator = strings.ToLower(strings.TrimSpace(input.Guard.Operator))
		if err := validateAccountGuardSettings(input.Guard.Operator, input.Guard.Priority); err != nil {
			return err
		}
	}
	return nil
}

func validateBulkAccountRateSync(work bulkAccountSettingsWork, enabled bool) error {
	if !enabled || work.SourceType != "newapi" {
		return nil
	}
	if work.SourceBaseURL == nil || strings.TrimSpace(*work.SourceBaseURL) == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_URL_REQUIRED", Message: "开启 NewAPI 倍率同步前必须填写源站地址"}
	}
	if work.SourceGroup == nil || strings.TrimSpace(*work.SourceGroup) == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_GROUP_REQUIRED", Message: "开启 NewAPI 倍率同步前必须绑定账号实际使用的分组"}
	}
	if !work.SourceCredentialSet {
		return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_CREDENTIAL_REQUIRED", Message: "开启 NewAPI 倍率同步前必须填写源站凭据"}
	}
	return nil
}

func (a *App) updateAccountSettings(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	var input accountSettingsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := validateAccountSettings(&input); err != nil {
		return err
	}
	current, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	if input.SourceType == "newapi" && input.RateSyncEnabled {
		if input.SourceBaseURL == "" {
			return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_URL_REQUIRED", Message: "开启 NewAPI 倍率同步前必须填写源站地址"}
		}
		if input.SourceGroup == "" {
			return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_GROUP_REQUIRED", Message: "开启 NewAPI 倍率同步前必须绑定账号实际使用的分组"}
		}
		if input.ClearSourceCredential || (input.SourceCredential == "" && !current.SourceCredentialSet) {
			return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_CREDENTIAL_REQUIRED", Message: "开启 NewAPI 倍率同步前必须填写源站凭据"}
		}
	}
	credentialCiphertext := ""
	if input.SourceCredential != "" {
		credentialCiphertext, err = a.cipher.Encrypt(input.SourceCredential, "account:"+accountID)
		if err != nil {
			return err
		}
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), accountSchedulingOperationTimeout)
	defer cancel()
	var rowsAffected int64
	err = a.withAccountSchedulingLock(requestCtx, accountID, func(connection *pgxpool.Conn) error {
		command, updateErr := connection.Exec(requestCtx, `
		UPDATE upstream_accounts a SET
		 health_enabled=$3,probe_interval_seconds=$4,probe_timeout_seconds=$5,failure_threshold=$6,
		 recovery_success_threshold=COALESCE($7,recovery_success_threshold),
		 scheduling_generation=scheduling_generation+CASE WHEN $7 IS NOT NULL AND recovery_success_threshold IS DISTINCT FROM $7 THEN 1 ELSE 0 END,
		 consecutive_recovery_successes=CASE WHEN $7 IS NOT NULL AND recovery_success_threshold IS DISTINCT FROM $7 THEN 0 ELSE consecutive_recovery_successes END,
		 probe_model=NULLIF($8,''),
		 rate_sync_enabled=$9,rate_sync_interval_seconds=$10,source_type=$11,
		 source_type_locked=source_type_locked OR $12 OR ($11='newapi' AND ($13<>'' OR $14<>'' OR $16<>'' OR $17<>'')),
		 source_credential_state=CASE
		   WHEN $11='sub2api' OR $15 OR source_type IS DISTINCT FROM $11 OR source_base_url IS DISTINCT FROM NULLIF($13,'') OR $14<>'' OR source_user_id IS DISTINCT FROM NULLIF($16,'') THEN 'unknown'
		   ELSE source_credential_state
		 END,
		 source_credential_checked_at=CASE
		   WHEN $11='sub2api' OR $15 OR source_type IS DISTINCT FROM $11 OR source_base_url IS DISTINCT FROM NULLIF($13,'') OR $14<>'' OR source_user_id IS DISTINCT FROM NULLIF($16,'') THEN NULL
		   ELSE source_credential_checked_at
		 END,
		 source_base_url=CASE WHEN $11='sub2api' THEN NULL ELSE NULLIF($13,'') END,
		 source_credential_ciphertext=CASE WHEN $11='sub2api' OR $15 THEN NULL WHEN $14<>'' THEN $14 ELSE source_credential_ciphertext END,
		 source_user_id=CASE WHEN $11='sub2api' THEN NULL ELSE NULLIF($16,'') END,
		 source_group=CASE WHEN $11='sub2api' THEN NULL ELSE NULLIF($17,'') END,recharge_ratio=$18,
		 priority_enabled=$19,guard_enabled=$20,guard_operator=$21,guard_priority=$22,
		 next_probe_at=LEAST(next_probe_at,now()),next_rate_sync_at=LEAST(next_rate_sync_at,now()),updated_at=now()
		FROM sites s WHERE a.id=$1 AND a.site_id=s.id AND s.owner_id=$2 AND a.deleted_at IS NULL`,
			accountID, identity.ID, input.HealthEnabled, input.ProbeIntervalSeconds, input.ProbeTimeoutSeconds, input.FailureThreshold, input.RecoverySuccessThreshold, input.ProbeModel,
			input.RateSyncEnabled, input.RateSyncIntervalSeconds, input.SourceType, input.SourceTypeLocked, input.SourceBaseURL, credentialCiphertext, input.ClearSourceCredential,
			input.SourceUserID, input.SourceGroup, input.RechargeRatio, input.PriorityEnabled, input.GuardEnabled, input.GuardOperator, input.GuardPriority)
		rowsAffected = command.RowsAffected()
		return updateErr
	})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return pgx.ErrNoRows
	}
	account, err := a.getAccount(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	a.requestBalanceRefresh()
	_, _ = a.db.Exec(r.Context(), `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, account.SiteID)
	_ = a.audit(r.Context(), identity.ID, identity.ID, account.SiteID, accountID, "account.settings.update", "success", map[string]any{
		"health": input.HealthEnabled, "rate_sync": input.RateSyncEnabled, "priority": input.PriorityEnabled, "guard": input.GuardEnabled,
	})
	writeData(w, http.StatusOK, account)
	return nil
}

func (a *App) bulkUpdateAccountSettings(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var input bulkAccountSettingsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	accountIDs, apiErr := normalizeBulkAccountIDs(input.AccountIDs)
	if apiErr != nil {
		return apiErr
	}
	if err := validateBulkAccountSettings(&input); err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if input.Health != nil {
		lockAccountIDs := append([]string(nil), accountIDs...)
		sort.Strings(lockAccountIDs)
		for _, accountID := range lockAccountIDs {
			first, second, err := accountSchedulingLockKeys(accountID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,$2)`, first, second); err != nil {
				return err
			}
		}
	}

	accountIDList := strings.Join(accountIDs, ",")
	rows, err := tx.Query(ctx, `
		SELECT a.id::text,a.site_id::text,a.source_type,a.source_base_url,
		       (a.source_credential_ciphertext IS NOT NULL),a.source_group
		FROM upstream_accounts a
		JOIN sites s ON s.id=a.site_id
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL
		  AND a.id=ANY(string_to_array($2, ',')::uuid[])
		ORDER BY a.id
		FOR UPDATE OF a`, identity.ID, accountIDList)
	if err != nil {
		return err
	}
	works := make(map[string]bulkAccountSettingsWork, len(accountIDs))
	for rows.Next() {
		var work bulkAccountSettingsWork
		if err := rows.Scan(&work.ID, &work.SiteID, &work.SourceType, &work.SourceBaseURL, &work.SourceCredentialSet, &work.SourceGroup); err != nil {
			rows.Close()
			return err
		}
		works[work.ID] = work
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(works) != len(accountIDs) {
		return pgx.ErrNoRows
	}
	if input.RateSync != nil {
		for _, accountID := range accountIDs {
			if err := validateBulkAccountRateSync(works[accountID], input.RateSync.Enabled); err != nil {
				return err
			}
		}
	}

	var health bulkAccountHealthSettings
	if input.Health != nil {
		health = *input.Health
	}
	var rateSync bulkAccountRateSyncSettings
	if input.RateSync != nil {
		rateSync = *input.RateSync
	}
	var priority bulkAccountPrioritySettings
	if input.Priority != nil {
		priority = *input.Priority
	}
	var guard bulkAccountGuardSettings
	if input.Guard != nil {
		guard = *input.Guard
	}
	command, err := tx.Exec(ctx, `
		UPDATE upstream_accounts a SET
		 health_enabled=CASE WHEN $3 THEN $4 ELSE a.health_enabled END,
		 probe_interval_seconds=CASE WHEN $3 THEN $5 ELSE a.probe_interval_seconds END,
		 probe_timeout_seconds=CASE WHEN $3 THEN $6 ELSE a.probe_timeout_seconds END,
		 failure_threshold=CASE WHEN $3 THEN $7 ELSE a.failure_threshold END,
		 recovery_success_threshold=CASE WHEN $3 THEN $8 ELSE a.recovery_success_threshold END,
		 scheduling_generation=scheduling_generation+CASE WHEN $3 AND a.recovery_success_threshold IS DISTINCT FROM $8 THEN 1 ELSE 0 END,
		 consecutive_recovery_successes=CASE WHEN $3 AND a.recovery_success_threshold IS DISTINCT FROM $8 THEN 0 ELSE a.consecutive_recovery_successes END,
		 probe_model=CASE WHEN $3 THEN NULLIF($9,'') ELSE a.probe_model END,
		 rate_sync_enabled=CASE WHEN $10 THEN $11 ELSE a.rate_sync_enabled END,
		 rate_sync_interval_seconds=CASE WHEN $10 THEN $12 ELSE a.rate_sync_interval_seconds END,
		 priority_enabled=CASE WHEN $13 THEN $14 ELSE a.priority_enabled END,
		 guard_enabled=CASE WHEN $15 THEN $16 ELSE a.guard_enabled END,
		 guard_operator=CASE WHEN $15 THEN $17 ELSE a.guard_operator END,
		 guard_priority=CASE WHEN $15 THEN $18 ELSE a.guard_priority END,
		 next_probe_at=CASE WHEN $3 THEN LEAST(a.next_probe_at,now()) ELSE a.next_probe_at END,
		 next_rate_sync_at=CASE WHEN $10 THEN LEAST(a.next_rate_sync_at,now()) ELSE a.next_rate_sync_at END,
		 updated_at=now()
		FROM sites s
		WHERE a.site_id=s.id AND s.owner_id=$2 AND a.deleted_at IS NULL
		  AND a.id=ANY(string_to_array($1, ',')::uuid[])`,
		accountIDList, identity.ID,
		input.Health != nil, health.Enabled, health.ProbeIntervalSeconds, health.ProbeTimeoutSeconds, health.FailureThreshold, health.RecoverySuccessThreshold, health.ProbeModel,
		input.RateSync != nil, rateSync.Enabled, rateSync.IntervalSeconds,
		input.Priority != nil, priority.Enabled,
		input.Guard != nil, guard.Enabled, guard.Operator, guard.Priority,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != int64(len(accountIDs)) {
		return fmt.Errorf("bulk account settings updated %d of %d locked accounts", command.RowsAffected(), len(accountIDs))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sites s SET next_reconcile_at=LEAST(s.next_reconcile_at,now())
		WHERE s.owner_id=$1 AND EXISTS (
			SELECT 1 FROM upstream_accounts a
			WHERE a.site_id=s.id AND a.id=ANY(string_to_array($2, ',')::uuid[])
		)`, identity.ID, accountIDList); err != nil {
		return err
	}

	rows, err = tx.Query(ctx, accountSelect+`
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL
		  AND a.id=ANY(string_to_array($2, ',')::uuid[])`, identity.ID, accountIDList)
	if err != nil {
		return err
	}
	updatedByID := make(map[string]Account, len(accountIDs))
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return err
		}
		updatedByID[account.ID] = account
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	updated := make([]Account, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		account, ok := updatedByID[accountID]
		if !ok {
			return fmt.Errorf("bulk account settings response is missing locked account %s", accountID)
		}
		updated = append(updated, account)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	a.requestBalanceRefresh()
	accountIDsBySite := make(map[string][]string)
	siteOrder := make([]string, 0)
	for _, account := range updated {
		if _, exists := accountIDsBySite[account.SiteID]; !exists {
			siteOrder = append(siteOrder, account.SiteID)
		}
		accountIDsBySite[account.SiteID] = append(accountIDsBySite[account.SiteID], account.ID)
	}
	for _, siteID := range siteOrder {
		siteAccountIDs := accountIDsBySite[siteID]
		auditDetail := map[string]any{
			"account_count": len(siteAccountIDs),
			"account_ids":   siteAccountIDs,
		}
		if input.Health != nil {
			auditDetail["health"] = health
		}
		if input.RateSync != nil {
			auditDetail["rate_sync"] = rateSync
		}
		if input.Priority != nil {
			auditDetail["priority"] = priority
		}
		if input.Guard != nil {
			auditDetail["guard"] = guard
		}
		_ = a.audit(ctx, identity.ID, identity.ID, siteID, "", "account.settings.bulk_update", "success", auditDetail)
	}
	writeData(w, http.StatusOK, bulkAccountSettingsResult{UpdatedCount: len(updated), Accounts: updated})
	return nil
}

func (a *App) loadAccountWork(ctx context.Context, accountID, ownerFilter string) (AccountWork, error) {
	var work AccountWork
	var sourceCredential *string
	err := a.db.QueryRow(ctx, `
		SELECT a.id,a.site_id,s.owner_id,s.name,s.base_url,s.api_key_ciphertext,a.remote_id,a.name,a.platform,a.account_type,a.remote_status,
		 a.schedulable,a.priority,a.rate_multiplier,a.health_enabled,a.probe_interval_seconds,a.probe_timeout_seconds,a.failure_threshold,a.recovery_success_threshold,a.probe_model,
		 a.rate_sync_enabled,a.rate_sync_interval_seconds,a.source_type,a.source_base_url,a.source_credential_ciphertext,a.source_user_id,a.source_group,
		 a.recharge_ratio,a.source_rate_multiplier,a.priority_enabled,a.guard_enabled,a.guard_operator,a.guard_priority,a.guard_holding,
		 a.health_state,a.consecutive_failures,a.consecutive_recovery_successes,a.managed_hold
		FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE a.id=$1 AND s.owner_id=COALESCE(NULLIF($2,'')::uuid,s.owner_id) AND a.deleted_at IS NULL`, accountID, ownerFilter).Scan(
		&work.ID, &work.SiteID, &work.OwnerID, &work.SiteName, &work.SiteBaseURL, &work.SiteAPIKeyCiphertext,
		&work.RemoteID, &work.Name, &work.Platform, &work.AccountType, &work.RemoteStatus, &work.Schedulable, &work.Priority,
		&work.RateMultiplier, &work.HealthEnabled, &work.ProbeIntervalSeconds, &work.ProbeTimeoutSeconds, &work.FailureThreshold, &work.RecoverySuccessThreshold,
		&work.ProbeModel, &work.RateSyncEnabled, &work.RateSyncIntervalSeconds, &work.SourceType, &work.SourceBaseURL,
		&sourceCredential, &work.SourceUserID, &work.SourceGroup, &work.RechargeRatio, &work.SourceRateMultiplier,
		&work.PriorityEnabled, &work.GuardEnabled, &work.GuardOperator, &work.GuardPriority, &work.GuardHolding,
		&work.HealthState, &work.ConsecutiveFailures, &work.ConsecutiveRecoverySuccesses, &work.ManagedHold,
	)
	if sourceCredential != nil {
		work.SourceCredentialCiphertext = *sourceCredential
		work.SourceCredentialSet = true
	}
	return work, err
}

func (a *App) clientForWork(work AccountWork) (*upstream.Sub2Client, error) {
	return a.sub2Client(SiteSecret{ID: work.SiteID, OwnerID: work.OwnerID, Name: work.SiteName, BaseURL: work.SiteBaseURL, APIKeyCiphertext: work.SiteAPIKeyCiphertext, Enabled: true})
}

type probeOutcome struct {
	Success       bool                  `json:"success"`
	Message       string                `json:"message"`
	LatencyMS     int                   `json:"latency_ms"`
	FailureReason *health.FailureReason `json:"failure_reason,omitempty"`
	HTTPStatus    *int                  `json:"http_status,omitempty"`
	Paused        bool                  `json:"paused"`
	Restored      bool                  `json:"restored"`
}

type probeControlToken struct {
	Sequence             int64
	SchedulingGeneration int64
}

type probeControlState struct {
	Schedulable                  bool
	ManagedHold                  bool
	ConsecutiveFailures          int
	ConsecutiveRecoverySuccesses int
	FailureThreshold             int
	RecoverySuccessThreshold     int
	AppliedSequence              int64
	SchedulingGeneration         int64
}

func (state probeControlState) accepts(token probeControlToken) bool {
	return state.SchedulingGeneration == token.SchedulingGeneration && token.Sequence > state.AppliedSequence
}

func (state probeControlState) recoveryThresholdReached() bool {
	return state.ManagedHold && state.RecoverySuccessThreshold >= 1 && state.ConsecutiveRecoverySuccesses+1 >= state.RecoverySuccessThreshold
}

func (a *App) beginProbe(ctx context.Context, accountID string) (probeControlToken, error) {
	var token probeControlToken
	err := a.db.QueryRow(ctx, `
		UPDATE upstream_accounts SET probe_sequence=probe_sequence+1
		WHERE id=$1 AND deleted_at IS NULL
		RETURNING probe_sequence,scheduling_generation`, accountID).Scan(&token.Sequence, &token.SchedulingGeneration)
	return token, err
}

func newProbePersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), accountSchedulingOperationTimeout)
}

func (a *App) runProbe(ctx context.Context, work AccountWork, kind, actorID string) (probeOutcome, error) {
	token, err := a.beginProbe(ctx, work.ID)
	if err != nil {
		return probeOutcome{}, err
	}
	client, err := a.clientForWork(work)
	if err != nil {
		return probeOutcome{}, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(work.ProbeTimeoutSeconds)*time.Second)
	started := time.Now()
	model := ""
	if work.ProbeModel != nil {
		model = *work.ProbeModel
	}
	result, probeErr := client.TestAccount(timeoutCtx, work.RemoteID, model)
	cancel()
	latency := int(time.Since(started).Milliseconds())
	if result.LatencyMS > 0 {
		latency = result.LatencyMS
	}
	if probeErr != nil {
		result.Success = false
		result.Message = probeErr.Error()
		result.LatencyMS = latency
	}
	var classification *health.Classification
	if !result.Success {
		classified := health.ClassifyFailure(map[string]any{
			"message":     result.Message,
			"http_status": result.HTTPStatus,
			"code":        result.Code,
			"detail":      result.FailureData,
			"cause":       probeErr,
		})
		classification = &classified
	}
	if len(result.Message) > 1000 {
		result.Message = result.Message[:1000]
	}
	outcome := probeOutcome{Success: result.Success, Message: result.Message, LatencyMS: latency}
	if classification != nil {
		outcome.FailureReason = &classification.Reason
		outcome.HTTPStatus = classification.HTTPStatus
	}
	now := time.Now()
	var failureReason any
	var failureHTTPStatus any
	if classification != nil {
		failureReason = string(classification.Reason)
		if classification.HTTPStatus != nil {
			failureHTTPStatus = *classification.HTTPStatus
		}
	}
	persistenceCtx, cancelPersistence := newProbePersistenceContext(ctx)
	defer cancelPersistence()
	// A completed model probe is durable before any local state transition or
	// remote scheduling side effect. Control-plane failures must not erase an
	// otherwise deterministic uptime sample.
	_, err = a.db.Exec(persistenceCtx, `INSERT INTO probe_attempts(id,owner_id,site_id,account_id,kind,success,latency_ms,model,message,failure_reason,http_status) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)`, uuid.NewString(), work.OwnerID, work.SiteID, work.ID, kind, result.Success, latency, model, result.Message, failureReason, failureHTTPStatus)
	if err != nil {
		return outcome, err
	}

	controlSkipped := false
	var controlWarning error
	err = a.withAccountSchedulingLock(persistenceCtx, work.ID, func(connection *pgxpool.Conn) error {
		ctx := persistenceCtx
		var state probeControlState
		if loadErr := connection.QueryRow(ctx, `
			SELECT schedulable,managed_hold,consecutive_failures,consecutive_recovery_successes,failure_threshold,recovery_success_threshold,applied_probe_sequence,scheduling_generation
			FROM upstream_accounts WHERE id=$1 AND deleted_at IS NULL`, work.ID).Scan(
			&state.Schedulable, &state.ManagedHold, &state.ConsecutiveFailures, &state.ConsecutiveRecoverySuccesses, &state.FailureThreshold, &state.RecoverySuccessThreshold,
			&state.AppliedSequence, &state.SchedulingGeneration,
		); loadErr != nil {
			return loadErr
		}
		if !state.accepts(token) {
			controlSkipped = true
			_, updateErr := connection.Exec(ctx, `
				UPDATE upstream_accounts
				SET next_probe_at=GREATEST(next_probe_at,$2::timestamptz+probe_interval_seconds*interval '1 second')
				WHERE id=$1 AND deleted_at IS NULL`, work.ID, now)
			return updateErr
		}

		if result.Success {
			if !state.ManagedHold {
				command, updateErr := connection.Exec(ctx, `
					UPDATE upstream_accounts SET
					 health_state='healthy',consecutive_failures=0,consecutive_recovery_successes=0,last_probe_at=$2,last_probe_latency_ms=$3,last_success_at=$2,
					 last_failure_reason=NULL,last_failure_http_status=NULL,last_error=NULL,
					 next_probe_at=$2::timestamptz+probe_interval_seconds*interval '1 second',applied_probe_sequence=$4,updated_at=now()
					WHERE id=$1 AND deleted_at IS NULL AND applied_probe_sequence<$4 AND scheduling_generation=$5`,
					work.ID, now, latency, token.Sequence, token.SchedulingGeneration)
				if updateErr != nil {
					return updateErr
				}
				if command.RowsAffected() == 0 {
					return errors.New("probe result became stale while recording success")
				}
				return nil
			}

			recoveryThresholdReached := state.recoveryThresholdReached()
			command, updateErr := connection.Exec(ctx, `
				UPDATE upstream_accounts SET
				 consecutive_failures=0,consecutive_recovery_successes=LEAST(consecutive_recovery_successes+1,recovery_success_threshold),last_probe_at=$2,last_probe_latency_ms=$3,last_success_at=$2,
				 last_failure_reason=NULL,last_failure_http_status=NULL,last_error=NULL,
				 next_probe_at=$2::timestamptz+probe_interval_seconds*interval '1 second',applied_probe_sequence=$4,updated_at=now()
				WHERE id=$1 AND managed_hold AND deleted_at IS NULL AND applied_probe_sequence<$4 AND scheduling_generation=$5`,
				work.ID, now, latency, token.Sequence, token.SchedulingGeneration)
			if updateErr != nil {
				return updateErr
			}
			if command.RowsAffected() == 0 {
				return errors.New("managed hold disappeared while recording recovery probe")
			}
			if !recoveryThresholdReached {
				return nil
			}

			remote, recoveryErr := client.GetAccount(ctx, work.RemoteID)
			if recoveryErr == nil && !remote.Schedulable {
				remote, recoveryErr = client.SetSchedulable(ctx, work.RemoteID, true)
			}
			if recoveryErr == nil && !remote.Schedulable {
				recoveryErr = errors.New("Sub2API did not restore account scheduling")
			}
			if recoveryErr != nil {
				result.Message = appendProbeControlError(result.Message, "failed to restore scheduling", recoveryErr)
				command, updateErr := connection.Exec(ctx, `
					UPDATE upstream_accounts SET
					 health_state=CASE WHEN schedulable THEN 'failing' ELSE 'paused' END,
					 consecutive_failures=0,last_probe_at=$2,last_probe_latency_ms=$3,last_success_at=$2,
					 last_failure_reason=NULL,last_failure_http_status=NULL,last_error=$4,
					 next_probe_at=$2::timestamptz+probe_interval_seconds*interval '1 second',updated_at=now()
					WHERE id=$1 AND managed_hold AND applied_probe_sequence=$5 AND scheduling_generation=$6`,
					work.ID, now, latency, result.Message, token.Sequence, token.SchedulingGeneration)
				if updateErr != nil {
					return errors.Join(recoveryErr, updateErr)
				}
				if command.RowsAffected() == 0 {
					return errors.Join(recoveryErr, errors.New("managed hold disappeared while recording recovery failure"))
				}
				return recoveryErr
			}

			command, updateErr = connection.Exec(ctx, `
				UPDATE upstream_accounts SET
				 health_state='healthy',consecutive_failures=0,consecutive_recovery_successes=0,managed_hold=false,schedulable=true,
				 last_probe_at=$2,last_probe_latency_ms=$3,last_success_at=$2,last_failure_reason=NULL,last_failure_http_status=NULL,last_error=NULL,
				 next_probe_at=$2::timestamptz+probe_interval_seconds*interval '1 second',updated_at=now()
				WHERE id=$1 AND managed_hold AND applied_probe_sequence=$4 AND scheduling_generation=$5`,
				work.ID, now, latency, token.Sequence, token.SchedulingGeneration)
			if updateErr != nil {
				return updateErr
			}
			if command.RowsAffected() == 0 {
				return errors.New("managed hold disappeared while completing recovery")
			}
			outcome.Restored = true
			return nil
		}

		thresholdReached := state.ConsecutiveFailures+1 >= state.FailureThreshold
		claimManagedHold := state.ManagedHold
		if !state.ManagedHold && state.Schedulable && thresholdReached {
			remote, getErr := client.GetAccount(ctx, work.RemoteID)
			if getErr != nil {
				controlWarning = getErr
				result.Message = appendProbeControlError(result.Message, "could not read remote scheduling state", getErr)
			} else if remote.Schedulable {
				claimManagedHold = true
			} else {
				state.Schedulable = false
			}
		}

		command, updateErr := connection.Exec(ctx, `
			UPDATE upstream_accounts SET
			 consecutive_failures=consecutive_failures+1,
			 consecutive_recovery_successes=0,
			 managed_hold=$7,
			 schedulable=$8,
			 health_state=CASE WHEN $7 AND managed_hold AND health_state='paused' THEN 'paused' ELSE 'failing' END,
			 last_probe_at=$2,last_probe_latency_ms=$3,last_failure_at=$2,last_error=$4,last_failure_reason=$5,last_failure_http_status=$6,
			 next_probe_at=$2::timestamptz+probe_interval_seconds*interval '1 second',applied_probe_sequence=$9,updated_at=now()
			WHERE id=$1 AND deleted_at IS NULL AND applied_probe_sequence<$9 AND scheduling_generation=$10`,
			work.ID, now, latency, result.Message, failureReason, failureHTTPStatus, claimManagedHold, state.Schedulable,
			token.Sequence, token.SchedulingGeneration)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() == 0 {
			return errors.New("probe result became stale while recording failure")
		}
		if !claimManagedHold {
			return nil
		}

		remote, getErr := client.GetAccount(ctx, work.RemoteID)
		if getErr != nil {
			controlWarning = getErr
			result.Message = appendProbeControlError(result.Message, "could not read remote scheduling state", getErr)
			_, updateErr = connection.Exec(ctx, `UPDATE upstream_accounts SET last_error=$2,updated_at=now() WHERE id=$1 AND managed_hold AND applied_probe_sequence=$3 AND scheduling_generation=$4`, work.ID, result.Message, token.Sequence, token.SchedulingGeneration)
			return updateErr
		}
		if remote.Schedulable {
			remote, getErr = client.SetSchedulable(ctx, work.RemoteID, false)
			if getErr != nil {
				controlWarning = getErr
				result.Message = appendProbeControlError(result.Message, "failed to pause scheduling", getErr)
				_, updateErr = connection.Exec(ctx, `UPDATE upstream_accounts SET last_error=$2,updated_at=now() WHERE id=$1 AND managed_hold AND applied_probe_sequence=$3 AND scheduling_generation=$4`, work.ID, result.Message, token.Sequence, token.SchedulingGeneration)
				return updateErr
			}
		}
		if remote.Schedulable {
			controlWarning = errors.New("Sub2API did not persist the paused state")
			result.Message = appendProbeControlError(result.Message, "failed to pause scheduling", controlWarning)
			_, updateErr = connection.Exec(ctx, `UPDATE upstream_accounts SET last_error=$2,updated_at=now() WHERE id=$1 AND managed_hold AND applied_probe_sequence=$3 AND scheduling_generation=$4`, work.ID, result.Message, token.Sequence, token.SchedulingGeneration)
			return updateErr
		}
		command, updateErr = connection.Exec(ctx, `UPDATE upstream_accounts SET health_state='paused',schedulable=false,last_error=$2,updated_at=now() WHERE id=$1 AND managed_hold AND applied_probe_sequence=$3 AND scheduling_generation=$4`, work.ID, result.Message, token.Sequence, token.SchedulingGeneration)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() == 0 {
			return errors.New("managed hold disappeared while completing pause")
		}
		outcome.Paused = true
		return nil
	})
	outcome.Message = result.Message
	status := "success"
	if !result.Success {
		status = "failed"
	}
	if controlSkipped {
		status = "skipped"
	}
	auditDetail := map[string]any{"latency_ms": latency, "failure_reason": failureReason, "http_status": failureHTTPStatus, "paused": outcome.Paused, "restored": outcome.Restored, "kind": kind, "state_applied": !controlSkipped, "probe_sequence": token.Sequence}
	if controlWarning != nil {
		auditDetail["control_warning"] = truncateError(controlWarning)
	}
	if err != nil {
		auditDetail["control_error"] = truncateError(err)
	}
	_ = a.audit(persistenceCtx, work.OwnerID, actorID, work.SiteID, work.ID, "account.probe", status, auditDetail)
	return outcome, err
}

func appendProbeControlError(message, operation string, err error) string {
	if message != "" {
		message += "; "
	}
	message += operation + ": " + err.Error()
	if len(message) > 1000 {
		return message[:1000]
	}
	return message
}

func (a *App) probeAccountHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	outcome, err := a.runProbe(r.Context(), work, "manual", identity.ID)
	if err != nil {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "PROBE_FAILED", Message: err.Error()}
	}
	account, _ := a.getAccount(r.Context(), accountID, identity.ID)
	writeData(w, http.StatusOK, map[string]any{"result": outcome, "account": account})
	return nil
}

func (a *App) listAccountModels(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	client, err := a.clientForWork(work)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := client.ListAccountModels(ctx, work.RemoteID)
	if err != nil {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "MODELS_FAILED", Message: err.Error()}
	}
	writeData(w, http.StatusOK, models)
	return nil
}

type rateSyncOutcome struct {
	SourceRate    float64 `json:"source_rate"`
	EffectiveRate float64 `json:"effective_rate"`
	Endpoint      string  `json:"endpoint"`
}

func (a *App) runRateSync(ctx context.Context, work AccountWork, actorID string) (rateSyncOutcome, error) {
	client, err := a.clientForWork(work)
	if err != nil {
		return rateSyncOutcome{}, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var outcome rateSyncOutcome
	if work.SourceType == "sub2api" {
		billing, err := client.ProbeBilling(timeoutCtx, work.RemoteID)
		if err != nil {
			a.recordRateFailure(ctx, work, err)
			return outcome, err
		}
		outcome.SourceRate = billing.EffectiveRateMultiplier
		outcome.EffectiveRate = billing.EffectiveRateMultiplier / work.RechargeRatio
		outcome.Endpoint = billing.Endpoint
	} else {
		if work.SourceBaseURL == nil || strings.TrimSpace(*work.SourceBaseURL) == "" || work.SourceCredentialCiphertext == "" {
			err := errors.New("NewAPI 倍率同步需要源站地址和凭据")
			a.recordRateFailure(ctx, work, err)
			return outcome, err
		}
		credential, err := a.cipher.Decrypt(work.SourceCredentialCiphertext, "account:"+work.ID)
		if err != nil {
			return outcome, err
		}
		userID, group := "", ""
		if work.SourceUserID != nil {
			userID = *work.SourceUserID
		}
		if work.SourceGroup != nil {
			group = *work.SourceGroup
		}
		if strings.TrimSpace(group) == "" {
			err := errors.New("NewAPI 倍率同步必须显式绑定账号实际使用的分组")
			a.recordRateFailure(ctx, work, err)
			return outcome, err
		}
		newClient, err := upstream.NewNewAPIClient(*work.SourceBaseURL, credential, userID, a.httpClient)
		if err != nil {
			a.recordRateFailure(ctx, work, err)
			return outcome, err
		}
		rate, err := newClient.ResolveRate(timeoutCtx, group)
		if err != nil {
			a.recordRateFailure(ctx, work, err)
			return outcome, err
		}
		outcome.SourceRate = rate.Rate
		outcome.EffectiveRate = rate.Rate / work.RechargeRatio
		outcome.Endpoint = rate.Endpoint
	}
	if !isFinite(outcome.EffectiveRate) || outcome.EffectiveRate < 0 {
		return outcome, errors.New("源站返回了无效倍率")
	}
	remote, err := client.UpdateAccount(timeoutCtx, work.RemoteID, upstream.AccountUpdate{RateMultiplier: &outcome.EffectiveRate})
	if err != nil {
		a.recordRateFailure(ctx, work, err)
		return outcome, err
	}
	if remote.RateMultiplier == nil || math.Abs(*remote.RateMultiplier-outcome.EffectiveRate) > 1e-7 {
		err := errors.New("Sub2API 未持久化账号倍率")
		a.recordRateFailure(ctx, work, err)
		return outcome, err
	}
	isNewAPI := work.SourceType == "newapi"
	_, err = a.db.Exec(ctx, `
		UPDATE upstream_accounts SET
		 rate_multiplier=$2,source_rate_multiplier=$3,source_rate_endpoint=$4,last_rate_sync_at=now(),
		 source_credential_state=CASE WHEN $5 THEN 'valid' ELSE source_credential_state END,
		 source_credential_checked_at=CASE WHEN $5 THEN now() ELSE source_credential_checked_at END,
		 next_rate_sync_at=now()+rate_sync_interval_seconds*interval '1 second',last_error=NULL,work_lease_until=NULL,updated_at=now()
		WHERE id=$1`, work.ID, outcome.EffectiveRate, outcome.SourceRate, outcome.Endpoint, isNewAPI)
	if err != nil {
		return outcome, err
	}
	_, _ = a.db.Exec(ctx, `UPDATE sites SET next_reconcile_at=LEAST(next_reconcile_at,now()) WHERE id=$1`, work.SiteID)
	_ = a.audit(ctx, work.OwnerID, actorID, work.SiteID, work.ID, "account.rate_sync", "success", map[string]any{"source_rate": outcome.SourceRate, "effective_rate": outcome.EffectiveRate, "endpoint": outcome.Endpoint})
	a.applyEnabledGroupRulesForSourceAccount(ctx, work.ID)
	return outcome, nil
}

func (a *App) recordRateFailure(ctx context.Context, work AccountWork, err error) {
	message := err.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	credentialInvalid := work.SourceType == "newapi" && upstream.IsNewAPIAuthenticationError(err)
	_, _ = a.db.Exec(ctx, `
		UPDATE upstream_accounts SET
		 last_error=$2,
		 source_credential_state=CASE WHEN $3 THEN 'invalid' ELSE source_credential_state END,
		 source_credential_checked_at=CASE WHEN $3 THEN now() ELSE source_credential_checked_at END,
		 next_rate_sync_at=now()+rate_sync_interval_seconds*interval '1 second',work_lease_until=NULL,updated_at=now()
		WHERE id=$1`, work.ID, message, credentialInvalid)
	_ = a.audit(ctx, work.OwnerID, "", work.SiteID, work.ID, "account.rate_sync", "failed", map[string]any{"error": message})
}

func (a *App) rateSyncAccountHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	outcome, err := a.runRateSync(r.Context(), work, identity.ID)
	if err != nil {
		if work.SourceType == "newapi" && upstream.IsNewAPIAuthenticationError(err) {
			return newAPICredentialInvalidError()
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "RATE_SYNC_FAILED", Message: err.Error()}
	}
	account, _ := a.getAccount(r.Context(), accountID, identity.ID)
	writeData(w, http.StatusOK, map[string]any{"result": outcome, "account": account})
	return nil
}

func (a *App) listSourceGroups(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	work, err := a.loadAccountWork(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	if work.SourceType != "newapi" || work.SourceBaseURL == nil || work.SourceCredentialCiphertext == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_NOT_CONFIGURED", Message: "请先保存 NewAPI 源站地址和凭据"}
	}
	credential, err := a.cipher.Decrypt(work.SourceCredentialCiphertext, "account:"+work.ID)
	if err != nil {
		return err
	}
	userID := ""
	if work.SourceUserID != nil {
		userID = *work.SourceUserID
	}
	client, err := upstream.NewNewAPIClient(*work.SourceBaseURL, credential, userID, a.httpClient)
	if err != nil {
		if upstream.IsNewAPIAuthenticationError(err) {
			a.setSourceCredentialState(r.Context(), work.ID, "invalid")
			return newAPICredentialInvalidError()
		}
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rates, err := client.ListGroupRates(ctx)
	if err != nil {
		if upstream.IsNewAPIAuthenticationError(err) {
			a.setSourceCredentialState(r.Context(), work.ID, "invalid")
			return newAPICredentialInvalidError()
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "SOURCE_GROUPS_FAILED", Message: err.Error()}
	}
	a.setSourceCredentialState(r.Context(), work.ID, "valid")
	writeData(w, http.StatusOK, rates)
	return nil
}

func (a *App) previewSourceGroups(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	if _, err := a.loadAccountWork(r.Context(), accountID, identity.ID); err != nil {
		return err
	}
	var input struct {
		SourceBaseURL    string `json:"source_base_url"`
		SourceCredential string `json:"source_credential"`
		SourceUserID     string `json:"source_user_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	input.SourceBaseURL = strings.TrimSpace(input.SourceBaseURL)
	input.SourceCredential = strings.TrimSpace(input.SourceCredential)
	input.SourceUserID = strings.TrimSpace(input.SourceUserID)
	if input.SourceBaseURL == "" || input.SourceCredential == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "NEWAPI_DRAFT_REQUIRED", Message: "请先填写 NewAPI 源站地址和凭据"}
	}
	client, err := upstream.NewNewAPIClient(input.SourceBaseURL, input.SourceCredential, input.SourceUserID, a.httpClient)
	if err != nil {
		if upstream.IsNewAPIAuthenticationError(err) {
			return newAPICredentialInvalidError()
		}
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_SOURCE", Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rates, err := client.ListGroupRates(ctx)
	if err != nil {
		if upstream.IsNewAPIAuthenticationError(err) {
			return newAPICredentialInvalidError()
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "SOURCE_GROUPS_FAILED", Message: err.Error()}
	}
	writeData(w, http.StatusOK, rates)
	return nil
}

func (a *App) setSourceCredentialState(ctx context.Context, accountID, state string) {
	_, _ = a.db.Exec(ctx, `UPDATE upstream_accounts SET source_credential_state=$2,source_credential_checked_at=now(),updated_at=now() WHERE id=$1`, accountID, state)
}

func newAPICredentialInvalidError() *apiError {
	return &apiError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "NEWAPI_CREDENTIAL_INVALID",
		Message: "NewAPI Session 或 Token 已过期或无效，请更新源站凭据",
	}
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

type reconcileAccount struct {
	ID              string
	RemoteID        int64
	Name            string
	Priority        int
	Rate            *float64
	PriorityEnabled bool
	GuardEnabled    bool
	GuardOperator   string
	GuardPriority   int
	GuardHolding    bool
	RestorePriority *int
	Groups          []GroupSummary
	CacheRate       *float64
	Desired         int
	Violation       bool
}

type ReconcileResult struct {
	Evaluated int `json:"evaluated"`
	Changed   int `json:"changed"`
	Failed    int `json:"failed"`
}

func (a *App) reconcileSite(ctx context.Context, siteID, ownerFilter, actorID string) (ReconcileResult, error) {
	site, err := a.siteSecret(ctx, siteID, ownerFilter)
	if err != nil {
		return ReconcileResult{}, err
	}
	var start, step int
	var cacheEnabled bool
	var rateWeight, cacheWeight float64
	if err := a.db.QueryRow(ctx, `SELECT priority_start,priority_step,cache_rate_priority_enabled,rate_priority_weight,cache_rate_priority_weight FROM sites WHERE id=$1`, siteID).Scan(&start, &step, &cacheEnabled, &rateWeight, &cacheWeight); err != nil {
		return ReconcileResult{}, err
	}
	if cacheEnabled {
		if actorID != "" {
			if sampleErr := a.sampleSiteCacheRates(ctx, siteID, ownerFilter); sampleErr != nil && ctx.Err() == nil {
				a.logger.Warn("cache rate sample before reconcile failed", slog.String("site_id", siteID), slog.Any("error", sampleErr))
			}
		}
	}
	rows, err := a.db.Query(ctx, `
		SELECT a.id,a.remote_id,a.name,a.priority,a.rate_multiplier,a.priority_enabled,a.guard_enabled,a.guard_operator,a.guard_priority,a.guard_holding,a.guard_restore_priority,a.cache_rate,
		COALESCE((SELECT jsonb_agg(jsonb_build_object('id',g.id,'remote_id',g.remote_id,'name',g.name,'rate_multiplier',g.rate_multiplier,'priority',m.group_priority)) FROM account_group_memberships m JOIN upstream_groups g ON g.id=m.group_id WHERE m.account_id=a.id AND g.deleted_at IS NULL),'[]'::jsonb)
		FROM upstream_accounts a WHERE a.site_id=$1 AND a.deleted_at IS NULL AND (a.priority_enabled OR a.guard_enabled OR a.guard_holding)`, siteID)
	if err != nil {
		return ReconcileResult{}, err
	}
	accounts := make([]reconcileAccount, 0)
	for rows.Next() {
		var account reconcileAccount
		var groups []byte
		if err := rows.Scan(&account.ID, &account.RemoteID, &account.Name, &account.Priority, &account.Rate, &account.PriorityEnabled, &account.GuardEnabled, &account.GuardOperator, &account.GuardPriority, &account.GuardHolding, &account.RestorePriority, &account.CacheRate, &groups); err != nil {
			rows.Close()
			return ReconcileResult{}, err
		}
		_ = json.Unmarshal(groups, &account.Groups)
		account.Desired = account.Priority
		accounts = append(accounts, account)
	}
	rows.Close()

	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: start, Step: step, CacheEnabled: cacheEnabled, RateWeight: rateWeight, CacheWeight: cacheWeight})

	client, err := a.sub2Client(site)
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Evaluated: len(accounts)}
	for _, account := range accounts {
		if account.Desired == account.Priority && account.Violation == account.GuardHolding {
			continue
		}
		if account.Desired != account.Priority {
			requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			remote, updateErr := client.UpdateAccount(requestCtx, account.RemoteID, upstream.AccountUpdate{Priority: &account.Desired})
			cancel()
			if updateErr != nil || remote.Priority != account.Desired {
				result.Failed++
				message := "Sub2API did not persist priority"
				if updateErr != nil {
					message = updateErr.Error()
				}
				_ = a.audit(ctx, site.OwnerID, actorID, siteID, account.ID, "account.priority.reconcile", "failed", map[string]any{"error": message, "desired": account.Desired})
				continue
			}
			result.Changed++
		}
		_, updateErr := a.db.Exec(ctx, `UPDATE upstream_accounts SET priority=$2,guard_restore_priority=CASE WHEN $3 AND NOT guard_holding THEN priority ELSE guard_restore_priority END,guard_holding=$3,updated_at=now() WHERE id=$1`, account.ID, account.Desired, account.Violation)
		if updateErr != nil {
			result.Failed++
			continue
		}
		_ = a.audit(ctx, site.OwnerID, actorID, siteID, account.ID, "account.priority.reconcile", "success", map[string]any{"before": account.Priority, "after": account.Desired, "guard": account.Violation})
	}
	_, _ = a.db.Exec(ctx, `UPDATE sites SET last_reconcile_at=now(),next_reconcile_at=now()+reconcile_interval_seconds*interval '1 second',reconcile_lease_until=NULL,updated_at=now() WHERE id=$1`, siteID)
	return result, nil
}

func buildReconcilePlan(accounts []reconcileAccount, start, step int) {
	buildReconcilePlanWithOptions(accounts, reconcilePlanOptions{Start: start, Step: step})
}

func buildReconcilePlanWithOptions(accounts []reconcileAccount, options reconcilePlanOptions) {
	start, step := options.Start, options.Step
	sortable := make([]*reconcileAccount, 0)
	for index := range accounts {
		accounts[index].Desired = accounts[index].Priority
		accounts[index].Violation = false
		if accounts[index].PriorityEnabled && accounts[index].Rate != nil {
			sortable = append(sortable, &accounts[index])
		}
	}
	if options.CacheEnabled && (options.RateWeight > 0 || options.CacheWeight > 0) {
		assignWeightedPriorities(sortable, start, step, options.RateWeight, options.CacheWeight)
	} else {
		sort.SliceStable(sortable, func(i, j int) bool {
			if *sortable[i].Rate == *sortable[j].Rate {
				return sortable[i].RemoteID < sortable[j].RemoteID
			}
			return *sortable[i].Rate < *sortable[j].Rate
		})
		rank := -1
		var previous float64
		for index, account := range sortable {
			if index == 0 || math.Abs(*account.Rate-previous) > 1e-9 {
				rank++
				previous = *account.Rate
			}
			account.Desired = start + rank*step
		}
	}

	for index := range accounts {
		account := &accounts[index]
		if account.GuardEnabled && account.Rate != nil {
			for _, group := range account.Groups {
				if group.RateMultiplier == nil {
					continue
				}
				violates := *account.Rate > *group.RateMultiplier || (account.GuardOperator == "gte" && math.Abs(*account.Rate-*group.RateMultiplier) <= 1e-9)
				if violates {
					account.Violation = true
					break
				}
			}
		}
		if account.Violation {
			account.Desired = account.GuardPriority
		} else if account.GuardHolding && !account.PriorityEnabled && account.RestorePriority != nil {
			account.Desired = *account.RestorePriority
		}
	}
}

func (a *App) reconcileSiteHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	result, err := a.reconcileSite(r.Context(), siteID, identity.ID, identity.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "RECONCILE_FAILED", Message: fmt.Sprintf("优先级调整失败：%v", err)}
	}
	writeData(w, http.StatusOK, result)
	return nil
}
