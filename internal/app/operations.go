package app

import (
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/database"
	"net/http"
	"runtime"
	"time"
)

func (a *App) operationsHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r)
	var sites, tasks []byte
	err := a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT s.id,s.name,s.connection_state,s.last_inventory_at,s.last_reconcile_at,s.usage_collection,s.traffic_collection,GREATEST(0,EXTRACT(EPOCH FROM now()-s.next_inventory_at))::bigint AS inventory_lag_seconds,GREATEST(0,EXTRACT(EPOCH FROM now()-s.next_reconcile_at))::bigint AS reconcile_lag_seconds FROM sites s WHERE s.owner_id=$1 ORDER BY s.name)v`, owner.ID).Scan(&sites)
	if err != nil {
		return err
	}
	err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT l.kind,l.resource_type,l.resource_id,l.started_at,l.finished_at,(SELECT max(ok.finished_at) FROM task_runs ok WHERE ok.owner_id=l.owner_id AND ok.resource_type=l.resource_type AND ok.kind=l.kind AND ok.success) AS last_success_at,l.duration_ms,l.last_error,l.finished_at IS NULL AS running FROM task_runs l WHERE l.owner_id=$1 ORDER BY l.started_at DESC LIMIT 30)v`, owner.ID).Scan(&tasks)
	if err != nil {
		return err
	}
	pool := a.db.Stat()
	result := map[string]any{"sites": json.RawMessage(sites), "tasks": json.RawMessage(tasks), "workers": a.config.Workers, "pool": map[string]any{"total": pool.TotalConns(), "idle": pool.IdleConns(), "acquired": pool.AcquiredConns(), "acquire_count": pool.AcquireCount(), "empty_acquires": pool.EmptyAcquireCount()}, "database": database.Metrics(a.db), "checked_at": time.Now().UTC()}
	if owner.Role == "admin" {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		result["process"] = map[string]any{"heap_mb": float64(memory.HeapAlloc) / (1024 * 1024), "goroutines": runtime.NumGoroutine()}
	}
	writeData(w, 200, result)
	return nil
}
