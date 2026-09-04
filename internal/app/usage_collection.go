package app

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

func (a *App) collectSiteUsage(ctx context.Context, id, owner string) error {
	return a.withSiteSchedulingLock(ctx, id, func(_ *pgxpool.Conn) error { return a.collectSiteUsageLocked(ctx, id, owner) })
}
func (a *App) collectSiteUsageLocked(ctx context.Context, id, owner string) error {
	site, err := a.siteSecret(ctx, id, owner)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, sampleErr := client.RecentUsage(requestCtx)
	if sampleErr != nil {
		result.Status = "error"
		result.Message = "用量采集失败，请检查接口与读取权限"
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT telemetry_generation FROM sites WHERE id=$1 FOR SHARE`, id).Scan(&generation); err != nil {
		return err
	}
	if generation != site.TelemetryGeneration {
		return errEngineReplan
	}
	for _, v := range result.Records {
		_, err = tx.Exec(ctx, `INSERT INTO usage_observations(site_id,remote_id,account_remote_id,group_remote_id,request_id,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,native_first_chunk_ms,synthetic,created_at,site_generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12 OR EXISTS(SELECT 1 FROM service_canary_runs r WHERE r.id::text=$5),$13,$14) ON CONFLICT DO NOTHING`, id, v.ID, v.AccountID, v.GroupID, v.RequestID, v.Model, v.InputTokens, v.OutputTokens, v.CacheReadTokens, v.CacheWriteTokens, v.NativeFirstChunkMS, strings.HasPrefix(v.SessionID, "pilot-canary:"), v.CreatedAt, generation)
		if err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `UPDATE sites SET usage_collection=$2,next_usage_at=now()+interval '5 minutes',usage_lease_until=NULL WHERE id=$1`, id, raw); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if sampleErr == nil {
		return a.evaluateRunwayAlerts(ctx)
	}
	return sampleErr
}
