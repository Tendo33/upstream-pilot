package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

type PriceCard struct {
	Currency        string    `json:"currency"`
	CurrencyToUSD   float64   `json:"currency_to_usd"`
	TokenUnit       int       `json:"token_unit"`
	Input           float64   `json:"input"`
	Output          float64   `json:"output"`
	CacheRead       *float64  `json:"cache_read"`
	CacheWrite      *float64  `json:"cache_write"`
	TokenConvention string    `json:"token_convention"`
	ApplyMultiplier bool      `json:"apply_multiplier"`
	Basis           string    `json:"basis"`
	Confirmed       bool      `json:"confirmed"`
	ValidUntil      time.Time `json:"valid_until"`
}

func (p PriceCard) Validate() error {
	if len(p.Currency) != 3 || strings.ToUpper(p.Currency) != p.Currency || !isFinite(p.CurrencyToUSD) || p.CurrencyToUSD <= 0 || p.CurrencyToUSD > 1e9 || (p.Currency == "USD" && p.CurrencyToUSD != 1) || (p.TokenUnit != 1000 && p.TokenUnit != 1000000) {
		return errors.New("币种、换算或 token 单位无效")
	}
	for _, v := range []*float64{&p.Input, &p.Output, p.CacheRead, p.CacheWrite} {
		if v != nil && (!isFinite(*v) || *v < 0 || *v > 1e9) {
			return errors.New("模型价格必须是非负有限数值")
		}
	}
	if p.TokenConvention != "disjoint" && p.TokenConvention != "input_includes_cache" {
		return errors.New("请确认此站点 usage 的输入/缓存计数口径")
	}
	if p.Basis == "" || len(p.Basis) > 256 || p.ValidUntil.Before(time.Now()) || p.ValidUntil.After(time.Now().Add(366*24*time.Hour)) {
		return errors.New("请注明计价依据和一年内的有效截止时间")
	}
	return nil
}

type TokenMix struct {
	Samples    int        `json:"samples"`
	Input      float64    `json:"input"`
	Output     float64    `json:"output"`
	CacheRead  float64    `json:"cache_read"`
	CacheWrite float64    `json:"cache_write"`
	LatestAt   *time.Time `json:"latest_at"`
}
type ComparableCost struct {
	SourceGeneration int64      `json:"source_generation"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason"`
	USDPerMillion    *float64   `json:"usd_per_million"`
	Basis            string     `json:"basis"`
	ValidUntil       *time.Time `json:"valid_until"`
	Mix              TokenMix   `json:"mix"`
}

func normalizedCost(card PriceCard, mix TokenMix, recharge float64, multiplier *float64, now time.Time) ComparableCost {
	v := ComparableCost{Status: "unknown", Reason: "价格或流量结构不足，无法比较", Mix: mix}
	if !card.Confirmed || !now.Before(card.ValidUntil) || !isFinite(recharge) || recharge <= 0 || mix.Samples < 5 || mix.LatestAt == nil || now.Sub(*mix.LatestAt) > 30*time.Minute {
		return v
	}
	input := mix.Input
	if card.TokenConvention == "input_includes_cache" {
		input -= mix.CacheRead + mix.CacheWrite
		if input < 0 {
			return v
		}
	}
	if card.TokenConvention != "disjoint" && card.TokenConvention != "input_includes_cache" {
		return v
	}
	if mix.CacheRead > 0 && card.CacheRead == nil || mix.CacheWrite > 0 && card.CacheWrite == nil {
		return v
	}
	total := input + mix.Output + mix.CacheRead + mix.CacheWrite
	if total <= 0 || card.TokenUnit <= 0 || card.CurrencyToUSD <= 0 {
		return v
	}
	amount := input*card.Input + mix.Output*card.Output
	if card.CacheRead != nil {
		amount += mix.CacheRead * *card.CacheRead
	}
	if card.CacheWrite != nil {
		amount += mix.CacheWrite * *card.CacheWrite
	}
	if card.ApplyMultiplier {
		if multiplier == nil {
			return v
		}
		amount *= *multiplier
	}
	cost := amount / float64(card.TokenUnit) * card.CurrencyToUSD / recharge / total * 1e6
	if !isFinite(cost) || cost < 0 {
		return v
	}
	deadline := card.ValidUntil
	fresh := mix.LatestAt.Add(30 * time.Minute)
	if fresh.Before(deadline) {
		deadline = fresh
	}
	v.Status = "comparable"
	v.Reason = "按同一分组观测用量结构折算，属于采购预估"
	v.USDPerMillion = &cost
	v.Basis = "USD/1M:" + card.TokenConvention
	v.ValidUntil = &deadline
	return v
}

func (a *App) poolEconomics(ctx context.Context, site string, works []engineWork) (map[string]map[string]ComparableCost, error) {
	rows, err := a.db.Query(ctx, `SELECT g.id::text,u.model,count(*),COALESCE(sum(u.input_tokens),0),COALESCE(sum(u.output_tokens),0),COALESCE(sum(u.cache_read_tokens),0),COALESCE(sum(u.cache_write_tokens),0),max(u.created_at) FROM usage_observations u JOIN sites s ON s.id=u.site_id AND s.telemetry_generation=u.site_generation JOIN upstream_groups g ON g.site_id=u.site_id AND g.remote_id=u.group_remote_id WHERE u.site_id=$1 AND NOT u.synthetic AND u.created_at>now()-interval '30 minutes' GROUP BY g.id,u.model`, site)
	if err != nil {
		return nil, err
	}
	mixes := map[string]TokenMix{}
	for rows.Next() {
		var group, model string
		var m TokenMix
		if err = rows.Scan(&group, &model, &m.Samples, &m.Input, &m.Output, &m.CacheRead, &m.CacheWrite, &m.LatestAt); err != nil {
			rows.Close()
			return nil, err
		}
		mixes[poolKey(group, model)] = m
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = a.db.Query(ctx, `SELECT c.account_id::text,c.model,c.config FROM model_price_cards c JOIN upstream_accounts a ON a.id=c.account_id WHERE a.site_id=$1 AND c.source_generation=a.source_generation`, site)
	if err != nil {
		return nil, err
	}
	cards := map[string]PriceCard{}
	for rows.Next() {
		var id, model string
		var raw []byte
		if err = rows.Scan(&id, &model, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var c PriceCard
		if err = json.Unmarshal(raw, &c); err != nil {
			rows.Close()
			return nil, err
		}
		cards[id+"/"+model] = c
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := map[string]map[string]ComparableCost{}
	for _, w := range works {
		costs := map[string]ComparableCost{}
		model := ""
		if w.Work.ProbeModel != nil {
			model = *w.Work.ProbeModel
		}
		var rate *float64
		if w.Snapshot.RateFresh {
			rate = w.Work.SourceRateMultiplier
		}
		for _, pool := range w.Pools {
			c := normalizedCost(cards[w.Work.ID+"/"+model], mixes[pool], w.Work.RechargeRatio, rate, time.Now())
			c.SourceGeneration = w.Work.SourceGeneration
			if c.Status == "comparable" {
				card := cards[w.Work.ID+"/"+model]
				if card.ApplyMultiplier && w.Snapshot.RateAt != nil {
					expires := w.Snapshot.RateAt.Add(time.Duration(w.Policy.FreshSeconds) * time.Second)
					if c.ValidUntil == nil || expires.Before(*c.ValidUntil) {
						c.ValidUntil = &expires
					}
				}
				c.Basis += "/" + pool
			}
			costs[pool] = c
		}
		result[w.Work.ID] = costs
	}
	return result, nil
}

type BalancePoint struct {
	At        time.Time
	Remaining float64
	Unit      string
}
type Runway struct {
	QuotaResetAt *time.Time `json:"quota_reset_at"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason"`
	HoursLow     *float64   `json:"hours_low"`
	HoursHigh    *float64   `json:"hours_high"`
	Samples      int        `json:"samples"`
	Unit         string     `json:"unit"`
}

func balanceRunway(points []BalancePoint, now time.Time) Runway {
	r := Runway{Status: "unknown", Reason: "需要至少 4 个同来源、同单位的有效消耗样本"}
	if len(points) < 4 {
		return r
	}
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	last := points[len(points)-1]
	r.Unit = last.Unit
	if last.Unit == "" || now.Sub(last.At) > 10*time.Minute || last.At.Sub(points[0].At) < 15*time.Minute {
		return r
	}
	rates := []float64{}
	segmentStart := points[0].At
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		if a.Unit != last.Unit || b.Unit != last.Unit {
			return r
		}
		hours := b.At.Sub(a.At).Hours()
		if hours <= 0 {
			continue
		}
		delta := a.Remaining - b.Remaining
		if delta < 0 {
			rates = nil
			segmentStart = b.At
			continue
		}
		rates = append(rates, delta/hours)
	}
	r.Samples = len(rates) + 1
	if len(rates) < 3 || last.Remaining < 0 || last.At.Sub(segmentStart) < 15*time.Minute {
		return r
	}
	sort.Float64s(rates)
	lo, hi := rates[len(rates)/4], rates[(len(rates)*3)/4]
	if lo <= 0 || hi <= 0 || !isFinite(hi) || hi > lo*10 {
		r.Reason = "消耗不稳定或包含重置/充值，不作可靠续航预测"
		return r
	}
	low, high := last.Remaining/hi, last.Remaining/lo
	if !isFinite(low) || !isFinite(high) || math.IsNaN(low) {
		return r
	}
	r.Status = "estimated"
	r.Reason = "按近期消耗速率区间估计；额度重置和充值会改变结果"
	r.HoursLow = &low
	r.HoursHigh = &high
	return r
}
