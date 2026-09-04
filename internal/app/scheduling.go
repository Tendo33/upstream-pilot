package app

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	accountSchedulingOperationTimeout = 30 * time.Second
	accountSchedulingLockRetryDelay   = 50 * time.Millisecond
	accountSchedulingUnlockTimeout    = 5 * time.Second
)

type accountSchedulingInput struct {
	Schedulable *bool `json:"schedulable"`
}

func (a *App) updateAccountScheduling(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	accountID, err := requiredID(r, "accountID")
	if err != nil {
		return err
	}
	var input accountSchedulingInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if input.Schedulable == nil {
		return &apiError{Status: http.StatusBadRequest, Code: "SCHEDULABLE_REQUIRED", Message: "必须指定是否开启账号调度"}
	}

	requestCtx, cancel := context.WithTimeout(r.Context(), accountSchedulingOperationTimeout)
	defer cancel()
	before, err := a.loadAccountWork(requestCtx, accountID, identity.ID)
	if err != nil {
		return err
	}
	client, err := a.clientForWork(before)
	if err != nil {
		return err
	}
	err = a.withAccountSchedulingLock(requestCtx, accountID, func(connection *pgxpool.Conn) error {
		if loadErr := connection.QueryRow(requestCtx, `
			SELECT a.schedulable,a.managed_hold FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
			WHERE a.id=$1 AND s.owner_id=$2 AND a.deleted_at IS NULL`, accountID, identity.ID).Scan(&before.Schedulable, &before.ManagedHold); loadErr != nil {
			return loadErr
		}
		var schedulingGeneration int64
		if generationErr := connection.QueryRow(requestCtx, `
			UPDATE upstream_accounts a SET scheduling_generation=scheduling_generation+1,updated_at=now()
			FROM sites s
			WHERE a.id=$1 AND a.site_id=s.id AND s.owner_id=$2 AND a.deleted_at IS NULL
			RETURNING a.scheduling_generation`, accountID, identity.ID).Scan(&schedulingGeneration); generationErr != nil {
			return generationErr
		}
		remote, updateErr := client.SetSchedulable(requestCtx, before.RemoteID, *input.Schedulable)
		if updateErr != nil {
			return updateErr
		}
		if remote.Schedulable != *input.Schedulable {
			return errors.New("Sub2API did not persist the requested scheduling state")
		}
		command, updateErr := connection.Exec(requestCtx, `
			UPDATE upstream_accounts a SET
			 schedulable=$3,managed_hold=false,consecutive_recovery_successes=0,
			 health_state=CASE WHEN health_state='paused' THEN CASE WHEN consecutive_failures>0 THEN 'failing' ELSE 'unknown' END ELSE health_state END,
			 updated_at=now()
			FROM sites s
			WHERE a.id=$1 AND a.site_id=s.id AND s.owner_id=$2 AND a.deleted_at IS NULL
			  AND a.scheduling_generation=$4`, accountID, identity.ID, *input.Schedulable, schedulingGeneration)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() == 0 {
			return fmt.Errorf("account disappeared while updating scheduling")
		}
		if _, updateErr = a.db.Exec(requestCtx, `UPDATE quality_policies SET config=jsonb_set(config,'{auto_pause}','false'),updated_at=now() WHERE account_id=$1`, accountID); updateErr != nil {
			return updateErr
		}
		_, updateErr = a.db.Exec(requestCtx, `UPDATE quality_states SET owned_pause=false,baseline_control=baseline_control-'schedulable',applied_control=applied_control-'schedulable',pending_control=jsonb_set(jsonb_set(pending_control,'{from}',COALESCE(pending_control->'from','{}')-'schedulable'),'{to}',COALESCE(pending_control->'to','{}')-'schedulable') WHERE account_id=$1`, accountID)
		return updateErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "SCHEDULING_UPDATE_FAILED", Message: fmt.Sprintf("账号调度更新失败：%v", err)}
	}

	account, err := a.getAccount(r.Context(), accountID, identity.ID)
	if err != nil {
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, account.SiteID, accountID, "account.scheduling.update", "success", map[string]any{
		"before_schedulable":  before.Schedulable,
		"before_managed_hold": before.ManagedHold,
		"schedulable":         *input.Schedulable,
	})
	writeData(w, http.StatusOK, account)
	return nil
}

type accountSchedulingLockPool interface {
	Acquire(context.Context) (accountSchedulingLockConnection, error)
}

type accountSchedulingLockConnection interface {
	TryLock(context.Context, int32, int32) (bool, error)
	Unlock(context.Context, int32, int32) (bool, error)
	PooledConnection() *pgxpool.Conn
	Release()
	Discard(context.Context) error
}

type pgxAccountSchedulingLockPool struct {
	pool *pgxpool.Pool
}

func (p pgxAccountSchedulingLockPool) Acquire(ctx context.Context) (accountSchedulingLockConnection, error) {
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &pgxAccountSchedulingLockConnection{connection: connection}, nil
}

type pgxAccountSchedulingLockConnection struct {
	connection *pgxpool.Conn
}

func (c *pgxAccountSchedulingLockConnection) TryLock(ctx context.Context, first, second int32) (bool, error) {
	var acquired bool
	err := c.connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1,$2)`, first, second).Scan(&acquired)
	return acquired, err
}

func (c *pgxAccountSchedulingLockConnection) Unlock(ctx context.Context, first, second int32) (bool, error) {
	var unlocked bool
	err := c.connection.QueryRow(ctx, `SELECT pg_advisory_unlock($1,$2)`, first, second).Scan(&unlocked)
	return unlocked, err
}

func (c *pgxAccountSchedulingLockConnection) PooledConnection() *pgxpool.Conn {
	return c.connection
}

func (c *pgxAccountSchedulingLockConnection) Release() {
	c.connection.Release()
}

func (c *pgxAccountSchedulingLockConnection) Discard(ctx context.Context) error {
	return c.connection.Hijack().Close(ctx)
}

func (a *App) withAccountSchedulingLock(ctx context.Context, accountID string, operation func(*pgxpool.Conn) error) error {
	var site string
	if err := a.db.QueryRow(ctx, `SELECT site_id::text FROM upstream_accounts WHERE id=$1`, accountID).Scan(&site); err != nil {
		return err
	}
	return a.withSiteSchedulingLock(ctx, site, operation)
}

func (a *App) withSiteSchedulingLock(ctx context.Context, siteID string, operation func(*pgxpool.Conn) error) error {
	// Reserve pool headroom for queries performed while a session lock is held.
	if a.controlSlots != nil {
		select {
		case a.controlSlots <- struct{}{}:
			defer func() { <-a.controlSlots }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	first, second, err := accountSchedulingLockKeys(siteID)
	if err != nil {
		return err
	}
	return withAccountSchedulingLockPool(ctx, pgxAccountSchedulingLockPool{pool: a.db}, first, second, accountSchedulingLockRetryDelay, func(connection accountSchedulingLockConnection) error {
		return operation(connection.PooledConnection())
	})
}

func accountSchedulingLockKeys(accountID string) (int32, int32, error) {
	parsed, err := uuid.Parse(accountID)
	if err != nil {
		return 0, 0, err
	}
	first := int32(binary.BigEndian.Uint32(parsed[0:4]) ^ binary.BigEndian.Uint32(parsed[8:12]))
	second := int32(binary.BigEndian.Uint32(parsed[4:8]) ^ binary.BigEndian.Uint32(parsed[12:16]))
	return first, second, nil
}

func withAccountSchedulingLockPool(
	ctx context.Context,
	pool accountSchedulingLockPool,
	first, second int32,
	retryDelay time.Duration,
	operation func(accountSchedulingLockConnection) error,
) (resultErr error) {
	if retryDelay <= 0 {
		retryDelay = time.Millisecond
	}
	// A session lock must stay on one connection, but contenders must not. Each
	// failed try returns its connection before waiting for the next attempt.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		acquired, lockErr := connection.TryLock(ctx, first, second)
		if lockErr != nil {
			return errors.Join(lockErr, discardAccountSchedulingLockConnection(connection))
		}
		if !acquired {
			connection.Release()
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}

		defer func() {
			resultErr = errors.Join(resultErr, releaseAccountSchedulingLockConnection(connection, first, second))
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation(connection)
	}
}

func releaseAccountSchedulingLockConnection(connection accountSchedulingLockConnection, first, second int32) error {
	unlockCtx, cancel := context.WithTimeout(context.Background(), accountSchedulingUnlockTimeout)
	unlocked, unlockErr := connection.Unlock(unlockCtx, first, second)
	cancel()
	if unlockErr == nil && unlocked {
		connection.Release()
		return nil
	}
	if unlockErr == nil {
		unlockErr = errors.New("account scheduling advisory lock was not held during release")
	}
	return errors.Join(fmt.Errorf("release account scheduling lock: %w", unlockErr), discardAccountSchedulingLockConnection(connection))
}

func discardAccountSchedulingLockConnection(connection accountSchedulingLockConnection) error {
	closeCtx, cancel := context.WithTimeout(context.Background(), accountSchedulingUnlockTimeout)
	defer cancel()
	if err := connection.Discard(closeCtx); err != nil {
		return fmt.Errorf("discard account scheduling lock connection: %w", err)
	}
	return nil
}
