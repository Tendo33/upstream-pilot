package app

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type taskLeaseContextKey struct{}

var errTaskBusy = errors.New("相关任务正在执行，稍后重试")

func (s *Scheduler) claim(ctx context.Context) (scheduledTask, error) {
	var task scheduledTask
	tx, err := s.app.db.Begin(ctx)
	if err != nil {
		return task, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(7820133791)`).Scan(&locked); err != nil {
		return task, err
	}
	if !locked {
		return task, pgx.ErrNoRows
	}
	err = tx.QueryRow(ctx, `WITH candidates AS(
 SELECT 'site' AS resource,s.id,s.owner_id,'inventory' AS kind,s.next_inventory_at AS due,0 AS priority FROM sites s WHERE s.enabled
 UNION ALL SELECT 'site',s.id,s.owner_id,'reconcile',s.next_reconcile_at,0 FROM sites s WHERE s.enabled
 UNION ALL SELECT 'site',s.id,s.owner_id,'cache-sample',s.next_cache_sample_at,1 FROM sites s WHERE s.enabled AND s.cache_rate_priority_enabled
 UNION ALL SELECT 'site',s.id,s.owner_id,'traffic-site',s.next_traffic_sample_at,1 FROM sites s WHERE s.enabled
 UNION ALL SELECT 'site',s.id,s.owner_id,'usage',s.next_usage_at,1 FROM sites s WHERE s.enabled
 UNION ALL SELECT 'account-work',a.id,s.owner_id,t.kind,t.due,2 FROM upstream_accounts a JOIN sites s ON s.id=a.site_id CROSS JOIN LATERAL(VALUES('probe',a.next_probe_at,a.health_enabled OR a.managed_hold),('rate',a.next_rate_sync_at,a.rate_sync_enabled))t(kind,due,enabled) WHERE s.enabled AND a.deleted_at IS NULL AND t.enabled
 UNION ALL SELECT 'profile',p.id,s.owner_id,'service-canary',p.next_probe_at,2`+serviceProfileJoins+` WHERE s.enabled AND `+serviceProfileParentLive+` AND p.enabled AND(SELECT count(*) FROM task_leases WHERE resource_type='profile' AND lease_until>now())<$1
 ),candidate AS(SELECT c.* FROM candidates c WHERE c.due<=now() AND NOT EXISTS(SELECT 1 FROM task_leases l WHERE l.resource_type=c.resource AND l.resource_id=c.id AND l.lease_until>now()) ORDER BY c.priority,c.due,c.kind,c.id LIMIT 1)
 INSERT INTO task_leases(resource_type,resource_id,owner_id,kind,owner_token,lease_until,started_at) SELECT resource,id,owner_id,kind,gen_random_uuid(),now()+interval '2 minutes',now() FROM candidate ON CONFLICT(resource_type,resource_id) DO UPDATE SET kind=excluded.kind,owner_token=excluded.owner_token,lease_until=excluded.lease_until,started_at=excluded.started_at,finished_at=NULL,last_error='' WHERE task_leases.lease_until<=now() RETURNING resource_type,resource_id::text,kind,owner_token::text`, max(1, s.workers/2)).Scan(&task.Resource, &task.ID, &task.Kind, &task.LeaseToken)
	if err != nil {
		return task, err
	}
	task.RunID = task.LeaseToken
	if _, err = tx.Exec(ctx, `INSERT INTO task_runs(id,owner_id,resource_type,resource_id,kind,started_at) SELECT owner_token,owner_id,resource_type,resource_id,kind,started_at FROM task_leases WHERE resource_type=$1 AND resource_id=$2 AND owner_token=$3`, task.Resource, task.ID, task.LeaseToken); err != nil {
		return task, err
	}
	if task.Kind == "service-canary" {
		if _, err = tx.Exec(ctx, `UPDATE service_profiles SET lease_token=$2,lease_until=now()+interval '2 minutes' WHERE id=$1`, task.ID, task.LeaseToken); err != nil {
			return task, err
		}
	}
	return task, tx.Commit(ctx)
}
func (a *App) requireTaskLease(ctx context.Context) error {
	t, ok := ctx.Value(taskLeaseContextKey{}).(scheduledTask)
	if !ok {
		return nil
	}
	var valid bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_leases WHERE resource_type=$1 AND resource_id=$2 AND owner_token=$3 AND lease_until>now())`, t.Resource, t.ID, t.LeaseToken).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errTaskBusy
	}
	return nil
}
func (s *Scheduler) execute(ctx context.Context, task scheduledTask) {
	// Tests and old internal callers may invoke an unclaimed task directly.
	if task.LeaseToken == "" {
		_ = s.executeTask(ctx, task)
		return
	}
	jobCtx, cancel := context.WithTimeout(context.WithValue(ctx, taskLeaseContextKey{}, task), 12*time.Minute)
	defer cancel()
	done := make(chan struct{})
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(jobCtx, 5*time.Second)
				command, e := s.app.db.Exec(renewCtx, `UPDATE task_leases SET lease_until=now()+interval '2 minutes' WHERE resource_type=$1 AND resource_id=$2 AND owner_token=$3 AND lease_until>now()`, task.Resource, task.ID, task.LeaseToken)
				if e == nil && command.RowsAffected() == 1 && task.Kind == "service-canary" {
					_, e = s.app.db.Exec(renewCtx, `UPDATE service_profiles SET lease_until=now()+interval '2 minutes' WHERE id=$1 AND lease_token=$2`, task.ID, task.LeaseToken)
				}
				stop()
				if e != nil || command.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	err := s.app.requireTaskLease(jobCtx)
	if err == nil {
		err = s.executeTask(jobCtx, task)
	}
	close(done)
	<-renewed
	finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finish()
	message := ""
	if err != nil {
		message = truncateError(err)
	}
	command, e := s.app.db.Exec(finishCtx, `UPDATE task_leases SET lease_until=now(),finished_at=now(),duration_ms=EXTRACT(EPOCH FROM now()-started_at)*1000,last_error=$4,last_success_at=CASE WHEN $4='' THEN now() ELSE last_success_at END WHERE resource_type=$1 AND resource_id=$2 AND owner_token=$3`, task.Resource, task.ID, task.LeaseToken, message)
	if e == nil && command.RowsAffected() == 1 {
		_, _ = s.app.db.Exec(finishCtx, `UPDATE task_runs SET finished_at=now(),success=$2,duration_ms=EXTRACT(EPOCH FROM now()-started_at)*1000,last_error=$3 WHERE id=$1`, task.RunID, err == nil, message)
	}
	if e == nil && command.RowsAffected() == 1 && err != nil {
		s.deferFailedTask(finishCtx, task)
	}
}
func (s *Scheduler) deferFailedTask(ctx context.Context, t scheduledTask) {
	switch t.Kind {
	case "inventory", "reconcile", "cache-sample", "usage", "traffic-site":
		column := map[string]string{"inventory": "next_inventory_at", "reconcile": "next_reconcile_at", "cache-sample": "next_cache_sample_at", "usage": "next_usage_at", "traffic-site": "next_traffic_sample_at"}[t.Kind]
		_, _ = s.app.db.Exec(ctx, `UPDATE sites SET `+column+`=GREATEST(`+column+`,now()+interval '30 seconds') WHERE id=$1`, t.ID)
	case "service-canary":
		_, _ = s.app.db.Exec(ctx, `UPDATE service_profiles SET next_probe_at=GREATEST(next_probe_at,now()+interval '60 seconds') WHERE id=$1 AND lease_token=$2`, t.ID, t.LeaseToken)
	}
}
