package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Tendo33/upstream-pilot/internal/auditlog"
)

const (
	defaultAuditLogRetentionDays = 14
	minAuditLogRetentionDays     = 1
	maxAuditLogRetentionDays     = 365
)

type auditLogSettings struct {
	RetentionDays           int        `json:"retention_days"`
	Configured              bool       `json:"configured"`
	LastPurgedAt            *time.Time `json:"last_purged_at,omitempty"`
	LastPurgeRemovedFiles   int        `json:"last_purge_removed_files"`
	LastPurgeRemovedRecords int        `json:"last_purge_removed_records"`
}

type auditLogSettingsInput struct {
	RetentionDays int `json:"retention_days"`
}

func (a *App) getAuditLogSettingsHandler(w http.ResponseWriter, r *http.Request) error {
	settings, err := a.loadAuditLogSettings(r.Context(), identityFrom(r).ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, settings)
	return nil
}

func (a *App) updateAuditLogSettingsHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var input auditLogSettingsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := normalizeAuditLogRetention(input.RetentionDays); err != nil {
		return err
	}
	if _, err := a.db.Exec(r.Context(), `
		INSERT INTO audit_log_settings(owner_id,retention_days,updated_at)
		VALUES($1,$2,now())
		ON CONFLICT (owner_id) DO UPDATE SET retention_days=EXCLUDED.retention_days, updated_at=now()`,
		identity.ID, input.RetentionDays); err != nil {
		return err
	}
	result, err := a.purgeOwnerAuditLogs(r.Context(), identity.ID, input.RetentionDays)
	if err != nil {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "AUDIT_LOG_PURGE_FAILED", Message: "活动日志清理失败：" + err.Error()}
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "audit_log.settings.update", "success", map[string]any{
		"retention_days":  input.RetentionDays,
		"removed_files":   result.RemovedFiles,
		"removed_records": result.RemovedRecords,
	})
	settings, err := a.loadAuditLogSettings(r.Context(), identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, settings)
	return nil
}

func (a *App) purgeAuditLogHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	settings, err := a.loadAuditLogSettings(r.Context(), identity.ID)
	if err != nil {
		return err
	}
	if !settings.Configured {
		return &apiError{Status: http.StatusBadRequest, Code: "AUDIT_LOG_RETENTION_REQUIRED", Message: "请先保存日志保留天数"}
	}
	if _, err := a.purgeOwnerAuditLogs(r.Context(), identity.ID, settings.RetentionDays); err != nil {
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "AUDIT_LOG_PURGE_FAILED", Message: "活动日志清理失败：" + err.Error()}
	}
	settings, err = a.loadAuditLogSettings(r.Context(), identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, settings)
	return nil
}

func (a *App) loadAuditLogSettings(ctx context.Context, ownerID string) (auditLogSettings, error) {
	settings := auditLogSettings{RetentionDays: defaultAuditLogRetentionDays}
	err := a.db.QueryRow(ctx, `
		SELECT retention_days,last_purged_at,last_purge_removed_files,last_purge_removed_records
		FROM audit_log_settings WHERE owner_id=$1`, ownerID).Scan(
		&settings.RetentionDays, &settings.LastPurgedAt, &settings.LastPurgeRemovedFiles, &settings.LastPurgeRemovedRecords,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	settings.Configured = true
	return settings, nil
}

func (a *App) purgeOwnerAuditLogs(ctx context.Context, ownerID string, retainDays int) (auditlog.PurgeResult, error) {
	result, err := a.auditLog.Purge(ownerID, retainDays, time.Now().UTC())
	if err != nil {
		return result, err
	}
	_, err = a.db.Exec(ctx, `
		UPDATE audit_log_settings
		SET last_purged_at=now(),
		    last_purge_removed_files=$2,
		    last_purge_removed_records=$3,
		    updated_at=now()
		WHERE owner_id=$1`, ownerID, result.RemovedFiles, result.RemovedRecords)
	return result, err
}

func normalizeAuditLogRetention(days int) error {
	if days < minAuditLogRetentionDays || days > maxAuditLogRetentionDays {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_AUDIT_LOG_RETENTION", Message: "日志保留天数必须在 1 到 365 之间"}
	}
	return nil
}
