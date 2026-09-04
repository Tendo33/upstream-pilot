package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// One open episode per source/account/channel. Updating a retry's message does
// not notify again. The incident and notification share the caller's transaction.
func recordIncidentTx(ctx context.Context, tx pgx.Tx, w AccountWork, channel, message string) error {
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT source_generation FROM upstream_accounts WHERE id=$1 FOR SHARE`, w.ID).Scan(&generation); err != nil {
		return err
	}
	if generation != w.SourceGeneration {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO operational_incidents(account_id,channel,owner_id,source_generation) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, w.ID, channel, w.OwnerID, generation)
	if err != nil {
		return err
	}
	var active bool
	var oldGeneration int64
	if err = tx.QueryRow(ctx, `SELECT active,source_generation FROM operational_incidents WHERE account_id=$1 AND channel=$2 FOR UPDATE`, w.ID, channel).Scan(&active, &oldGeneration); err != nil {
		return err
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	nowActive := message != ""
	changed := active != nowActive || (active && oldGeneration != generation)
	_, err = tx.Exec(ctx, `UPDATE operational_incidents SET active=$3,message=$4,source_generation=$5,checked_at=now(),episode=episode+CASE WHEN $6 AND $3 THEN 1 ELSE 0 END,opened_at=CASE WHEN $6 AND $3 THEN now() ELSE opened_at END,resolved_at=CASE WHEN NOT $3 AND $6 THEN now() WHEN $3 THEN NULL ELSE resolved_at END WHERE account_id=$1 AND channel=$2`, w.ID, channel, nowActive, message, generation, changed)
	if err != nil || !changed {
		return err
	}
	kind, text := channel+"_recovered", channel+" 已恢复"
	if nowActive {
		kind = channel + "_failed"
		text = message
	}
	if active && oldGeneration != generation && !nowActive {
		kind = channel + "_source_changed"
		text = channel + "：旧来源事件关闭，新来源等待验证"
	}
	_, err = tx.Exec(ctx, `INSERT INTO quality_notifications(id,owner_id,account_id,kind,message) VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), w.OwnerID, w.ID, kind, fmt.Sprintf("%s：%s", w.Name, text))
	return err
}

func (a *App) recordIncident(ctx context.Context, w AccountWork, channel, message string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = recordIncidentTx(ctx, tx, w, channel, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) incidentsHandler(w http.ResponseWriter, r *http.Request) error {
	var raw []byte
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT i.account_id,a.name AS account_name,a.site_id,i.channel,i.active,i.episode,i.message,i.source_generation,i.opened_at,i.checked_at,i.resolved_at,(i.source_generation=a.source_generation) AS current_source FROM operational_incidents i JOIN upstream_accounts a ON a.id=i.account_id WHERE i.owner_id=$1 AND i.episode>0 AND a.deleted_at IS NULL ORDER BY i.active DESC,i.checked_at DESC LIMIT 200)v`, identityFrom(r).ID).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, 200, json.RawMessage(raw))
	return nil
}
