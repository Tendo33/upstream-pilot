package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

type TrafficSummary struct {
	FirstContentAt      *time.Time `json:"first_content_at"`
	FirstContentSamples int        `json:"first_content_samples"`
	LatestAt            *time.Time `json:"latest_at"`
	Status              string     `json:"status"`
	Message             string     `json:"message"`
	Model               string     `json:"model"`
	Total               int        `json:"total"`
	Failed              int        `json:"failed"`
	FirstContentP95     *int       `json:"first_content_p95_ms"`
	Truncated           bool       `json:"truncated"`
	WindowStart         time.Time  `json:"window_start"`
	WindowEnd           time.Time  `json:"window_end"`
}

// RecentTraffic reads bounded samples from the admin request log, not the
// public group endpoint. Missing TTFT fields remain unknown rather than zero.
func (c *Sub2Client) RecentTraffic(ctx context.Context, id int64, model string) (TrafficSummary, error) {
	now := time.Now().UTC()
	s := TrafficSummary{Status: "unknown", Model: model, WindowStart: now.Add(-15 * time.Minute), WindowEnd: now}
	query := url.Values{"account_id": {strconv.FormatInt(id, 10)}, "model": {model}, "start_time": {s.WindowStart.Format(time.RFC3339)}, "end_time": {now.Format(time.RFC3339)}, "kind": {"all"}, "page_size": {"100"}, "page": {"1"}, "sort": {"created_at_desc"}}
	times := []int{}
	for page := 1; page <= 3; page++ {
		query.Set("page", strconv.Itoa(page))
		raw, err := c.request(ctx, http.MethodGet, "/ops/requests?"+query.Encode(), nil, "application/json")
		if err != nil {
			var he *HTTPError
			if errors.As(err, &he) && (he.Status == 404 || he.Status == 405) {
				s.Status = "unsupported"
				s.Message = "站点未提供真实请求接口"
				return s, nil
			}
			s.Status = "error"
			s.Message = "真实请求采集失败"
			return s, err
		}
		payload, err := unwrapJSON(raw)
		if err != nil {
			return s, err
		}
		var result struct {
			Items []struct {
				AccountID      int64     `json:"account_id"`
				Model          string    `json:"model"`
				Kind           string    `json:"kind"`
				StatusCode     int       `json:"status_code"`
				Phase          string    `json:"phase"`
				CreatedAt      time.Time `json:"created_at"`
				FirstContent   *int      `json:"time_to_first_token_ms"`
				StreamComplete *bool     `json:"stream_complete"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err = json.Unmarshal(payload, &result); err != nil {
			return s, err
		}
		if result.Items == nil {
			s.Status = "unsupported"
			s.Message = "真实请求接口未返回可识别的记录"
			return s, nil
		}
		for _, item := range result.Items {
			if item.AccountID != id || (model != "" && item.Model != model) || item.CreatedAt.Before(s.WindowStart) || item.CreatedAt.After(now) {
				continue
			}
			if item.Kind != "success" && item.Kind != "error" {
				continue
			}
			// Invalid client input is not evidence that an upstream is broken.
			if item.Kind == "error" && item.StatusCode < 500 && item.StatusCode != 429 && item.Phase != "upstream" {
				continue
			}
			if s.LatestAt == nil || item.CreatedAt.After(*s.LatestAt) {
				v := item.CreatedAt
				s.LatestAt = &v
			}
			s.Total++
			if item.Kind == "error" || (item.StreamComplete != nil && !*item.StreamComplete) {
				s.Failed++
			}
			if item.Kind == "success" && item.FirstContent != nil && *item.FirstContent >= 0 {
				times = append(times, *item.FirstContent)
				if s.FirstContentAt == nil || item.CreatedAt.After(*s.FirstContentAt) {
					at := item.CreatedAt
					s.FirstContentAt = &at
				}
			}
		}
		if len(result.Items) < 100 || page*100 >= result.Total {
			break
		}
		if page == 3 {
			s.Truncated = true
		}
	}
	s.FirstContentSamples = len(times)
	s.Status = "ok"
	if len(times) > 0 {
		sort.Ints(times)
		value := times[int(math.Ceil(float64(len(times))*.95))-1]
		s.FirstContentP95 = &value
	}
	if s.Total == 0 {
		s.Message = "该时间窗口没有可用真实请求样本"
	}
	return s, nil
}
