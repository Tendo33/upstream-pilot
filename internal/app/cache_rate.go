package app

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"sub2api-upstream-manager/internal/upstream"
)

const (
	defaultCacheRateWindowSeconds = 3600
	minCacheRateWindowSeconds     = 300
	maxCacheRateWindowSeconds     = 86400
	maxPriorityWeight             = 100
	cacheSampleInterval           = 60 * time.Second
	cacheSampleMinGap             = 20 * time.Second
	cacheSampleConcurrency        = 8
	cacheSampleTimeout            = 15 * time.Second
	cacheSampleLookbackFactor     = 2
)

type reconcilePlanOptions struct {
	Start        int
	Step         int
	CacheEnabled bool
	RateWeight   float64
	CacheWeight  float64
}

type cacheSample struct {
	AccountID string
	At        time.Time
	Input     int64
	Creation  int64
	Read      int64
}

type cacheRateSnapshot struct {
	Rate      *float64
	Tokens    int64
	SampledAt time.Time
}

func cacheRateFromSamples(samples []cacheSample, now time.Time, window time.Duration) (float64, int64, bool) {
	if len(samples) == 0 || window <= 0 {
		return 0, 0, false
	}
	sorted := append([]cacheSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	latest := sorted[len(sorted)-1]
	cutoff := now.Add(-window)
	anchor := sorted[0]
	for _, sample := range sorted {
		if !sample.At.After(cutoff) {
			anchor = sample
		}
	}
	useCumulative := latest.At.Equal(anchor.At) || latest.Input < anchor.Input || latest.Creation < anchor.Creation || latest.Read < anchor.Read
	var input, creation, read int64
	if useCumulative {
		input, creation, read = latest.Input, latest.Creation, latest.Read
	} else {
		input = latest.Input - anchor.Input
		creation = latest.Creation - anchor.Creation
		read = latest.Read - anchor.Read
	}
	input = max64(input, 0)
	creation = max64(creation, 0)
	read = max64(read, 0)
	denom := input + creation + read
	if denom <= 0 {
		return 0, 0, false
	}
	return float64(read) / float64(denom), denom, true
}

func assignWeightedPriorities(sortable []*reconcileAccount, start, step int, rateWeight, cacheWeight float64) {
	if len(sortable) == 0 {
		return
	}
	cacheSum := 0.0
	cacheCount := 0
	for _, account := range sortable {
		if account.CacheRate != nil && isFinite(*account.CacheRate) {
			cacheSum += clampUnit(*account.CacheRate)
			cacheCount++
		}
	}
	meanCache := 0.0
	if cacheCount > 0 {
		meanCache = cacheSum / float64(cacheCount)
	}
	type scored struct {
		account *reconcileAccount
		score   float64
	}
	scoredAccounts := make([]scored, 0, len(sortable))
	for _, account := range sortable {
		cacheRate := meanCache
		if account.CacheRate != nil && isFinite(*account.CacheRate) {
			cacheRate = clampUnit(*account.CacheRate)
		}
		score := rateWeight*(*account.Rate) + cacheWeight*(1-cacheRate)
		scoredAccounts = append(scoredAccounts, scored{account: account, score: score})
	}
	sort.SliceStable(scoredAccounts, func(i, j int) bool {
		if math.Abs(scoredAccounts[i].score-scoredAccounts[j].score) <= 1e-9 {
			return scoredAccounts[i].account.RemoteID < scoredAccounts[j].account.RemoteID
		}
		return scoredAccounts[i].score < scoredAccounts[j].score
	})
	rank := -1
	previous := 0.0
	for index, item := range scoredAccounts {
		if index == 0 || math.Abs(item.score-previous) > 1e-9 {
			rank++
			previous = item.score
		}
		item.account.Desired = start + rank*step
	}
}

func clampUnit(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func max64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

func (a *App) sampleSiteCacheRates(ctx context.Context, siteID, ownerFilter string) error {
	return a.sampleCacheRates(ctx, siteID, ownerFilter, "", false)
}

func (a *App) sampleCacheRates(ctx context.Context, siteID, ownerFilter, accountID string, force bool) error {
	site, err := a.siteSecret(ctx, siteID, ownerFilter)
	if err != nil {
		return err
	}
	var windowSeconds int
	if err := a.db.QueryRow(ctx, `SELECT cache_rate_window_seconds FROM sites WHERE id=$1`, siteID).Scan(&windowSeconds); err != nil {
		return err
	}
	if windowSeconds < minCacheRateWindowSeconds {
		windowSeconds = defaultCacheRateWindowSeconds
	}
	rows, err := a.db.Query(ctx, `
		SELECT a.id,a.remote_id
		FROM upstream_accounts a
		WHERE a.site_id=$1 AND a.deleted_at IS NULL
		  AND ($2='' OR a.id=$2::uuid)
		  AND ($3 OR a.priority_enabled)
		ORDER BY a.remote_id`, siteID, accountID, force || accountID != "")
	if err != nil {
		return err
	}
	type sampleTarget struct {
		ID       string
		RemoteID int64
	}
	targets := make([]sampleTarget, 0)
	for rows.Next() {
		var target sampleTarget
		if err := rows.Scan(&target.ID, &target.RemoteID); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	statsByAccount := make(map[string]upstream.AccountUsageStats, len(targets))
	if len(targets) > 0 {
		client, err := a.sub2Client(site)
		if err != nil {
			return err
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, cacheSampleConcurrency)
		for _, target := range targets {
			target := target
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				requestCtx, cancel := context.WithTimeout(ctx, cacheSampleTimeout)
				defer cancel()
				stats, err := client.AccountUsageStats(requestCtx, target.RemoteID)
				if err != nil {
					a.logger.Warn("cache rate sample failed", slog.String("site_id", siteID), slog.Int64("remote_id", target.RemoteID), slog.Any("error", err))
					return
				}
				mu.Lock()
				statsByAccount[target.ID] = stats
				mu.Unlock()
			}()
		}
		wg.Wait()
	}

	now := time.Now().UTC()
	for _, target := range targets {
		stats, ok := statsByAccount[target.ID]
		if !ok {
			continue
		}
		if !force {
			var lastAt time.Time
			err := a.db.QueryRow(ctx, `
				SELECT sampled_at FROM account_cache_samples
				WHERE account_id=$1 ORDER BY sampled_at DESC LIMIT 1`, target.ID).Scan(&lastAt)
			if err == nil && now.Sub(lastAt) < cacheSampleMinGap {
				continue
			}
		}
		if _, err := a.db.Exec(ctx, `
			INSERT INTO account_cache_samples(account_id,site_id,sampled_at,input_tokens,cache_creation_tokens,cache_read_tokens)
			VALUES($1,$2,$3,$4,$5,$6)`, target.ID, siteID, now, stats.TotalInputTokens, stats.TotalCacheCreationTokens, stats.TotalCacheReadTokens); err != nil {
			return err
		}
	}

	snapshots, err := a.computeSiteCacheRates(ctx, siteID, time.Duration(windowSeconds)*time.Second, now)
	if err != nil {
		return err
	}
	for _, target := range targets {
		snapshot, ok := snapshots[target.ID]
		if !ok {
			if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET cache_rate=NULL,cache_rate_tokens=0,cache_rate_sampled_at=$2,updated_at=now() WHERE id=$1`, target.ID, now); err != nil {
				return err
			}
			continue
		}
		if _, err := a.db.Exec(ctx, `UPDATE upstream_accounts SET cache_rate=$2,cache_rate_tokens=$3,cache_rate_sampled_at=$4,updated_at=now() WHERE id=$1`, target.ID, snapshot.Rate, snapshot.Tokens, snapshot.SampledAt); err != nil {
			return err
		}
	}
	_, _ = a.db.Exec(ctx, `UPDATE sites SET last_cache_sample_at=$2,next_cache_sample_at=$2+interval '60 seconds',cache_sample_lease_until=NULL,updated_at=now() WHERE id=$1`, siteID, now)
	return nil
}

func (a *App) computeSiteCacheRates(ctx context.Context, siteID string, window time.Duration, now time.Time) (map[string]cacheRateSnapshot, error) {
	lookback := window * cacheSampleLookbackFactor
	if lookback < window {
		lookback = window
	}
	rows, err := a.db.Query(ctx, `
		SELECT account_id::text,sampled_at,input_tokens,cache_creation_tokens,cache_read_tokens
		FROM account_cache_samples
		WHERE site_id=$1 AND sampled_at>=$2
		ORDER BY account_id,sampled_at`, siteID, now.Add(-lookback))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := make(map[string][]cacheSample)
	for rows.Next() {
		var sample cacheSample
		if err := rows.Scan(&sample.AccountID, &sample.At, &sample.Input, &sample.Creation, &sample.Read); err != nil {
			return nil, err
		}
		grouped[sample.AccountID] = append(grouped[sample.AccountID], sample)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make(map[string]cacheRateSnapshot, len(grouped))
	for accountID, samples := range grouped {
		rate, tokens, ok := cacheRateFromSamples(samples, now, window)
		if !ok {
			continue
		}
		value := rate
		result[accountID] = cacheRateSnapshot{Rate: &value, Tokens: tokens, SampledAt: now}
	}
	return result, nil
}
