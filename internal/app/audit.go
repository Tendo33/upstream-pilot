package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Tendo33/upstream-pilot/internal/auditlog"
)

const legacyAuditMigrationLockID int64 = 7820133780

func (a *App) audit(ctx context.Context, ownerID, actorID, siteID, accountID, action, outcome string, detail map[string]any) error {
	err := a.appendAudit(ctx, auditlog.Record{
		ID:          uuid.NewString(),
		OwnerID:     ownerID,
		ActorUserID: actorID,
		SiteID:      siteID,
		AccountID:   accountID,
		Action:      action,
		Outcome:     outcome,
		Detail:      detail,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		a.logger.Error("audit event write failed", slog.String("action", action), slog.String("outcome", outcome), slog.Any("error", err))
	}
	return err
}

func (a *App) appendAudit(ctx context.Context, record auditlog.Record) error {
	if record.OwnerID == "" {
		return errors.New("audit event owner ID is required")
	}
	if record.ActorUserID != "" && record.ActorUserID != record.OwnerID {
		return errors.New("audit event actor does not belong to owner")
	}
	if record.AccountID != "" {
		var resolvedSiteID string
		err := a.db.QueryRow(ctx, `
			SELECT s.id::text,s.name,a.name
			FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
			WHERE a.id=$1 AND s.owner_id=$2
			  AND ($3='' OR s.id=$3::uuid)`, record.AccountID, record.OwnerID, record.SiteID).Scan(
			&resolvedSiteID, &record.SiteName, &record.AccountName,
		)
		if err != nil {
			return fmt.Errorf("resolve audit account snapshot: %w", err)
		}
		record.SiteID = resolvedSiteID
	} else if record.SiteID != "" {
		if err := a.db.QueryRow(ctx, `SELECT name FROM sites WHERE id=$1 AND owner_id=$2`, record.SiteID, record.OwnerID).Scan(&record.SiteName); err != nil {
			return fmt.Errorf("resolve audit site snapshot: %w", err)
		}
	}
	return a.auditLog.Append(record)
}

func (a *App) migrateLegacyAuditEvents(ctx context.Context) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin legacy audit migration: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, legacyAuditMigrationLockID); err != nil {
		return fmt.Errorf("lock legacy audit migration: %w", err)
	}

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('audit_events_legacy') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect legacy audit table: %w", err)
	}
	if !exists {
		return tx.Commit(ctx)
	}
	knownIDs, err := a.auditLog.IDs()
	if err != nil {
		return fmt.Errorf("read existing audit log IDs: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT e.id::text,e.owner_id::text,COALESCE(e.actor_user_id::text,''),
		       COALESCE(e.site_id::text,''),COALESCE(e.account_id::text,''),
		       COALESCE(s.name,''),COALESCE(a.name,''),e.action,e.outcome,e.detail,e.created_at
		FROM audit_events_legacy e
		LEFT JOIN sites s ON s.id=e.site_id AND s.owner_id=e.owner_id
		LEFT JOIN upstream_accounts a ON a.id=e.account_id AND a.site_id=e.site_id
		ORDER BY e.created_at,e.id`)
	if err != nil {
		return fmt.Errorf("read legacy audit events: %w", err)
	}
	imported := 0
	for rows.Next() {
		var record auditlog.Record
		var detail []byte
		if err := rows.Scan(
			&record.ID, &record.OwnerID, &record.ActorUserID, &record.SiteID, &record.AccountID,
			&record.SiteName, &record.AccountName, &record.Action, &record.Outcome, &detail, &record.CreatedAt,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy audit event: %w", err)
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &record.Detail); err != nil {
				rows.Close()
				return fmt.Errorf("decode legacy audit event %s: %w", record.ID, err)
			}
		}
		if _, duplicate := knownIDs[record.ID]; duplicate {
			continue
		}
		if err := a.auditLog.Append(record); err != nil {
			rows.Close()
			return fmt.Errorf("write legacy audit event %s: %w", record.ID, err)
		}
		knownIDs[record.ID] = struct{}{}
		imported++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate legacy audit events: %w", err)
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `DROP TABLE audit_events_legacy`); err != nil {
		return fmt.Errorf("drop migrated legacy audit table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit legacy audit migration: %w", err)
	}
	if imported > 0 {
		a.logger.Info("legacy audit events migrated", slog.Int("events", imported), slog.String("directory", a.auditLog.Directory()))
	}
	return nil
}
