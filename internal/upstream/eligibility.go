package upstream

import (
	"encoding/json"
	"path"
	"strings"
	"time"
)

// NativeConstraints contains only routing metadata; never persist credentials
// or the arbitrary extra object returned by an administration endpoint.
type NativeConstraints struct {
	QuotaResetAt       *time.Time        `json:"quota_reset_at"`
	CurrentConcurrency *int              `json:"current_concurrency"`
	QueueDepth         *int              `json:"queue_depth"`
	CapacityVerified   bool              `json:"capacity_verified"`
	Known              bool              `json:"known"`
	GroupsKnown        bool              `json:"groups_known"`
	Groups             []int64           `json:"groups"`
	RateLimitResetAt   *time.Time        `json:"rate_limit_reset_at"`
	OverloadUntil      *time.Time        `json:"overload_until"`
	CooldownUntil      *time.Time        `json:"cooldown_until"`
	ExpiresAt          *int64            `json:"expires_at"`
	AutoPauseOnExpired bool              `json:"auto_pause_on_expired"`
	QuotaExceeded      bool              `json:"quota_exceeded"`
	QuotaUnknown       bool              `json:"quota_unknown"`
	MappingKnown       bool              `json:"mapping_known"`
	Mapping            map[string]string `json:"mapping"`
	Passthrough        bool              `json:"passthrough"`
	Concurrency        *int              `json:"concurrency"`
}

type NativeAssessment struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func parseNativeConstraints(data []byte, a Sub2Account) NativeConstraints {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	n := NativeConstraints{Known: true, Concurrency: a.Concurrency}
	_ = json.Unmarshal(raw["current_concurrency"], &n.CurrentConcurrency)
	_ = json.Unmarshal(raw["queue_depth"], &n.QueueDepth)
	var capacityStatus string
	_ = json.Unmarshal(raw["concurrency_status"], &capacityStatus)
	n.CapacityVerified = capacityStatus == "ok" && n.CurrentConcurrency != nil && *n.CurrentConcurrency >= 0 && n.QueueDepth != nil && *n.QueueDepth >= 0
	for _, key := range []string{"rate_limit_reset_at", "overload_until", "temp_unschedulable_until", "expires_at", "auto_pause_on_expired", "extra", "type", "platform", "status", "schedulable"} {
		if _, ok := raw[key]; !ok {
			n.Known = false
		}
	}
	if string(raw["auto_pause_on_expired"]) == "null" || string(raw["schedulable"]) == "null" {
		n.Known = false
	}
	if v, ok := raw["parent_account_id"]; ok && string(v) != "null" {
		n.Known = false
	}
	// Malformed optional fields are unknown, never silently treated as null.
	for key, dest := range map[string]any{"rate_limit_reset_at": &n.RateLimitResetAt, "overload_until": &n.OverloadUntil, "temp_unschedulable_until": &n.CooldownUntil, "expires_at": &n.ExpiresAt, "auto_pause_on_expired": &n.AutoPauseOnExpired} {
		if v, ok := raw[key]; ok && json.Unmarshal(v, dest) != nil {
			n.Known = false
		}
	}
	_, n.GroupsKnown = raw["account_groups"]
	for _, g := range a.AccountGroups {
		n.Groups = append(n.Groups, g.GroupID)
	}
	var credentials map[string]json.RawMessage
	if v, ok := raw["credentials"]; ok && json.Unmarshal(v, &credentials) == nil && credentials != nil {
		n.MappingKnown = true
		if m, ok := credentials["model_mapping"]; ok && string(m) != "null" {
			if json.Unmarshal(m, &n.Mapping) != nil {
				n.MappingKnown = false
			}
		}
	}
	for _, key := range []string{"quota_daily_reset_at", "quota_weekly_reset_at"} {
		var v string
		if json.Unmarshal(raw[key], &v) == nil {
			if at, e := time.Parse(time.RFC3339, v); e == nil && time.Now().Before(at) && (n.QuotaResetAt == nil || at.Before(*n.QuotaResetAt)) {
				n.QuotaResetAt = &at
			}
		}
	}
	var extra map[string]json.RawMessage
	if v, ok := raw["extra"]; ok && json.Unmarshal(v, &extra) != nil {
		n.QuotaUnknown = true
	}
	if a.Platform == "openai" {
		if v, ok := extra["openai_passthrough"]; ok && string(v) != "null" {
			_ = json.Unmarshal(v, &n.Passthrough)
		} else {
			_ = json.Unmarshal(extra["openai_oauth_passthrough"], &n.Passthrough)
		}
	}
	// Be conservative about elapsed quota windows. Only the target scheduler can
	// authoritatively reset usage; a reported exhausted window cannot be a backup.
	for _, prefix := range []string{"quota_", "quota_daily_", "quota_weekly_"} {
		var limit, used float64
		if v, ok := extra[prefix+"limit"]; ok {
			if json.Unmarshal(v, &limit) != nil || limit < 0 {
				n.QuotaUnknown = true
				continue
			}
			if limit > 0 {
				if json.Unmarshal(extra[prefix+"used"], &used) != nil || used < 0 {
					n.QuotaUnknown = true
					continue
				}
				if used >= limit {
					n.QuotaExceeded = true
				}
			}
		}
	}
	return n
}

// NativeEligibility certifies only the basic API-key contract supported here.
// OAuth/shadow/session-specific constraints need a version-specific adapter.
// This is a snapshot of eligibility, not a reservation of concurrent capacity.
func (a Sub2Account) NativeEligibility(model string, groups []int64, now time.Time) NativeAssessment {
	blocked := func(reason string) NativeAssessment { return NativeAssessment{"blocked", reason} }
	unknown := func(reason string) NativeAssessment { return NativeAssessment{"unknown", reason} }
	n := a.Native
	if a.Status != "active" || !a.Schedulable {
		return blocked("原生调度开关关闭或账号非 active")
	}
	for _, v := range []struct {
		at     *time.Time
		reason string
	}{{n.CooldownUntil, "原生临时冷却中"}, {n.RateLimitResetAt, "原生限流恢复期"}, {n.OverloadUntil, "原生过载保护中"}} {
		if v.at != nil && now.Before(*v.at) {
			return blocked(v.reason)
		}
	}
	if n.AutoPauseOnExpired && n.ExpiresAt != nil && now.Unix() >= *n.ExpiresAt {
		return blocked("账号已过期")
	}
	if n.Concurrency != nil && n.CurrentConcurrency != nil && *n.CurrentConcurrency >= *n.Concurrency {
		return blocked("原生并发上限已占满")
	}
	if n.QueueDepth != nil && *n.QueueDepth > 0 {
		return blocked("原生请求已有排队")
	}
	if n.QuotaExceeded {
		return blocked("原生额度已用尽，等待确认重置")
	}
	if !n.Known || n.QuotaUnknown {
		return unknown("原生运行约束缺失或格式不兼容")
	}
	if a.Type != "apikey" || (a.Platform != "openai" && a.Platform != "anthropic") {
		return unknown("此账号类型的原生资格尚未验证")
	}
	if n.Concurrency == nil || *n.Concurrency < 1 {
		return unknown("未提供有效并发容量")
	}
	if len(groups) > 0 && !n.GroupsKnown {
		return unknown("原生分组成员资格未提供")
	}
	for _, group := range groups {
		found := false
		for _, g := range n.Groups {
			found = found || g == group
		}
		if !found {
			return blocked("原生分组成员资格已变化")
		}
	}
	if model == "" || !n.MappingKnown {
		return unknown("模型资格未确认")
	}
	if len(n.Mapping) > 0 && !n.Passthrough {
		found := false
		for key := range n.Mapping {
			// Only a trailing wildcard is certified. More complex native alias and
			// wildcard rules remain unknown until the corresponding adapter exists.
			if key == model || (strings.HasSuffix(key, "*") && !strings.ContainsAny(strings.TrimSuffix(key, "*"), "*?[\\") && strings.HasPrefix(model, strings.TrimSuffix(key, "*"))) {
				found = true
			}
		}
		if !found {
			for key := range n.Mapping {
				if match, _ := path.Match(key, model); match {
					return unknown("模型映射需要更具体的契约适配")
				}
			}
			return blocked("当前模型不在原生账号映射中")
		}
	}
	return NativeAssessment{"eligible", "原生约束通过；瞬时并发余量仍由 Sub2API 决定"}
}
