package app

import (
	"context"
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"time"
)

func (a *App) collectSiteTraffic(ctx context.Context, id, owner string) error {
	return a.withSiteSchedulingLock(ctx, id, func(_ *pgxpool.Conn) error { return a.collectSiteTrafficLocked(ctx, id, owner) })
}
func (a *App) collectSiteTrafficLocked(ctx context.Context, id, owner string) error {
	site, err := a.siteSecret(ctx, id, owner)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT id::text,remote_id,name,source_generation,probe_model FROM upstream_accounts WHERE site_id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	works := []AccountWork{}
	for rows.Next() {
		w := AccountWork{OwnerID: site.OwnerID}
		w.SiteID = id
		if err = rows.Scan(&w.ID, &w.RemoteID, &w.Name, &w.SourceGeneration, &w.ProbeModel); err != nil {
			rows.Close()
			return err
		}
		works = append(works, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	batch, sampleErr := client.RecentSiteTraffic(requestCtx)
	if sampleErr != nil {
		batch.Status = "error"
		batch.Message = "站点真实请求采集失败"
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
	for _, w := range works {
		s := upstream.SummarizeTraffic(batch, w.RemoteID, modelForWork(w))
		raw, _ := json.Marshal(s)
		command, e := tx.Exec(ctx, `INSERT INTO quality_traffic(account_id,snapshot,source_generation) SELECT id,$2,source_generation FROM upstream_accounts WHERE id=$1 AND source_generation=$3 AND deleted_at IS NULL FOR SHARE ON CONFLICT(account_id) DO UPDATE SET snapshot=excluded.snapshot,source_generation=excluded.source_generation,checked_at=now()`, w.ID, raw, w.SourceGeneration)
		if e != nil {
			return e
		}
		if command.RowsAffected() == 0 {
			continue
		}
		message := ""
		if s.Status != "ok" {
			message = s.Message
		}
		if e = recordIncidentTx(ctx, tx, w, "collector_traffic", message); e != nil {
			return e
		}
	}
	knownProbeRows, e := tx.Query(ctx, `SELECT r.id::text,COALESCE(r.result->>'request_id','') FROM service_canary_runs r JOIN service_profiles p ON p.id=r.profile_id LEFT JOIN upstream_groups g ON g.id=p.group_id LEFT JOIN upstream_accounts a ON a.id=p.account_id WHERE COALESCE(g.site_id,a.site_id)=$1 AND r.started_at>now()-interval '24 hours'`, id)
	if e != nil {
		return e
	}
	probeIDs := map[string]bool{}
	for knownProbeRows.Next() {
		var runID, requestID string
		if e = knownProbeRows.Scan(&runID, &requestID); e != nil {
			knownProbeRows.Close()
			return e
		}
		probeIDs[runID] = true
		if requestID != "" {
			probeIDs[requestID] = true
		}
	}
	e = knownProbeRows.Err()
	knownProbeRows.Close()
	if e != nil {
		return e
	}
	userRecords := []upstream.TrafficRecord{}
	for _, r := range batch.Records {
		if !probeIDs[r.RequestID] {
			userRecords = append(userRecords, r)
		}
	}
	outcomes, uncorrelated := upstream.FinalRequestOutcomes(userRecords)
	for _, v := range outcomes {
		_, err = tx.Exec(ctx, `INSERT INTO request_outcome_observations(site_id,site_generation,group_remote_id,request_id,model,outcome,seen_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(site_id,site_generation,group_remote_id,request_id) DO UPDATE SET outcome=CASE WHEN request_outcome_observations.outcome='conflict' OR excluded.outcome='conflict' THEN 'conflict' WHEN request_outcome_observations.outcome='unknown' THEN excluded.outcome WHEN excluded.outcome='unknown' OR request_outcome_observations.outcome=excluded.outcome THEN request_outcome_observations.outcome ELSE 'conflict' END,model=CASE WHEN excluded.outcome IN('success','failure') THEN excluded.model ELSE request_outcome_observations.model END,seen_at=GREATEST(request_outcome_observations.seen_at,excluded.seen_at)`, id, generation, v.GroupID, v.RequestID, v.Model, v.Outcome, v.SeenAt)
		if err != nil {
			return err
		}
	}
	meta, _ := json.Marshal(map[string]any{"status": batch.Status, "message": batch.Message, "truncated": batch.Truncated, "uncorrelated": uncorrelated, "checked_at": time.Now().UTC(), "sample_rows": len(batch.Records)})
	if _, err = tx.Exec(ctx, `UPDATE sites SET traffic_collection=$2,next_traffic_sample_at=now()+interval '60 seconds',traffic_sample_lease_until=NULL WHERE id=$1`, id, meta); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return sampleErr
}

func (a *App) requestOutcomesHandler(w http.ResponseWriter, r *http.Request) error {
	var raw []byte
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT g.id AS group_id,g.name,u.model,count(*) AS observed_requests,count(*) FILTER(WHERE u.outcome='success') AS confirmed_success,count(*) FILTER(WHERE u.outcome='failure') AS confirmed_failure,count(*) FILTER(WHERE u.outcome IN('unknown','conflict')) AS unconfirmed,max(u.seen_at) AS latest_at,s.traffic_collection AS coverage FROM request_outcome_observations u JOIN sites s ON s.id=u.site_id AND s.telemetry_generation=u.site_generation JOIN upstream_groups g ON g.site_id=s.id AND g.remote_id=u.group_remote_id WHERE s.owner_id=$1 AND g.deleted_at IS NULL AND u.seen_at>now()-interval '15 minutes' GROUP BY g.id,g.name,u.model,s.traffic_collection ORDER BY g.name,u.model)v`, identityFrom(r).ID).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, 200, json.RawMessage(raw))
	return nil
}
