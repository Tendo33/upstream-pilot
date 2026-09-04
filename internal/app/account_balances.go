package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"sub2api-upstream-manager/internal/upstream"
)

const (
	maxBalanceAccountIDs    = 200
	balanceQueryConcurrency = 6
	balanceQueryTimeout     = 20 * time.Second
)

type accountBalancesInput struct {
	AccountIDs []string `json:"account_ids"`
}

type accountBalanceWork struct {
	ID                                  string
	OwnerID                             string
	SiteID                              string
	SiteName                            string
	SiteBaseURL                         string
	SiteAPIKeyCiphertext                string
	RemoteID                            int64
	SourceType                          string
	SourceBaseURL                       *string
	ObservedSourceBaseURL               *string
	ObservedSourceCredentialFingerprint string
	SourceCredentialCiphertext          string
	SourceCredentialFingerprint         string
	SourceUserID                        *string
}

type accountBalanceResult struct {
	AccountID string     `json:"account_id"`
	CheckedAt *time.Time `json:"checked_at"`
	upstream.BalanceResult
}

type accountBalanceSnapshot struct {
	CacheKey  string
	CheckedAt time.Time
	upstream.BalanceResult
}

func (a *App) accountBalancesHandler(w http.ResponseWriter, r *http.Request) error {
	var input accountBalancesInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	accountIDs, apiErr := normalizeBalanceAccountIDs(input.AccountIDs)
	if apiErr != nil {
		return apiErr
	}
	if len(accountIDs) == 0 {
		writeData(w, http.StatusOK, []accountBalanceResult{})
		return nil
	}

	identity := identityFrom(r)
	works, err := a.loadAccountBalanceWork(r.Context(), identity.ID, accountIDs)
	if err != nil {
		return err
	}
	if len(works) != len(accountIDs) {
		return &apiError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "账号不存在或无权访问"}
	}

	snapshots, err := a.loadAccountBalanceSnapshots(r.Context(), accountIDs)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	results := make([]accountBalanceResult, len(works))
	refreshNeeded := false
	for index, work := range works {
		cacheKey := accountBalanceCacheKey(work)
		snapshot, ok := snapshots[work.ID]
		if !ok || snapshot.CacheKey != cacheKey {
			refreshNeeded = true
			results[index] = accountBalanceResult{
				AccountID: work.ID,
				BalanceResult: upstream.BalanceResult{
					Status: "pending", Message: "余额快照等待后台刷新",
				},
			}
			continue
		}
		checkedAt := snapshot.CheckedAt
		results[index] = accountBalanceResult{AccountID: work.ID, CheckedAt: &checkedAt, BalanceResult: snapshot.BalanceResult}
		if now.Sub(snapshot.CheckedAt) >= balanceSnapshotMaxAge {
			refreshNeeded = true
		}
	}
	if refreshNeeded {
		a.requestBalanceRefresh()
	}

	writeData(w, http.StatusOK, results)
	return nil
}

func normalizeBalanceAccountIDs(raw []string) ([]string, *apiError) {
	if len(raw) > maxBalanceAccountIDs {
		return nil, &apiError{Status: http.StatusBadRequest, Code: "TOO_MANY_ACCOUNTS", Message: "单次最多查询 200 个账号余额"}
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

func (a *App) loadAccountBalanceWork(ctx context.Context, ownerID string, accountIDs []string) ([]accountBalanceWork, error) {
	rows, err := a.db.Query(ctx, `
		SELECT a.id::text,s.owner_id::text,a.site_id::text,s.name,s.base_url,s.api_key_ciphertext,a.remote_id,a.source_type,a.source_base_url,a.observed_source_base_url,COALESCE(a.observed_source_credential_fingerprint,''),a.source_credential_ciphertext,a.source_user_id
		FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL
		  AND a.id=ANY(string_to_array($2, ',')::uuid[])`, ownerID, strings.Join(accountIDs, ","))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]accountBalanceWork, len(accountIDs))
	for rows.Next() {
		var work accountBalanceWork
		var sourceCredential *string
		if err := rows.Scan(
			&work.ID, &work.OwnerID, &work.SiteID, &work.SiteName, &work.SiteBaseURL, &work.SiteAPIKeyCiphertext, &work.RemoteID,
			&work.SourceType, &work.SourceBaseURL, &work.ObservedSourceBaseURL, &work.ObservedSourceCredentialFingerprint, &sourceCredential, &work.SourceUserID,
		); err != nil {
			return nil, err
		}
		if sourceCredential != nil {
			work.SourceCredentialCiphertext = *sourceCredential
		}
		a.setBalanceCredentialFingerprint(&work)
		byID[work.ID] = work
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ordered := make([]accountBalanceWork, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if work, ok := byID[accountID]; ok {
			ordered = append(ordered, work)
		}
	}
	return ordered, nil
}

func (a *App) setBalanceCredentialFingerprint(work *accountBalanceWork) {
	if work.SourceType != "newapi" || work.SourceCredentialCiphertext == "" {
		return
	}
	credential, err := a.cipher.Decrypt(work.SourceCredentialCiphertext, "account:"+work.ID)
	if err == nil {
		work.SourceCredentialFingerprint = balanceCredentialFingerprint(credential)
	}
}

func (a *App) queryAccountBalance(ctx context.Context, work accountBalanceWork) upstream.BalanceResult {
	if work.SourceType == "newapi" {
		if work.SourceBaseURL == nil || work.SourceCredentialCiphertext == "" {
			return upstream.BalanceResult{Status: "unsupported", Provider: "newapi", Message: "请先配置 NewAPI 源站地址和凭据"}
		}
		credential, err := a.cipher.Decrypt(work.SourceCredentialCiphertext, "account:"+work.ID)
		if err != nil {
			return balanceFailure(err, "newapi")
		}
		userID := ""
		if work.SourceUserID != nil {
			userID = *work.SourceUserID
		}
		client, err := upstream.NewNewAPIClient(*work.SourceBaseURL, credential, userID, a.httpClient)
		if err != nil {
			return balanceFailure(err, "newapi")
		}
		balance, err := client.Balance(ctx)
		if err != nil {
			return balanceFailure(err, "newapi")
		}
		return balance
	}

	client, err := a.sub2Client(SiteSecret{
		ID: work.SiteID, Name: work.SiteName, BaseURL: work.SiteBaseURL,
		APIKeyCiphertext: work.SiteAPIKeyCiphertext, Enabled: true,
	})
	if err != nil {
		return balanceFailure(err, "sub2api-admin")
	}

	adminBalance, adminErr := client.AccountUsageBalance(ctx, work.RemoteID)
	if adminErr == nil && (adminBalance.Status == "ok" || adminBalance.Status == "invalid") {
		return adminBalance
	}

	exported, exportErr := client.AccountUsageCredentials(ctx, []int64{work.RemoteID})
	usageCredential := exported[work.RemoteID]
	if exportErr != nil {
		if adminErr != nil {
			return balanceFailure(errors.Join(adminErr, exportErr), "usage")
		}
		return balanceFailure(exportErr, "usage")
	}
	if usageCredential.BaseURL == "" || usageCredential.APIKey == "" {
		if adminErr == nil && adminBalance.Message != "" {
			adminBalance.Message += "；账号凭据中缺少 base_url 或 api_key"
			return adminBalance
		}
		if adminErr != nil {
			return upstream.BalanceResult{Status: "unsupported", Provider: "usage", Message: "Sub2API 账号 usage 接口不可用，且账号凭据中缺少 base_url 或 api_key"}
		}
		return upstream.BalanceResult{Status: "unsupported", Provider: "usage", Message: "账号凭据中缺少 base_url 或 api_key"}
	}
	balance, err := upstream.QueryUsageBalance(ctx, usageCredential.BaseURL, usageCredential.APIKey, a.httpClient)
	if err != nil {
		return balanceFailure(err, "usage")
	}
	return balance
}

func balanceFailure(err error, provider string) upstream.BalanceResult {
	status := "error"
	var httpErr *upstream.HTTPError
	if upstream.IsNewAPIAuthenticationError(err) || (errors.As(err, &httpErr) && (httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden)) {
		status = "invalid"
	}
	message := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())), " ")
	runes := []rune(message)
	if len(runes) > 240 {
		message = string(runes[:240])
	}
	return upstream.BalanceResult{Status: status, Provider: provider, Message: message}
}
