package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

const (
	balanceSnapshotMaxAge      = 5 * time.Minute
	balanceRefreshPollInterval = time.Minute
	balanceRefreshAdvisoryLock = int64(7820133780)
)

type accountBalanceRefreshGroup struct {
	Key   string
	Works []accountBalanceWork
	Due   bool
}

func (a *App) requestBalanceRefresh() {
	if a.balanceRefreshSignal == nil {
		return
	}
	select {
	case a.balanceRefreshSignal <- struct{}{}:
	default:
	}
}

func (a *App) runAccountBalanceRefresher(ctx context.Context) {
	ticker := time.NewTicker(balanceRefreshPollInterval)
	defer ticker.Stop()
	for {
		if err := a.refreshDueAccountBalanceSnapshots(ctx); err != nil && ctx.Err() == nil && a.logger != nil {
			a.logger.Warn("account balance snapshot refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.balanceRefreshSignal:
		}
	}
}

func (a *App) refreshDueAccountBalanceSnapshots(ctx context.Context) error {
	connection, err := a.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()

	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, balanceRefreshAdvisoryLock).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, balanceRefreshAdvisoryLock)
	}()

	works, err := a.loadAllAccountBalanceWork(ctx)
	if err != nil || len(works) == 0 {
		return err
	}
	snapshots, err := a.loadAllAccountBalanceSnapshots(ctx)
	if err != nil {
		return err
	}
	groups := dueAccountBalanceRefreshGroups(works, snapshots, time.Now().UTC())
	if len(groups) == 0 {
		return nil
	}

	jobs := make(chan accountBalanceRefreshGroup)
	errorsFound := make(chan error, len(groups))
	var workers sync.WaitGroup
	workerCount := min(balanceQueryConcurrency, len(groups))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for group := range jobs {
				queryCtx, cancel := context.WithTimeout(ctx, balanceQueryTimeout)
				balance := a.queryAccountBalance(queryCtx, group.Works[0])
				cancel()
				checkedAt := time.Now().UTC()
				if saveErr := a.saveAccountBalanceSnapshots(ctx, group.Key, group.Works, balance, checkedAt); saveErr != nil {
					errorsFound <- saveErr
				}
			}
		}()
	}
	for _, group := range groups {
		select {
		case jobs <- group:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			close(errorsFound)
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	close(errorsFound)

	var joined error
	for refreshErr := range errorsFound {
		joined = errors.Join(joined, refreshErr)
	}
	if alertErr := a.sendDueBalanceAlerts(ctx, time.Now().UTC()); alertErr != nil {
		joined = errors.Join(joined, alertErr)
	}
	return joined
}

func dueAccountBalanceRefreshGroups(works []accountBalanceWork, snapshots map[string]accountBalanceSnapshot, now time.Time) []accountBalanceRefreshGroup {
	byKey := make(map[string]*accountBalanceRefreshGroup)
	for _, work := range works {
		key := accountBalanceCacheKey(work)
		group := byKey[key]
		if group == nil {
			group = &accountBalanceRefreshGroup{Key: key}
			byKey[key] = group
		}
		group.Works = append(group.Works, work)
		snapshot, ok := snapshots[work.ID]
		if !ok || snapshot.CacheKey != key || now.Sub(snapshot.CheckedAt) >= balanceSnapshotMaxAge {
			group.Due = true
		}
	}

	keys := make([]string, 0, len(byKey))
	for key, group := range byKey {
		if group.Due {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	groups := make([]accountBalanceRefreshGroup, 0, len(keys))
	for _, key := range keys {
		group := byKey[key]
		sort.Slice(group.Works, func(i, j int) bool { return group.Works[i].ID < group.Works[j].ID })
		groups = append(groups, *group)
	}
	return groups
}

func accountBalanceCacheKey(work accountBalanceWork) string {
	prefix := work.OwnerID + "|" + work.SourceType + "|"
	var rawURL string
	var credentialFingerprint string
	if work.SourceType == "newapi" && work.SourceBaseURL != nil {
		rawURL = *work.SourceBaseURL
		credentialFingerprint = work.SourceCredentialFingerprint
	} else if work.SourceType == "sub2api" && work.ObservedSourceBaseURL != nil {
		rawURL = *work.ObservedSourceBaseURL
		credentialFingerprint = work.ObservedSourceCredentialFingerprint
	}
	if canonical := canonicalBalanceSourceURL(rawURL, work.SourceType); canonical != "" && credentialFingerprint != "" {
		return prefix + canonical + "|key:" + credentialFingerprint
	}
	if work.SourceType == "sub2api" {
		return prefix + "admin:" + work.SiteID + ":" + strconv.FormatInt(work.RemoteID, 10)
	}
	return prefix + "account:" + work.ID
}

func balanceCredentialFingerprint(credential string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(credential)))
	return hex.EncodeToString(sum[:])
}

func canonicalBalanceSourceURL(rawURL, sourceType string) string {
	normalized, err := upstream.NormalizeBaseURL(rawURL)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	suffixes := []string{"/v1/usage", "/v1"}
	if sourceType == "newapi" {
		suffixes = []string{"/api/subscription/self", "/api/user/self", "/api/status", "/api/v1", "/v1"}
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(strings.ToLower(parsed.Path), suffix) {
			parsed.Path = parsed.Path[:len(parsed.Path)-len(suffix)]
			break
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/")
}

func (a *App) loadAllAccountBalanceWork(ctx context.Context) ([]accountBalanceWork, error) {
	rows, err := a.db.Query(ctx, `
		SELECT a.id::text,s.owner_id::text,a.site_id::text,s.name,s.base_url,s.api_key_ciphertext,a.remote_id,a.source_type,a.source_base_url,a.observed_source_base_url,COALESCE(a.observed_source_credential_fingerprint,''),a.source_credential_ciphertext,a.source_user_id
		FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE a.deleted_at IS NULL
		ORDER BY s.owner_id,a.site_id,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	works := make([]accountBalanceWork, 0)
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
		works = append(works, work)
	}
	return works, rows.Err()
}

func (a *App) loadAccountBalanceSnapshots(ctx context.Context, accountIDs []string) (map[string]accountBalanceSnapshot, error) {
	if len(accountIDs) == 0 {
		return map[string]accountBalanceSnapshot{}, nil
	}
	rows, err := a.db.Query(ctx, `
		SELECT account_id::text,cache_key,status,provider,plan_name,remaining,used,total,unit,message,endpoint,checked_at
		FROM account_balance_snapshots
		WHERE account_id=ANY(string_to_array($1, ',')::uuid[])`, strings.Join(accountIDs, ","))
	if err != nil {
		return nil, err
	}
	return scanAccountBalanceSnapshots(rows)
}

func (a *App) loadAllAccountBalanceSnapshots(ctx context.Context) (map[string]accountBalanceSnapshot, error) {
	rows, err := a.db.Query(ctx, `
		SELECT b.account_id::text,b.cache_key,b.status,b.provider,b.plan_name,b.remaining,b.used,b.total,b.unit,b.message,b.endpoint,b.checked_at
		FROM account_balance_snapshots b
		JOIN upstream_accounts a ON a.id=b.account_id
		WHERE a.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	return scanAccountBalanceSnapshots(rows)
}

func scanAccountBalanceSnapshots(rows pgx.Rows) (map[string]accountBalanceSnapshot, error) {
	defer rows.Close()
	result := make(map[string]accountBalanceSnapshot)
	for rows.Next() {
		var accountID string
		var snapshot accountBalanceSnapshot
		if err := rows.Scan(
			&accountID, &snapshot.CacheKey, &snapshot.Status, &snapshot.Provider, &snapshot.PlanName,
			&snapshot.Remaining, &snapshot.Used, &snapshot.Total, &snapshot.Unit, &snapshot.Message, &snapshot.Endpoint, &snapshot.CheckedAt,
		); err != nil {
			return nil, err
		}
		result[accountID] = snapshot
	}
	return result, rows.Err()
}

func (a *App) saveAccountBalanceSnapshots(ctx context.Context, cacheKey string, works []accountBalanceWork, balance upstream.BalanceResult, checkedAt time.Time) error {
	if len(works) == 0 {
		return nil
	}
	if balance.Status != "ok" && balance.Status != "unsupported" && balance.Status != "invalid" && balance.Status != "error" {
		balance = upstream.BalanceResult{Status: "error", Message: "余额采集返回无效状态"}
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, work := range works {
		if _, err := tx.Exec(ctx, `
			INSERT INTO account_balance_snapshots(account_id,cache_key,status,provider,plan_name,remaining,used,total,unit,message,endpoint,checked_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT(account_id) DO UPDATE SET
			 cache_key=excluded.cache_key,status=excluded.status,provider=excluded.provider,plan_name=excluded.plan_name,
			 remaining=excluded.remaining,used=excluded.used,total=excluded.total,unit=excluded.unit,
			 message=excluded.message,endpoint=excluded.endpoint,checked_at=excluded.checked_at,updated_at=now()`,
			work.ID, cacheKey, balance.Status, balance.Provider, balance.PlanName, balance.Remaining, balance.Used, balance.Total,
			balance.Unit, balance.Message, balance.Endpoint, checkedAt,
		); err != nil {
			return fmt.Errorf("save balance snapshot for account %s: %w", work.ID, err)
		}
	}
	return tx.Commit(ctx)
}
