package app

import (
	"context"
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
)

// Publish the same sanitized runtime snapshot used by the controller so the UI
// cannot keep showing an old inventory-only eligibility result after a preflight.
func (a *App) recordNativeObservation(ctx context.Context, w AccountWork, remote upstream.Sub2Account) error {
	raw, err := json.Marshal(remote.Native)
	if err != nil {
		return err
	}
	var generation int64
	err = a.db.QueryRow(ctx, `UPDATE upstream_accounts SET native_constraints=$2,native_checked_at=now(),source_mapping_fingerprint=CASE WHEN $4 THEN $5 ELSE source_mapping_fingerprint END WHERE id=$1 AND source_generation=$3 AND deleted_at IS NULL RETURNING source_generation`, w.ID, raw, w.SourceGeneration, remote.SourceMappingKnown, remote.SourceMappingFingerprint).Scan(&generation)
	if err != nil {
		return err
	}
	if generation != w.SourceGeneration {
		return errEngineReplan
	}
	return nil
}
