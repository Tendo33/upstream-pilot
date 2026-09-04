package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountSchedulingLockWaiterDoesNotRetainPoolConnection(t *testing.T) {
	pool := newFakeAccountSchedulingLockPool(2)
	holderRelease := make(chan struct{})
	holderStarted := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- withAccountSchedulingLockPool(context.Background(), pool, 1, 2, time.Millisecond, func(accountSchedulingLockConnection) error {
			close(holderStarted)
			<-holderRelease
			return nil
		})
	}()
	waitForSignal(t, holderStarted, time.Second, "holder did not acquire the advisory lock")

	waiterCtx, cancelWaiter := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelWaiter()
	waiterCalled := false
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- withAccountSchedulingLockPool(waiterCtx, pool, 1, 2, 5*time.Millisecond, func(accountSchedulingLockConnection) error {
			waiterCalled = true
			return nil
		})
	}()
	waitForSignal(t, pool.failedTry, time.Second, "waiter did not attempt the advisory lock")

	// The holder uses one of two slots. A waiter that blocks while retaining its
	// slot would prevent this unrelated database acquisition from succeeding.
	unrelatedCtx, cancelUnrelated := context.WithTimeout(context.Background(), 75*time.Millisecond)
	unrelated, err := pool.Acquire(unrelatedCtx)
	cancelUnrelated()
	if err != nil {
		t.Fatalf("unrelated connection was starved by lock waiter: %v", err)
	}
	unrelated.Release()

	if err := <-waiterDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v, want deadline exceeded", err)
	}
	if waiterCalled {
		t.Fatal("timed-out waiter unexpectedly ran the protected operation")
	}
	if active := pool.activeConnections(); active != 1 {
		t.Fatalf("active connections while holder is running = %d, want 1", active)
	}

	close(holderRelease)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder operation: %v", err)
	}
	if active := pool.activeConnections(); active != 0 {
		t.Fatalf("active connections after release = %d, want 0", active)
	}
}

func TestAccountSchedulingLockDeadlineCoversWait(t *testing.T) {
	pool := newFakeAccountSchedulingLockPool(2)
	holderRelease := make(chan struct{})
	holderStarted := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- withAccountSchedulingLockPool(context.Background(), pool, 3, 4, time.Millisecond, func(accountSchedulingLockConnection) error {
			close(holderStarted)
			<-holderRelease
			return nil
		})
	}()
	waitForSignal(t, holderStarted, time.Second, "holder did not acquire the advisory lock")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	operationCalled := false
	err := withAccountSchedulingLockPool(ctx, pool, 3, 4, 5*time.Millisecond, func(accountSchedulingLockConnection) error {
		operationCalled = true
		return nil
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want deadline exceeded", err)
	}
	if operationCalled {
		t.Fatal("operation ran without acquiring the lock")
	}
	if elapsed < 40*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("deadline elapsed = %v, want approximately 60ms", elapsed)
	}
	if active := pool.activeConnections(); active != 1 {
		t.Fatalf("timed-out waiter retained a connection; active = %d", active)
	}

	close(holderRelease)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder operation: %v", err)
	}
}

func TestAccountSchedulingLockCancellationStillUnlocks(t *testing.T) {
	pool := newFakeAccountSchedulingLockPool(1)
	ctx, cancel := context.WithCancel(context.Background())
	operationStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withAccountSchedulingLockPool(ctx, pool, 5, 6, time.Millisecond, func(accountSchedulingLockConnection) error {
			close(operationStarted)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	waitForSignal(t, operationStarted, time.Second, "operation did not acquire the advisory lock")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("operation error = %v, want context canceled", err)
	}
	active, held, unlocks, discards := pool.snapshot()
	if active != 0 || held || unlocks != 1 || discards != 0 {
		t.Fatalf("state after cancellation = active:%d held:%t unlocks:%d discards:%d", active, held, unlocks, discards)
	}
}

func TestAccountSchedulingLockDeadlineCoversProtectedOperation(t *testing.T) {
	pool := newFakeAccountSchedulingLockPool(1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := withAccountSchedulingLockPool(ctx, pool, 9, 10, time.Millisecond, func(accountSchedulingLockConnection) error {
		<-ctx.Done()
		return ctx.Err()
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("operation error = %v, want deadline exceeded", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("operation deadline elapsed = %v, want approximately 60ms", elapsed)
	}
	active, held, unlocks, discards := pool.snapshot()
	if active != 0 || held || unlocks != 1 || discards != 0 {
		t.Fatalf("state after operation deadline = active:%d held:%t unlocks:%d discards:%d", active, held, unlocks, discards)
	}
}

func TestAccountSchedulingLockDiscardsSessionWhenUnlockFails(t *testing.T) {
	pool := newFakeAccountSchedulingLockPool(1)
	unlockFailure := errors.New("fixture unlock failure")
	pool.setUnlockError(unlockFailure)
	err := withAccountSchedulingLockPool(context.Background(), pool, 7, 8, time.Millisecond, func(accountSchedulingLockConnection) error {
		return nil
	})
	if !errors.Is(err, unlockFailure) {
		t.Fatalf("release error = %v, want unlock failure", err)
	}
	active, held, unlocks, discards := pool.snapshot()
	if active != 0 || held || unlocks != 0 || discards != 1 {
		t.Fatalf("state after failed unlock = active:%d held:%t unlocks:%d discards:%d", active, held, unlocks, discards)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatal(failure)
	}
}

type fakeAccountSchedulingLockPool struct {
	slots     chan struct{}
	failedTry chan struct{}

	mu          sync.Mutex
	held        bool
	active      int
	unlocks     int
	discards    int
	unlockError error
	failureOnce sync.Once
}

func newFakeAccountSchedulingLockPool(capacity int) *fakeAccountSchedulingLockPool {
	return &fakeAccountSchedulingLockPool{
		slots:     make(chan struct{}, capacity),
		failedTry: make(chan struct{}),
	}
}

func (p *fakeAccountSchedulingLockPool) Acquire(ctx context.Context) (accountSchedulingLockConnection, error) {
	select {
	case p.slots <- struct{}{}:
		p.mu.Lock()
		p.active++
		p.mu.Unlock()
		return &fakeAccountSchedulingLockConnection{pool: p}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *fakeAccountSchedulingLockPool) activeConnections() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func (p *fakeAccountSchedulingLockPool) snapshot() (active int, held bool, unlocks int, discards int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active, p.held, p.unlocks, p.discards
}

func (p *fakeAccountSchedulingLockPool) setUnlockError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unlockError = err
}

type fakeAccountSchedulingLockConnection struct {
	pool *fakeAccountSchedulingLockPool
	owns bool
	done bool
}

func (c *fakeAccountSchedulingLockConnection) TryLock(ctx context.Context, _, _ int32) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if c.pool.held {
		c.pool.failureOnce.Do(func() { close(c.pool.failedTry) })
		return false, nil
	}
	c.pool.held = true
	c.owns = true
	return true, nil
}

func (c *fakeAccountSchedulingLockConnection) Unlock(ctx context.Context, _, _ int32) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if c.pool.unlockError != nil {
		return false, c.pool.unlockError
	}
	if !c.owns || !c.pool.held {
		return false, nil
	}
	c.pool.held = false
	c.owns = false
	c.pool.unlocks++
	return true, nil
}

func (c *fakeAccountSchedulingLockConnection) PooledConnection() *pgxpool.Conn { return nil }

func (c *fakeAccountSchedulingLockConnection) Release() {
	c.finish(false)
}

func (c *fakeAccountSchedulingLockConnection) Discard(context.Context) error {
	c.finish(true)
	return nil
}

func (c *fakeAccountSchedulingLockConnection) finish(discard bool) {
	c.pool.mu.Lock()
	if c.done {
		c.pool.mu.Unlock()
		return
	}
	c.done = true
	if discard {
		c.pool.discards++
		if c.owns {
			c.pool.held = false
			c.owns = false
		}
	}
	c.pool.active--
	c.pool.mu.Unlock()
	<-c.pool.slots
}
