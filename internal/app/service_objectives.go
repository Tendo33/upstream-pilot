package app

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/upstream"
)

type serviceObjectiveSummary struct {
	ProfileID       string     `json:"profile_id"`
	GroupID         string     `json:"group_id"`
	Source          string     `json:"source"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason"`
	Unconfirmed     int        `json:"unconfirmed"`
	Samples         int        `json:"samples"`
	SuccessPercent  *float64   `json:"success_percent"`
	CompletePercent *float64   `json:"complete_percent"`
	FirstContentP95 *int       `json:"first_content_p95_ms"`
	LatestAt        *time.Time `json:"latest_at"`
	CostStatus      string     `json:"cost_status"`
}

type serviceSample struct {
	Unconfirmed bool
	At          time.Time
	Result      upstream.CanaryResult
}

func evaluateServiceObjectives(p serviceProfileWork, samples []serviceSample, now time.Time) serviceObjectiveSummary {
	s := serviceObjectiveSummary{ProfileID: p.ID, GroupID: p.GroupID, Source: "synthetic_group_entry", Status: "unknown", Reason: "等待足量的分组入口探测", CostStatus: "unknown"}
	if p.AccountID != "" {
		s.Source = "account_direct"
		s.Reason = "等待直接来源协议样本"
	}
	var successes, complete int
	latencies := []int{}
	for _, v := range samples {
		if v.At.Before(now.Add(-24*time.Hour)) || v.At.After(now) {
			continue
		}
		if v.Unconfirmed {
			s.Unconfirmed++
			continue
		}
		s.Samples++
		if v.Result.Success {
			successes++
		}
		if v.Result.Complete {
			complete++
		}
		if v.Result.FirstContentMS != nil {
			latencies = append(latencies, *v.Result.FirstContentMS)
		}
		if s.LatestAt == nil || v.At.After(*s.LatestAt) {
			at := v.At
			s.LatestAt = &at
		}
	}
	if s.Samples == 0 {
		if s.Unconfirmed > 0 {
			s.Reason = "入口请求在途或中断，尚无已确认结果"
		}
		return s
	}
	rate, completion := 100*float64(successes)/float64(s.Samples), 100*float64(complete)/float64(s.Samples)
	s.SuccessPercent = &rate
	s.CompletePercent = &completion
	if len(latencies) >= p.Config.Objectives.MinimumSamples {
		sort.Ints(latencies)
		v := latencies[int(math.Ceil(.95*float64(len(latencies))))-1]
		s.FirstContentP95 = &v
	}
	if s.Unconfirmed > 0 {
		s.Reason = "窗口内存在未确认请求，暂不认证服务目标"
		return s
	}
	if s.Samples < p.Config.Objectives.MinimumSamples {
		return s
	}
	if s.LatestAt == nil || now.Sub(*s.LatestAt) > time.Duration(p.Config.IntervalSeconds*2+p.Config.TimeoutSeconds)*time.Second {
		s.Reason = "最新入口探测已过期"
		return s
	}
	o := p.Config.Objectives
	if rate < o.SuccessPercent || o.RequireComplete && completion < o.SuccessPercent {
		s.Status = "degraded"
		s.Reason = "合成入口请求的成功率或完整结束率低于目标"
		return s
	}
	if !p.Config.Stream {
		s.Status = "partial"
		s.Reason = "成功率和完整结束达标；非流式请求没有独立首字指标"
		return s
	}
	if p.Config.Stream && s.FirstContentP95 == nil {
		s.Reason = "首字样本不足"
		return s
	}
	if s.FirstContentP95 != nil && *s.FirstContentP95 > o.FirstContentMS {
		s.Status = "degraded"
		s.Reason = "合成入口首字 P95 超过目标"
		return s
	}
	if o.MaxRequestCost != nil {
		s.Reason = "可用性已测，实际单次成本尚未确认"
		return s
	}
	s.Status = "healthy"
	if p.AccountID != "" {
		s.Reason = "供应商直连协议样本达到目标；不代表 Sub2API 代理、映射或分组路径"
		return s
	}
	s.Reason = "当前合成入口样本达到目标；真实用户最终可用率需单独统计"
	return s
}

func (a *App) serviceObjectivesHandler(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.Query(r.Context(), serviceProfileSelect+` WHERE s.owner_id=$1 AND `+serviceProfileParentLive+` ORDER BY COALESCE(g.name,a.name),p.created_at`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	profiles := []serviceProfileWork{}
	for rows.Next() {
		p, e := scanServiceProfile(rows)
		if e != nil {
			rows.Close()
			return e
		}
		profiles = append(profiles, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// One query for the complete owner window, not one history query per profile.
	rows, err = a.db.Query(r.Context(), `SELECT r.profile_id::text,COALESCE(r.completed_at,r.started_at),r.status,r.result FROM service_canary_runs r JOIN service_profiles p ON p.id=r.profile_id LEFT JOIN upstream_groups g ON g.id=p.group_id LEFT JOIN upstream_accounts a ON a.id=p.account_id JOIN sites s ON s.id=COALESCE(g.site_id,a.site_id) WHERE s.owner_id=$1 AND r.generation=p.generation AND r.started_at>now()-interval '24 hours' ORDER BY r.completed_at`, identityFrom(r).ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	samples := map[string][]serviceSample{}
	for rows.Next() {
		var id string
		var v serviceSample
		var status string
		var raw []byte
		if err = rows.Scan(&id, &v.At, &status, &raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &v.Result); err != nil {
			return err
		}
		v.Unconfirmed = status == "reserved" || status == "abandoned" || v.Result.ControlError
		samples[id] = append(samples[id], v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	result := []serviceObjectiveSummary{}
	for _, p := range profiles {
		result = append(result, evaluateServiceObjectives(p, samples[p.ID], time.Now().UTC()))
	}
	writeData(w, 200, result)
	return nil
}
