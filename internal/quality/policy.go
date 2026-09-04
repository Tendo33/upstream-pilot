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
	return Policy{Mode: "observe", SlowMS: 8000, FailureThreshold: 3, ErrorPercent: 20, MinimumSamples: 5, RecoverySamples: 3, CooldownSeconds: 120, FreshSeconds: 600, PriorityStep: 10, MaxPenalty: 30, PriceRisePercent: 20}
}

func (p Policy) Validate() error {
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
	Samples       []Sample
	Balance       *float64
	BalanceFresh  bool
	Rate          *float64
	PreviousRate  *float64
	RateFresh     bool
	TrafficTotal  int
	TrafficFailed int
	TrafficP95    *int
	TrafficFresh  bool
}
type State struct {
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
	State          State
	P95            *int
	SuccessPercent *float64
	Count          int
	HardFailure    bool
}

func Evaluate(p Policy, previous State, snapshot Snapshot, now time.Time) Decision {
	s := previous
	samples := make([]Sample, 0, len(snapshot.Samples))
	for _, v := range snapshot.Samples {
		if !v.At.After(now.Add(time.Second)) && now.Sub(v.At) <= time.Duration(p.FreshSeconds)*time.Second {
			samples = append(samples, v)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.After(samples[j].At) })
	d := Decision{State: s, Count: len(samples)}
	if previous.Conflict {
		s.Status = "conflict"
		s.Reason = "检测到人工修改，自动写回已暂停"
		d.State = s
		return d
	}
	financialRisk := snapshot.BalanceFresh && snapshot.Balance != nil && p.LowBalance != nil && *snapshot.Balance <= *p.LowBalance
	financialRisk = financialRisk || (snapshot.RateFresh && snapshot.Rate != nil && ((p.MaxRate != nil && *snapshot.Rate > *p.MaxRate) || (p.PriceRisePercent > 0 && snapshot.PreviousRate != nil && *snapshot.PreviousRate > 0 && *snapshot.Rate > *snapshot.PreviousRate*(1+p.PriceRisePercent/100))))
	if len(samples) == 0 && !(snapshot.TrafficFresh && snapshot.TrafficTotal >= p.MinimumSamples) && !financialRisk {
		s.Status = "unknown"
		s.Reason = "探测数据不足或已过期，保留现有优先级"
		s.RecoveryStreak = 0
		d.State = s
		return d
	}
	failed, consecutive := 0, 0
	times := []int{}
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
	tier := 0
	reasons := []string{}
	if consecutive >= p.FailureThreshold {
		tier = 2
		reasons = append(reasons, fmt.Sprintf("连续 %d 次探测失败", consecutive))
	}
	if len(samples) >= p.MinimumSamples && 100*float64(failed)/float64(len(samples)) >= p.ErrorPercent {
		tier = max(tier, 2)
		reasons = append(reasons, "探测错误率超过阈值")
	}
	if len(times) >= p.MinimumSamples && d.P95 != nil && *d.P95 > p.SlowMS {
		tier = max(tier, 1)
		reasons = append(reasons, "首字 P95 超过阈值")
	}
	if snapshot.TrafficFresh && snapshot.TrafficTotal >= p.MinimumSamples {
		if 100*float64(snapshot.TrafficFailed)/float64(snapshot.TrafficTotal) >= p.ErrorPercent {
			tier = max(tier, 2)
			reasons = append(reasons, "真实请求错误率超过阈值")
		}
		if snapshot.TrafficP95 != nil && *snapshot.TrafficP95 > p.SlowMS {
			tier = max(tier, 1)
			reasons = append(reasons, "真实请求首字 P95 超过阈值")
		}
	}
	if consecutive >= p.FailureThreshold && len(samples) > 0 && (samples[0].FailureReason == "AUTH" || samples[0].FailureReason == "BALANCE") {
		d.HardFailure = true
	}
	if snapshot.BalanceFresh && snapshot.Balance != nil && p.LowBalance != nil && *snapshot.Balance <= *p.LowBalance {
		tier = max(tier, 1)
		reasons = append(reasons, "上游余额低于阈值")
		if *snapshot.Balance <= 0 {
			tier = max(tier, 2)
			d.HardFailure = true
		}
	}
	if snapshot.RateFresh && snapshot.Rate != nil {
		if p.MaxRate != nil && *snapshot.Rate > *p.MaxRate {
			tier = max(tier, 1)
			reasons = append(reasons, "成本倍率超过上限")
		}
		if p.PriceRisePercent > 0 && snapshot.PreviousRate != nil && *snapshot.PreviousRate > 0 && *snapshot.Rate > *snapshot.PreviousRate*(1+p.PriceRisePercent/100) {
			tier = max(tier, 1)
			reasons = append(reasons, "上游价格上涨超过阈值")
		}
	}
	freshSample := len(samples) > 0 && (s.LastSampleAt == nil || samples[0].At.After(*s.LastSampleAt))
	if freshSample {
		at := samples[0].At
		s.LastSampleAt = &at
	}
	canChange := s.LastChangedAt == nil || now.Sub(*s.LastChangedAt) >= time.Duration(p.CooldownSeconds)*time.Second
	if tier > s.Tier {
		s.Tier = tier
		s.RecoveryStreak = 0
		at := now
		s.LastChangedAt = &at
	} else if tier < s.Tier {
		if freshSample && samples[0].Success {
			s.RecoveryStreak++
		} else if freshSample {
			s.RecoveryStreak = 0
		}
		// A missing cost or balance observation cannot prove that its risk recovered.
		financialUnknown := (p.LowBalance != nil && !snapshot.BalanceFresh) || ((p.MaxRate != nil || strings.Contains(previous.Reason, "价格") || strings.Contains(previous.Reason, "成本")) && !snapshot.RateFresh)
		if s.RecoveryStreak >= p.RecoverySamples && canChange && !financialUnknown {
			s.Tier--
			s.RecoveryStreak = 0
			at := now
			s.LastChangedAt = &at
		}
	} else {
		s.RecoveryStreak = 0
	}
	s.Desired = min(1000000, s.Baseline+min(p.MaxPenalty, s.Tier*p.PriorityStep))
	s.Status = "healthy"
	s.Reason = "质量正常"
	if s.Tier > 0 {
		s.Status = "degraded"
		s.Reason = "恢复观察中"
	}
	if len(reasons) > 0 {
		s.Reason = strings.Join(reasons, "；")
	}
	if len(samples) > 0 && !samples[0].Success && s.Tier == 0 {
		s.Status = "watching"
		s.Reason = "发现异常，等待后续样本确认"
	}
	d.State = s
	return d
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
