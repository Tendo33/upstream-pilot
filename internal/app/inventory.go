package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"sub2api-upstream-manager/internal/upstream"
)

func (a *App) syncSite(ctx context.Context, siteID, ownerFilter, actorID, mode string) error {
	var inventoryStartedAt time.Time
	if err := a.db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&inventoryStartedAt); err != nil {
		return err
	}
	site, err := a.siteSecret(ctx, siteID, ownerFilter)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	groups, err := client.ListGroups(requestCtx)
	if err != nil {
		a.recordSiteFailure(ctx, siteID, err)
		_ = a.audit(ctx, site.OwnerID, actorID, siteID, "", "inventory.sync", "failed", map[string]any{"error": err.Error(), "mode": mode})
		return fmt.Errorf("库存同步失败：%w", err)
	}
	accounts, err := client.ListAccounts(requestCtx)
	if err != nil {
		a.recordSiteFailure(ctx, siteID, err)
		_ = a.audit(ctx, site.OwnerID, actorID, siteID, "", "inventory.sync", "failed", map[string]any{"error": err.Error(), "mode": mode})
		return fmt.Errorf("库存同步失败：%w", err)
	}
	lockedSourceTypes, err := a.lockedAccountSourceTypes(requestCtx, siteID)
	if err != nil {
		return err
	}
	detectedSourceTypes := a.detectAccountSourceTypes(requestCtx, accounts, lockedSourceTypes)

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	groupIDs := make(map[int64]string, len(groups))
	remoteGroupIDs := make([]int64, 0, len(groups))
	for _, group := range groups {
		localID := uuid.NewString()
		err := tx.QueryRow(ctx, `
			INSERT INTO upstream_groups(id,site_id,remote_id,name,platform,status,rate_multiplier,deleted_at,observed_at)
			VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULL,now())
			ON CONFLICT(site_id,remote_id) DO UPDATE SET name=excluded.name,platform=excluded.platform,status=excluded.status,
			 rate_multiplier=excluded.rate_multiplier,deleted_at=NULL,observed_at=now(),updated_at=now()
			RETURNING id`, localID, siteID, group.ID, group.Name, group.Platform, statusText(group.Status), group.RateMultiplier).Scan(&localID)
		if err != nil {
			return err
		}
		groupIDs[group.ID] = localID
		remoteGroupIDs = append(remoteGroupIDs, group.ID)
	}
	if len(remoteGroupIDs) == 0 {
		_, err = tx.Exec(ctx, `UPDATE upstream_groups SET deleted_at=now(),updated_at=now() WHERE site_id=$1 AND deleted_at IS NULL`, siteID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE upstream_groups SET deleted_at=now(),updated_at=now() WHERE site_id=$1 AND deleted_at IS NULL AND NOT(remote_id=ANY($2))`, siteID, remoteGroupIDs)
	}
	if err != nil {
		return err
	}

	remoteAccountIDs := make([]int64, 0, len(accounts))
	for accountIndex, account := range accounts {
		localID := uuid.NewString()
		detectedSourceType := detectedSourceTypes[accountIndex]
		initialSourceType := detectedSourceType
		if initialSourceType == "" {
			initialSourceType = "sub2api"
		}
		var defaultProbeModel any
		if strings.EqualFold(strings.TrimSpace(account.Platform), "openai") {
			defaultProbeModel = "gpt-5.5"
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO upstream_accounts(id,site_id,remote_id,name,platform,account_type,remote_status,schedulable,priority,rate_multiplier,remote_updated_at,observed_source_base_url,observed_source_credential_fingerprint,source_type,probe_model,deleted_at,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,''),$14,$15,NULL,now())
			ON CONFLICT(site_id,remote_id) DO UPDATE SET name=excluded.name,platform=excluded.platform,account_type=excluded.account_type,
			 remote_status=excluded.remote_status,
			 schedulable=CASE WHEN upstream_accounts.updated_at<=$19 THEN excluded.schedulable ELSE upstream_accounts.schedulable END,
			 priority=excluded.priority,rate_multiplier=excluded.rate_multiplier,
			 remote_updated_at=excluded.remote_updated_at,
			 observed_source_base_url=CASE WHEN $16 THEN excluded.observed_source_base_url ELSE upstream_accounts.observed_source_base_url END,
			 observed_source_credential_fingerprint=CASE WHEN $17 THEN excluded.observed_source_credential_fingerprint ELSE upstream_accounts.observed_source_credential_fingerprint END,
			 source_type=CASE WHEN NOT upstream_accounts.source_type_locked AND $18 THEN excluded.source_type ELSE upstream_accounts.source_type END,
			 deleted_at=NULL,observed_at=now(),updated_at=now()
			RETURNING id`, localID, siteID, account.ID, account.Name, account.Platform, account.Type, statusText(account.Status), account.Schedulable, account.Priority, account.RateMultiplier, account.UpdatedAt, account.ObservedSourceBaseURL, account.ObservedSourceCredentialFingerprint, initialSourceType, defaultProbeModel, account.ObservedSourceBaseURLKnown, account.ObservedSourceCredentialFingerprintKnown, detectedSourceType != "", inventoryStartedAt).Scan(&localID)
		if err != nil {
			return err
		}
		remoteAccountIDs = append(remoteAccountIDs, account.ID)
		if _, err := tx.Exec(ctx, `DELETE FROM account_group_memberships WHERE account_id=$1`, localID); err != nil {
			return err
		}
		for _, membership := range account.AccountGroups {
			groupID := groupIDs[membership.GroupID]
			if groupID == "" && membership.Group != nil {
				groupID = uuid.NewString()
				if err := tx.QueryRow(ctx, `INSERT INTO upstream_groups(id,site_id,remote_id,name,platform,status,rate_multiplier,deleted_at,observed_at) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULL,now()) ON CONFLICT(site_id,remote_id) DO UPDATE SET name=excluded.name,rate_multiplier=excluded.rate_multiplier,deleted_at=NULL,observed_at=now(),updated_at=now() RETURNING id`, groupID, siteID, membership.Group.ID, membership.Group.Name, membership.Group.Platform, statusText(membership.Group.Status), membership.Group.RateMultiplier).Scan(&groupID); err != nil {
					return err
				}
				groupIDs[membership.GroupID] = groupID
			}
			if groupID != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO account_group_memberships(account_id,group_id,site_id,group_priority) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, localID, groupID, siteID, membership.Priority); err != nil {
					return err
				}
			}
		}
	}
	if len(remoteAccountIDs) == 0 {
		_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET deleted_at=now(),updated_at=now() WHERE site_id=$1 AND deleted_at IS NULL`, siteID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE upstream_accounts SET deleted_at=now(),updated_at=now() WHERE site_id=$1 AND deleted_at IS NULL AND NOT(remote_id=ANY($2))`, siteID, remoteAccountIDs)
	}
	if err != nil {
		return err
	}
	version := client.Version(requestCtx)
	_, err = tx.Exec(ctx, `UPDATE sites SET connection_state='healthy',last_error=NULL,version_hint=COALESCE(NULLIF($2,''),version_hint),last_inventory_at=now(),next_inventory_at=now()+inventory_interval_seconds*interval '1 second',inventory_lease_until=NULL,updated_at=now() WHERE id=$1`, siteID, version)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_ = a.audit(ctx, site.OwnerID, actorID, siteID, "", "inventory.sync", "success", map[string]any{"accounts": len(accounts), "groups": len(groups), "mode": mode})

	a.requestBalanceRefresh()
	return nil
}

func (a *App) lockedAccountSourceTypes(ctx context.Context, siteID string) (map[int64]string, error) {
	rows, err := a.db.Query(ctx, `SELECT remote_id,source_type FROM upstream_accounts WHERE site_id=$1 AND source_type_locked`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]string)
	for rows.Next() {
		var remoteID int64
		var sourceType string
		if err := rows.Scan(&remoteID, &sourceType); err != nil {
			return nil, err
		}
		result[remoteID] = sourceType
	}
	return result, rows.Err()
}

func (a *App) detectAccountSourceTypes(ctx context.Context, accounts []upstream.Sub2Account, lockedSourceTypes map[int64]string) []string {
	result := make([]string, len(accounts))
	candidates := make(map[string][]int)
	for index, account := range accounts {
		if sourceType, locked := lockedSourceTypes[account.ID]; locked {
			result[index] = sourceType
			continue
		}
		if account.SourceTypeHint != "" {
			result[index] = account.SourceTypeHint
			continue
		}
		if upstream.IsNewAPISourceCandidate(account) {
			candidates[*account.ObservedSourceBaseURL] = append(candidates[*account.ObservedSourceBaseURL], index)
		}
	}

	semaphore := make(chan struct{}, 6)
	var wait sync.WaitGroup
	for sourceURL, indexes := range candidates {
		sourceURL, indexes := sourceURL, indexes
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			isNewAPI := upstream.ProbeNewAPISource(probeCtx, sourceURL, a.httpClient)
			cancel()
			if !isNewAPI {
				return
			}
			for _, index := range indexes {
				result[index] = "newapi"
			}
		}()
	}
	wait.Wait()
	return result
}
