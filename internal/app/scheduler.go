package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type Scheduler struct {
	app     *App
	workers int
	sem     chan struct{}
	wg      sync.WaitGroup
}

type scheduledTask struct {
	Resource   string
	RunID      string
	LeaseToken string
	Kind       string
	ID         string
}

func (a *App) NewScheduler() *Scheduler {
	return &Scheduler{app: a, workers: a.config.Workers, sem: make(chan struct{}, a.config.Workers)}
}

func (s *Scheduler) Run(ctx context.Context) {
	s.app.logger.Info("scheduler started", slog.Int("workers", s.workers))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.app.runAccountBalanceRefresher(ctx)
	}()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.app.runNotifications(ctx) }()
	s.cleanup(ctx)
	ticker := time.NewTicker(time.Second)
	cleanup := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			s.app.logger.Info("scheduler stopped")
			return
		case <-cleanup.C:
			s.cleanup(ctx)
		case <-ticker.C:
			s.fill(ctx)
		}
	}
}

func (s *Scheduler) fill(ctx context.Context) {
	for {
		select {
		case s.sem <- struct{}{}:
		default:
			return
		}
		task, err := s.claim(ctx)
		if err != nil {
			<-s.sem
			if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
				s.app.logger.Error("scheduler claim failed", slog.Any("error", err))
			}
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			s.execute(ctx, task)
		}()
	}
}

func (s *Scheduler) executeTask(ctx context.Context, task scheduledTask) error {
	var err error
	switch task.Kind {
	case "inventory":
		err = s.app.syncSite(ctx, task.ID, "", "", "scheduled")
		if err != nil {
			_, _ = s.app.db.Exec(ctx, `UPDATE sites SET inventory_lease_until=NULL,next_inventory_at=now()+inventory_interval_seconds*interval '1 second' WHERE id=$1`, task.ID)
		}
	case "cache-sample":
		err = s.app.sampleSiteCacheRates(ctx, task.ID, "")
		if err != nil {
			_, _ = s.app.db.Exec(ctx, `UPDATE sites SET cache_sample_lease_until=NULL,next_cache_sample_at=now()+interval '60 seconds' WHERE id=$1`, task.ID)
		}
	case "reconcile":
		_, err = s.app.reconcileSite(ctx, task.ID, "", "")
	case "service-canary":
		_, err = s.app.runServiceCanary(ctx, task.ID, "", true, task.LeaseToken)
		_, updateErr := s.app.db.Exec(ctx, `UPDATE service_profiles SET lease_until=NULL,lease_token=NULL,next_probe_at=GREATEST(next_probe_at,now()+interval '60 seconds') WHERE id=$1 AND lease_token=$2::uuid`, task.ID, task.LeaseToken)
		err = errors.Join(err, updateErr)
	case "usage":
		err = s.app.collectSiteUsage(ctx, task.ID, "")
		_, _ = s.app.db.Exec(ctx, `UPDATE sites SET usage_lease_until=NULL,next_usage_at=now()+interval '5 minutes' WHERE id=$1`, task.ID)
	case "traffic-site":
		err = s.app.collectSiteTraffic(ctx, task.ID, "")
	case "traffic":
		var work AccountWork
		work, err = s.app.loadAccountWork(ctx, task.ID, "")
		if err == nil {
			err = s.app.sampleQualityTraffic(ctx, work)
		}
		_, updateErr := s.app.db.Exec(ctx, `UPDATE upstream_accounts SET traffic_lease_until=NULL,next_traffic_at=now()+interval '60 seconds' WHERE id=$1`, task.ID)
		err = errors.Join(err, updateErr)
	case "probe", "rate":
		var work AccountWork
		work, err = s.app.loadAccountWork(ctx, task.ID, "")
		if err == nil && task.Kind == "probe" {
			_, err = s.app.runProbe(ctx, work, "scheduled", "")
		} else if err == nil {
			_, err = s.app.runRateSync(ctx, work, "")
		}
		if err == nil && task.Kind == "probe" {
			_, err = s.app.db.Exec(ctx, `UPDATE upstream_accounts SET work_lease_until=NULL WHERE id=$1`, task.ID)
		}
		if err != nil {
			column := "next_probe_at"
			intervalColumn := "probe_interval_seconds"
			if task.Kind == "rate" {
				column, intervalColumn = "next_rate_sync_at", "rate_sync_interval_seconds"
			}
			_, _ = s.app.db.Exec(ctx, `UPDATE upstream_accounts SET work_lease_until=NULL,`+column+`=now()+`+intervalColumn+`*interval '1 second',last_error=$2,updated_at=now() WHERE id=$1 AND source_generation=$3`, task.ID, truncateError(err), work.SourceGeneration)
		}
	}
	if err != nil && ctx.Err() == nil {
		s.app.logger.Warn("scheduled task failed", slog.String("kind", task.Kind), slog.String("id", task.ID), slog.Any("error", err))
	}
	return err
}

func (s *Scheduler) cleanup(ctx context.Context) {
	_, _ = s.app.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at<now()`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM probe_attempts WHERE created_at<now()-interval '90 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM quality_decisions WHERE created_at<now()-interval '90 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM quality_notifications WHERE created_at<now()-interval '90 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM account_cache_samples WHERE sampled_at<now()-interval '48 hours'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM auth_throttles WHERE updated_at<now()-interval '24 hours'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM service_canary_runs WHERE started_at<now()-interval '90 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM balance_observations WHERE checked_at<now()-interval '90 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM usage_observations WHERE created_at<now()-interval '7 days'`)
	_, _ = s.app.db.Exec(ctx, `DELETE FROM request_outcome_observations WHERE seen_at<now()-interval '7 days'`)
	s.purgeAuditLogs(ctx)
}

func (s *Scheduler) purgeAuditLogs(ctx context.Context) {
	rows, err := s.app.db.Query(ctx, `SELECT owner_id::text, retention_days FROM audit_log_settings`)
	if err != nil {
		if ctx.Err() == nil {
			s.app.logger.Error("audit log retention query failed", slog.Any("error", err))
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ownerID string
		var retainDays int
		if err := rows.Scan(&ownerID, &retainDays); err != nil {
			s.app.logger.Error("audit log retention scan failed", slog.Any("error", err))
			return
		}
		if _, err := s.app.purgeOwnerAuditLogs(ctx, ownerID, retainDays); err != nil && ctx.Err() == nil {
			s.app.logger.Warn("audit log purge failed", slog.String("owner_id", ownerID), slog.Any("error", err))
		}
	}
}

func truncateError(err error) string {
	message := err.Error()
	if len(message) > 1000 {
		return message[:1000]
	}
	return message
}
