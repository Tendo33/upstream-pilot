package app

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestPriceNormalizationUsesCurrencyUnitsCacheAndRechargeOnce(t *testing.T) {
	now := time.Now()
	cache := 7.0
	card := PriceCard{Currency: "CNY", CurrencyToUSD: 1.0 / 7, TokenUnit: 1000000, Input: 14, Output: 70, CacheRead: &cache, TokenConvention: "disjoint", Confirmed: true, ValidUntil: now.Add(time.Hour)}
	mix := TokenMix{Samples: 20, Input: 500, Output: 100, CacheRead: 500, LatestAt: &now}
	c := normalizedCost(card, mix, 1.25, nil, now)
	want := 2500.0 / 1100 / 1.25
	if c.USDPerMillion == nil || math.Abs(*c.USDPerMillion-want) > 1e-9 {
		t.Fatalf("normalization=%+v want=%f", c, want)
	}
	card.TokenUnit = 1000
	card.Input /= 1000
	card.Output /= 1000
	cache /= 1000
	if got := normalizedCost(card, mix, 1.25, nil, now); got.USDPerMillion == nil || math.Abs(*got.USDPerMillion-want) > 1e-9 {
		t.Fatal(got)
	}
	card.ApplyMultiplier = true
	multiplier := 2.0
	if got := normalizedCost(card, mix, 1.25, &multiplier, now); got.USDPerMillion == nil || math.Abs(*got.USDPerMillion-2*want) > 1e-9 {
		t.Fatal("double recharge conversion", got)
	}
	card.CacheRead = nil
	if got := normalizedCost(card, mix, 1.25, &multiplier, now); got.Status != "unknown" {
		t.Fatal("unknown cache price used", got)
	}
}

func TestRunwayRejectsResetShortHistoryAndUnstableUse(t *testing.T) {
	now := time.Now()
	points := []BalancePoint{}
	for i := 0; i < 5; i++ {
		points = append(points, BalancePoint{At: now.Add(time.Duration(i-4) * 5 * time.Minute), Remaining: 100 - float64(i)*5, Unit: "USD"})
	}
	r := balanceRunway(points, now)
	if r.Status != "estimated" || math.Abs(*r.HoursLow-80.0/60) > 1e-8 {
		t.Fatal(r)
	}
	points[2].Remaining = 150
	points[3].Remaining = 145
	points[4].Remaining = 140
	if r = balanceRunway(points, now); r.Status != "unknown" {
		t.Fatal("forecast survived short post-topup series", r)
	}
	points[4].Unit = "credits"
	if r = balanceRunway(points, now); r.Status != "unknown" {
		t.Fatal(r)
	}
}

func TestEconomicsUsesCommonGroupMixAndCurrentSourceOnly(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	ctx := context.Background()
	group, other := seedEnginePool(t, a, w, 20)
	card := PriceCard{Currency: "USD", CurrencyToUSD: 1, TokenUnit: 1000000, Input: 2, Output: 10, TokenConvention: "disjoint", Confirmed: true, ValidUntil: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(card)
	for _, id := range []string{w.ID, other} {
		engineRegressionSQL(t, a, `INSERT INTO model_price_cards(account_id,model,source_generation,config) SELECT id,'test-model',source_generation,$2 FROM upstream_accounts WHERE id=$1`, id, raw)
	}
	for i := 1; i <= 5; i++ {
		engineRegressionSQL(t, a, `INSERT INTO usage_observations(site_id,remote_id,account_remote_id,group_remote_id,request_id,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,created_at) VALUES($1,$2,7,1,$3,'test-model',100,100,0,0,now())`, w.SiteID, i, "request-"+string(rune('a'+i)))
	}
	works, _, err := a.engineSnapshot(ctx, w.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range works {
		c := p.Costs[poolKey(group, "test-model")]
		if c.USDPerMillion == nil || *c.USDPerMillion != 6 {
			t.Fatalf("common mix missing: %+v", c)
		}
	}
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET source_generation=source_generation+1 WHERE id=$1`, w.ID)
	works, _, err = a.engineSnapshot(ctx, w.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range works {
		if p.Work.ID == w.ID && p.Costs[poolKey(group, "test-model")].USDPerMillion != nil {
			t.Fatal("old price card reused")
		}
	}
}
