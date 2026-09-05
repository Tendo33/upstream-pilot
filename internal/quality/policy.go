// Package quality contains the application's independently implemented policy.
// Evaluation is pure; remote writes belong to the application controller.
package quality

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Policy struct {
	WindowSeconds    int      `json:"window_seconds"`
	SlowConsecutive  int      `json:"slow_consecutive"`
	AutoLoadFactor   bool     `json:"auto_load_factor"`
	AutoConcurrency  bool     `json:"auto_concurrency"`
	CapacityPercent  int      `json:"capacity_percent"`
	Mode             string   `json:"mode"`
	SlowMS           int      `json:"slow_ms"`
	FailureThreshold int      `json:"failure_threshold"`
	ErrorPercent     float64  `json:"error_percent"`
	MinimumSamples   int      `json:"minimum_samples"`
	RecoverySamples  int      `json:"recovery_samples"`
	CooldownSeconds  int      `json:"cooldown_seconds"`
	FreshSeconds     int      `json:"fresh_seconds"`
	PriorityStep     int      `json:"priority_step"`
	MaxPenalty       int      `json:"max_penalty"`
	LowBalance       *float64 `json:"low_balance"`
	MaxRate          *float64 `json:"max_rate"`
	PriceRisePercent float64  `json:"price_rise_percent"`
	AutoPause        bool     `json:"auto_pause"`
}

func DefaultPolicy() Policy {
	return Policy{WindowSeconds: 1800, SlowConsecutive: 2, CapacityPercent: 50, Mode: "observe", SlowMS: 8000, FailureThreshold: 3, ErrorPercent: 20, MinimumSamples: 5, RecoverySamples: 3, CooldownSeconds: 120, FreshSeconds: 600, PriorityStep: 10, MaxPenalty: 30, PriceRisePercent: 20}
}

func (p Policy) Validate() error {
	if p.WindowSeconds < 30 || p.WindowSeconds > 86400 || p.WindowSeconds < p.FreshSeconds || p.SlowConsecutive < 2 || p.SlowConsecutive > 100 || p.CapacityPercent < 1 || p.CapacityPercent > 100 {
		return errors.New("统计窗口、连续慢请求或容量比例无效")
	}
	if p.Mode != "observe" && p.Mode != "priority" {
		return errors.New("模式必须是 observe 或 priority")
	}
	if p.SlowMS < 100 || p.SlowMS > 600000 || p.FailureThreshold < 2 || p.FailureThreshold > 100 || p.MinimumSamples < 2 || p.MinimumSamples > 1000 || p.RecoverySamples < 2 || p.RecoverySamples > 100 {
		return errors.New("探测阈值超出允许范围")
	}
	if p.CooldownSeconds < 0 || p.CooldownSeconds > 86400 || p.FreshSeconds < 30 || p.FreshSeconds > 86400 || p.PriorityStep < 1 || p.PriorityStep > 100000 || p.MaxPenalty < p.PriorityStep || p.MaxPenalty > 1000000 {
		return errors.New("冷却、数据有效期或优先级设置无效")
	}
	if !finite(p.ErrorPercent) || p.ErrorPercent <= 0 || p.ErrorPercent > 100 || !finite(p.PriceRisePercent) || p.PriceRisePercent < 0 || p.PriceRisePercent > 10000 {
		return errors.New("百分比设置无效")
	}
	for _, v := range []*float64{p.LowBalance, p.MaxRate} {
		if v != nil && (!finite(*v) || *v < 0 || *v > 1e12) {
			return errors.New("余额或成本阈值无效")
		}
	}
	return nil
}

type Sample struct {
	At             time.Time `json:"at"`
	Success        bool      `json:"success"`
	FirstContentMS *int      `json:"first_content_ms"`
	DurationMS     int       `json:"duration_ms"`
	FailureReason  string    `json:"failure_reason"`
	Model          string    `json:"model"`
}
type Snapshot struct {
	TrafficIncomplete     bool
	TrafficLatencyAt      *time.Time
	BalanceAt             *time.Time
	RateAt                *time.Time
	TrafficAt             *time.Time
	ReferenceRate         *float64
	Samples               []Sample
	Balance               *float64
	BalanceFresh          bool
	Rate                  *float64
	PreviousRate          *float64
	RateFresh             bool
	TrafficLatencySamples int
	TrafficTotal          int
	TrafficFailed         int
	TrafficP95            *int
	TrafficFresh          bool
}
type Risk struct {
	Kind           string     `json:"kind"`
	Level          int        `json:"level"`
	Hard           bool       `json:"hard"`
	Since          time.Time  `json:"since"`
	LastEvidenceAt *time.Time `json:"last_evidence_at"`
	LastChangedAt  time.Time  `json:"last_changed_at"`
	Recovery       int        `json:"recovery"`
	Unknown        bool       `json:"unknown"`
}

type State struct {
	LastControlAppliedAt *time.Time `json:"last_control_applied_at"`
	Risks                []Risk     `json:"risks"`
	EvaluatedAt          *time.Time `json:"evaluated_at"`
	PlanError            string     `json:"plan_error"`
	PlanStrategy         string     `json:"plan_strategy"`

	Baseline       int        `json:"baseline_priority"`
	LastApplied    *int       `json:"last_applied_priority"`
	Desired        int        `json:"desired_priority"`
	Tier           int        `json:"tier"`
	RecoveryStreak int        `json:"recovery_streak"`
	LastSampleAt   *time.Time `json:"last_sample_at"`
	LastChangedAt  *time.Time `json:"last_changed_at"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	Conflict       bool       `json:"conflict"`
	OwnedPause     bool       `json:"owned_pause"`
}
type Decision struct {
	SortingLatency *int
	LatencySource  string
	Eligible       bool
	LatestAt       *time.Time
	State          State
	P95            *int
	SuccessPercent *float64
	Count          int
	HardFailure    bool
}

func Evaluate(p Policy, previous State, snapshot Snapshot, now time.Time) Decision {
	// Policy values come from validated settings, but old serialized settings
	// receive newly introduced defaults during upgrade.
	if p.WindowSeconds == 0 {
		p.WindowSeconds = 1800
	}
	if p.SlowConsecutive == 0 {
		p.SlowConsecutive = 2
	}
	snapshot = snapshot.At(p, now)
	s := previous
	s.Risks = append([]Risk(nil), previous.Risks...)
	s.EvaluatedAt = &now
	d := Decision{State: s}
	if s.Conflict {
		s.Status = "conflict"
		s.Reason = "检测到人工修改，自动写回已暂停"
		d.State = s
		return d
	}
	samples := make([]Sample, 0, len(snapshot.Samples))
	for _, v := range snapshot.Samples {
		if !v.At.After(now.Add(time.Second)) && now.Sub(v.At) <= time.Duration(p.WindowSeconds)*time.Second {
			samples = append(samples, v)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.After(samples[j].At) })
	d.Count = len(samples)
	failed, consecutive, slowRun, hardRun := 0, 0, 0, 0
	times := []int{}
	var latest *Sample
	if len(samples) > 0 {
		latest = &samples[0]
		at := latest.At
		d.LatestAt = &at
	}
	fresh := latest != nil && now.Sub(latest.At) <= time.Duration(p.FreshSeconds)*time.Second
	for i, v := range samples {
		if !v.Success {
			failed++
			if consecutive == i {
				consecutive++
			}
		}
		if v.Success && v.FirstContentMS != nil {
			times = append(times, *v.FirstContentMS)
		}
		if i == slowRun && v.Success && v.FirstContentMS != nil && *v.FirstContentMS > p.SlowMS {
			slowRun++
		}
		if i == hardRun && !v.Success && latest != nil && v.FailureReason == latest.FailureReason && (v.FailureReason == "AUTH" || v.FailureReason == "BALANCE") {
			hardRun++
		}
	}
	if len(samples) > 0 {
		v := 100 * float64(len(samples)-failed) / float64(len(samples))
		d.SuccessPercent = &v
	}
	if len(times) > 0 {
		sort.Ints(times)
		v := times[int(math.Ceil(.95*float64(len(times))))-1]
		d.P95 = &v
	}
	trafficKnown := snapshot.TrafficFresh && !snapshot.TrafficIncomplete && snapshot.TrafficTotal >= p.MinimumSamples
	partialFailure := snapshot.TrafficFresh && snapshot.TrafficIncomplete && snapshot.TrafficTotal >= p.MinimumSamples && snapshot.TrafficFailed >= p.FailureThreshold && 100*float64(snapshot.TrafficFailed)/float64(snapshot.TrafficTotal) >= p.ErrorPercent
	trafficLatencyKnown := trafficKnown && snapshot.TrafficP95 != nil && snapshot.TrafficLatencySamples >= p.MinimumSamples && snapshot.TrafficLatencyAt != nil && !snapshot.TrafficLatencyAt.After(now.Add(time.Second)) && now.Sub(*snapshot.TrafficLatencyAt) <= time.Duration(p.FreshSeconds)*time.Second
	if trafficLatencyKnown {
		d.SortingLatency = snapshot.TrafficP95
		d.LatencySource = "traffic"
	} else if fresh && len(times) >= p.MinimumSamples {
		d.SortingLatency = d.P95
		d.LatencySource = "probe"
	} else {
		d.LatencySource = "unknown"
	}
	probeKnown := fresh && len(samples) >= p.MinimumSamples
	failureTrigger := fresh && (consecutive >= p.FailureThreshold || (latest != nil && !latest.Success && probeKnown && 100*float64(failed)/float64(len(samples)) >= p.ErrorPercent))
	failureTrigger = failureTrigger || partialFailure || (trafficKnown && 100*float64(snapshot.TrafficFailed)/float64(snapshot.TrafficTotal) >= p.ErrorPercent)
	slowTrigger := fresh && (slowRun >= p.SlowConsecutive || (latest != nil && latest.FirstContentMS != nil && *latest.FirstContentMS > p.SlowMS && len(times) >= p.MinimumSamples && d.P95 != nil && *d.P95 > p.SlowMS))
	slowTrigger = slowTrigger || (trafficKnown && snapshot.TrafficP95 != nil && *snapshot.TrafficP95 > p.SlowMS)
	reference := snapshot.ReferenceRate
	if reference == nil {
		reference = snapshot.PreviousRate
	}
	priceTrigger := snapshot.RateFresh && snapshot.Rate != nil && ((p.MaxRate != nil && *snapshot.Rate > *p.MaxRate) || (p.PriceRisePercent > 0 && reference != nil && *reference > 0 && *snapshot.Rate > *reference*(1+p.PriceRisePercent/100)))
	balanceTrigger := snapshot.BalanceFresh && snapshot.Balance != nil && p.LowBalance != nil && *snapshot.Balance <= *p.LowBalance
	latestOK := fresh && latest.Success
	slowOK := latestOK && latest.FirstContentMS != nil && *latest.FirstContentMS <= p.SlowMS
	qualityAt := d.LatestAt
	if (trafficKnown || partialFailure) && snapshot.TrafficAt != nil && (qualityAt == nil || snapshot.TrafficAt.After(*qualityAt)) {
		qualityAt = snapshot.TrafficAt
	}
	d.LatestAt = qualityAt
	type observation struct {
		kind                       string
		trigger, known, good, hard bool
		level, votes               int
		at                         *time.Time
	}
	balanceLevel := 1
	if balanceTrigger && *snapshot.Balance <= 0 {
		balanceLevel = 2
	}
	obs := []observation{
		{"failure", failureTrigger, fresh || trafficKnown || partialFailure, latestOK || (trafficKnown && snapshot.TrafficFailed == 0), hardRun >= p.FailureThreshold, 2, p.RecoverySamples, qualityAt},
		{"slow", slowTrigger, fresh || trafficKnown, slowOK || (trafficKnown && snapshot.TrafficP95 != nil && *snapshot.TrafficP95 <= p.SlowMS), false, 1, p.RecoverySamples, qualityAt},
		{"balance", balanceTrigger, p.LowBalance == nil || snapshot.BalanceFresh, p.LowBalance == nil || (snapshot.BalanceFresh && snapshot.Balance != nil && *snapshot.Balance > *p.LowBalance), balanceLevel == 2, balanceLevel, 1, snapshot.BalanceAt},
		{"price", priceTrigger, (p.MaxRate == nil && p.PriceRisePercent == 0) || snapshot.RateFresh, !priceTrigger && ((p.MaxRate == nil && p.PriceRisePercent == 0) || snapshot.RateFresh), false, 1, 1, snapshot.RateAt},
	}
	byKind := map[string]Risk{}
	for _, r := range s.Risks {
		byKind[r.Kind] = r
	}
	// Preserve old holds whose reason cannot be reconstructed safely.
	if len(byKind) == 0 && s.Tier > 0 {
		byKind["legacy"] = Risk{Kind: "legacy", Level: s.Tier, Since: now, Unknown: true}
	}
	for _, o := range obs {
		r, exists := byKind[o.kind]
		if !exists && !o.trigger {
			continue
		}
		if !exists {
			r = Risk{Kind: o.kind, Since: now, LastChangedAt: now}
		}
		at := o.at
		if at == nil && o.known {
			at = &now
		}
		newEvidence := at != nil && (r.LastEvidenceAt == nil || at.After(*r.LastEvidenceAt))
		r.Unknown = !o.known
		if o.trigger {
			if r.Level < o.level {
				r.Level = o.level
				r.LastChangedAt = now
			}
			r.Hard = o.hard
			r.Recovery = 0
		} else if !o.known {
			r.Recovery = 0
		} else if o.good {
			if newEvidence {
				r.Recovery++
			}
			if r.Recovery >= o.votes && now.Sub(r.LastChangedAt) >= time.Duration(p.CooldownSeconds)*time.Second {
				r.Level--
				if o.kind == "price" || o.kind == "balance" {
					r.Level = 0
				}
				r.LastChangedAt = now
				r.Recovery = 0
			}
		} else if newEvidence {
			r.Recovery = 0
		}
		if newEvidence {
			v := *at
			r.LastEvidenceAt = &v
		}
		if r.Level <= 0 {
			delete(byKind, o.kind)
		} else {
			byKind[o.kind] = r
		}
	}
	s.Risks = []Risk{}
	s.Tier = 0
	s.RecoveryStreak = 0
	keys := []string{}
	for key := range byKind {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	reasons := []string{}
	for _, key := range keys {
		r := byKind[key]
		s.Risks = append(s.Risks, r)
		s.Tier = max(s.Tier, r.Level)
		s.RecoveryStreak = max(s.RecoveryStreak, r.Recovery)
		d.HardFailure = d.HardFailure || (r.Hard && !r.Unknown)
		label := map[string]string{"failure": "上游失败风险", "slow": "首字延迟超限", "balance": "余额低于阈值", "price": "采购成本超限", "legacy": "旧版降级需人工确认"}[key]
		if r.Unknown {
			label += "（证据未知，保留降级）"
		} else if r.Recovery > 0 {
			label += fmt.Sprintf("（恢复确认 %d）", r.Recovery)
		}
		reasons = append(reasons, label)
	}
	s.Status = "healthy"
	s.Reason = "质量正常"
	if s.Tier > 0 {
		s.Status = "degraded"
		s.Reason = strings.Join(reasons, "；")
	} else if !fresh && !trafficKnown {
		s.Status = "unknown"
		s.Reason = "最新证据已过期，保留现有优先级"
	} else if !probeKnown && !trafficKnown {
		s.Status = "watching"
		s.Reason = "样本不足，继续观察"
	} else if fresh && !latest.Success {
		s.Status = "watching"
		s.Reason = "发现异常，等待后续确认"
	}
	if d.LatestAt != nil {
		s.LastSampleAt = d.LatestAt
	}
	if s.Tier != previous.Tier {
		changed := now
		s.LastChangedAt = &changed
	}
	s.Desired = min(1000000, s.Baseline+min(p.MaxPenalty, s.Tier*p.PriorityStep))
	if s.Status == "unknown" {
		s.Desired = previous.Desired
	}
	if !fresh && !trafficKnown && !partialFailure && !priceTrigger && !balanceTrigger {
		s.Status = "unknown"
		s.Reason = "最新证据已过期，保留现有优先级"
		s.Desired = previous.Desired
	}
	financeEligible := (p.LowBalance == nil || (snapshot.BalanceFresh && snapshot.Balance != nil && *snapshot.Balance > *p.LowBalance)) && (p.MaxRate == nil || (snapshot.RateFresh && snapshot.Rate != nil && *snapshot.Rate <= *p.MaxRate))
	d.Eligible = s.Status == "healthy" && (probeKnown || trafficKnown) && financeEligible
	d.State = s
	return d
}

func EventKey(s State) string {
	parts := []string{s.Status}
	for _, risk := range s.Risks {
		parts = append(parts, risk.Kind+":"+fmt.Sprint(risk.Level))
	}
	if s.PlanError != "" {
		parts = append(parts, "plan-conflict")
	}
	return strings.Join(parts, ":")
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// At rechecks timestamps every time a cached snapshot is consumed, including
// during a long reconciliation cycle. A successful collector flag alone is not evidence of freshness.
func (s Snapshot) At(p Policy, now time.Time) Snapshot {
	fresh := func(ok bool, at *time.Time) bool {
		return ok && at != nil && !at.After(now.Add(time.Second)) && now.Sub(*at) <= time.Duration(p.FreshSeconds)*time.Second
	}
	s.BalanceFresh = fresh(s.BalanceFresh, s.BalanceAt) && s.Balance != nil
	s.RateFresh = fresh(s.RateFresh, s.RateAt) && s.Rate != nil
	s.TrafficFresh = fresh(s.TrafficFresh, s.TrafficAt)
	return s
}
