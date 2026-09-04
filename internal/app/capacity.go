package app

import "time"

type CapacityView struct {
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Configured    *int   `json:"configured"`
	ReportedSpare *int   `json:"reported_spare"`
	VerifiedSpare *int   `json:"verified_spare"`
}

func capacityView(w AccountWork, now time.Time) CapacityView {
	n := w.NativeConstraints
	v := CapacityView{Status: "unknown", Reason: "缺少可靠的实时并发和排队数据", Configured: n.Concurrency}
	if w.NativeCheckedAt == nil || now.Sub(*w.NativeCheckedAt) > 5*time.Minute {
		return v
	}
	if n.Concurrency != nil && *n.Concurrency > 0 && n.CurrentConcurrency != nil && *n.CurrentConcurrency >= 0 {
		spare := max(0, *n.Concurrency-*n.CurrentConcurrency)
		v.ReportedSpare = &spare
	}
	if !w.Schedulable || w.RemoteStatus != "active" || n.QuotaExceeded || (n.CooldownUntil != nil && now.Before(*n.CooldownUntil)) || (n.RateLimitResetAt != nil && now.Before(*n.RateLimitResetAt)) || (n.OverloadUntil != nil && now.Before(*n.OverloadUntil)) || (v.ReportedSpare != nil && *v.ReportedSpare == 0) || (n.QueueDepth != nil && *n.QueueDepth > 0) {
		zero := 0
		v.Status = "unavailable"
		v.Reason = "原生调度、额度、并发或排队约束阻止承接新请求"
		v.VerifiedSpare = &zero
		return v
	}
	if n.CapacityVerified && v.ReportedSpare != nil {
		v.Status = "known"
		v.Reason = "当前接口报告的空闲槽位；不是预留容量"
		v.VerifiedSpare = v.ReportedSpare
	}
	return v
}
