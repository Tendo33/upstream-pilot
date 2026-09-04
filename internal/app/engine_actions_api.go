package app

import (
	"encoding/json"
	"net/http"
)

func (a *App) engineActionsHandler(w http.ResponseWriter, r *http.Request) error {
	var raw []byte
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT x.id,x.account_id,a.name AS account_name,x.plan_id,x.source_generation,x.config_generation,x.before_values,x.after_values,x.before_sli,x.after_sli,x.effect_status,x.effect_reason,x.window_seconds,x.created_at,x.checked_at,COALESCE(q.last_applied_priority IS NOT NULL OR q.owned_pause OR q.applied_control<>'{}'::jsonb,false) AS has_managed_controls FROM engine_actions x JOIN upstream_accounts a ON a.id=x.account_id LEFT JOIN quality_states q ON q.account_id=a.id WHERE x.owner_id=$1 ORDER BY x.created_at DESC LIMIT 100)v`, identityFrom(r).ID).Scan(&raw)
	if err != nil {
		return err
	}
	writeData(w, 200, json.RawMessage(raw))
	return nil
}
