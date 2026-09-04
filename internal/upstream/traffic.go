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
	"strings"
	"time"
)

type TrafficSummary struct {
	TTFTAvailable       bool           `json:"ttft_available"`
	CompletionAvailable bool           `json:"completion_available"`
	FailureCategories   map[string]int `json:"failure_categories"`
	ExcludedErrors      int            `json:"excluded_errors"`
	FirstContentAt      *time.Time     `json:"first_content_at"`
	FirstContentSamples int            `json:"first_content_samples"`
	LatestAt            *time.Time     `json:"latest_at"`
	Status              string         `json:"status"`
	Message             string         `json:"message"`
	Model               string         `json:"model"`
	Total               int            `json:"total"`
	Failed              int            `json:"failed"`
	FirstContentP95     *int           `json:"first_content_p95_ms"`
	Truncated           bool           `json:"truncated"`
	WindowStart         time.Time      `json:"window_start"`
	WindowEnd           time.Time      `json:"window_end"`
}

type TrafficRecord struct {
	AccountID      int64     `json:"account_id"`
	GroupID        *int64    `json:"group_id"`
	RequestID      string    `json:"request_id"`
	ErrorID        *int64    `json:"error_id"`
	Model          string    `json:"model"`
	Kind           string    `json:"kind"`
	StatusCode     int       `json:"status_code"`
	Phase          string    `json:"phase"`
	Reason         string    `json:"error_type"`
	Code           string    `json:"code"`
	CreatedAt      time.Time `json:"created_at"`
	FirstContent   *int      `json:"time_to_first_token_ms"`
	Stream         *bool     `json:"stream"`
	StreamComplete *bool     `json:"stream_complete"`
	FinalOutcome   string    `json:"final_outcome"`
	IsFinal        *bool     `json:"is_final"`
}
type TrafficBatch struct {
	Status      string          `json:"status"`
	Message     string          `json:"message"`
	Truncated   bool            `json:"truncated"`
	WindowStart time.Time       `json:"window_start"`
	WindowEnd   time.Time       `json:"window_end"`
	Records     []TrafficRecord `json:"-"`
}

func (c *Sub2Client) RecentSiteTraffic(ctx context.Context) (TrafficBatch, error) {
	return c.fetchTraffic(ctx, url.Values{})
}
func (c *Sub2Client) RecentTraffic(ctx context.Context, id int64, model string) (TrafficSummary, error) {
	batch, err := c.fetchTraffic(ctx, url.Values{"account_id": {strconv.FormatInt(id, 10)}, "model": {model}})
	return SummarizeTraffic(batch, id, model), err
}
func (c *Sub2Client) fetchTraffic(ctx context.Context, query url.Values) (TrafficBatch, error) {
	now := time.Now().UTC()
	b := TrafficBatch{Status: "unknown", WindowStart: now.Add(-15 * time.Minute), WindowEnd: now}
	query.Set("start_time", b.WindowStart.Format(time.RFC3339))
	query.Set("end_time", now.Format(time.RFC3339))
	query.Set("kind", "all")
	query.Set("page_size", "100")
	query.Set("sort", "created_at_desc")
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		query.Set("page", strconv.Itoa(page))
		raw, err := c.request(ctx, http.MethodGet, "/ops/requests?"+query.Encode(), nil, "application/json")
		if err != nil {
			var he *HTTPError
			if errors.As(err, &he) && (he.Status == 404 || he.Status == 405) {
				b.Status = "unsupported"
				b.Message = "站点未提供真实请求接口"
				return b, nil
			}
			b.Status = "error"
			b.Message = "真实请求采集失败"
			return b, err
		}
		payload, err := unwrapJSON(raw)
		if err != nil {
			return b, err
		}
		var result struct {
			Items []TrafficRecord `json:"items"`
			Total int             `json:"total"`
		}
		if err = json.Unmarshal(payload, &result); err != nil {
			return b, err
		}
		if result.Items == nil {
			b.Status = "unsupported"
			b.Message = "真实请求接口未返回可识别的记录"
			return b, nil
		}
		for _, r := range result.Items {
			if len(r.Model) > 256 || len(r.RequestID) > 256 || r.CreatedAt.Before(b.WindowStart) || r.CreatedAt.After(now) {
				continue
			}
			if r.RequestID != "" {
				key := r.RequestID + "/" + r.Kind + "/" + strconv.FormatInt(r.AccountID, 10) + "/" + r.CreatedAt.Format(time.RFC3339Nano)
				if r.ErrorID != nil {
					key += "/" + strconv.FormatInt(*r.ErrorID, 10)
				}
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			b.Records = append(b.Records, r)
		}
		if len(result.Items) < 100 || page*100 >= result.Total {
			break
		}
		if page == 3 {
			b.Truncated = true
		}
	}
	b.Status = "ok"
	return b, nil
}
func SummarizeTraffic(batch TrafficBatch, id int64, model string) TrafficSummary {
	s := TrafficSummary{FailureCategories: map[string]int{}, Status: batch.Status, Message: batch.Message, Model: model, WindowStart: batch.WindowStart, WindowEnd: batch.WindowEnd, Truncated: batch.Truncated}
	if batch.Status != "ok" {
		return s
	}
	times := []int{}
	for _, item := range batch.Records {
		if item.AccountID != id || (model != "" && item.Model != model) || item.CreatedAt.Before(s.WindowStart) || item.CreatedAt.After(s.WindowEnd) {
			continue
		}
		if item.Kind != "success" && item.Kind != "error" {
			continue
		}
		// Invalid client input is not evidence that an upstream is broken.
		if item.Kind == "error" {
			category, supplier := classifyTrafficFailure(item.StatusCode, item.Phase, item.Reason+" "+item.Code)
			if !supplier {
				s.ExcludedErrors++
				continue
			}
			s.FailureCategories[category]++
		}
		if item.FirstContent != nil && *item.FirstContent >= 0 {
			s.TTFTAvailable = true
		}
		if item.StreamComplete != nil {
			s.CompletionAvailable = true
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
	return s
}

// The request phase takes precedence over HTTP status: a customer quota/429 or
// client authentication error is not a supplier failure. Account auth is.
func classifyTrafficFailure(status int, phase, reason string) (string, bool) {
	phase = strings.ToLower(strings.TrimSpace(phase))
	reason = strings.ToLower(reason)
	switch phase {
	case "auth", "request", "routing", "internal":
		return phase, false
	case "account_auth":
		return "upstream_auth", true
	case "network":
		return "network", true
	}
	if phase != "upstream" && status < 500 && status != 429 {
		return "unclassified", false
	}
	if strings.Contains(reason, "insufficient_quota") || strings.Contains(reason, "balance") || status == 402 {
		return "balance", true
	}
	if status == 401 || status == 403 {
		return "upstream_auth", true
	}
	if status == 429 {
		return "rate_limit", true
	}
	if status == 400 || status == 404 {
		return "model_or_request", true
	}
	return "upstream_http", true
}
