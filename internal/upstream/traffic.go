package upstream

import (
	"context"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TrafficSummary struct {
	Feeds               map[string]TrafficFeed `json:"feeds,omitempty"`
	Incomplete          bool                   `json:"incomplete"`
	TTFTAvailable       bool                   `json:"ttft_available"`
	CompletionAvailable bool                   `json:"completion_available"`
	FailureCategories   map[string]int         `json:"failure_categories"`
	ExcludedErrors      int                    `json:"excluded_errors"`
	FirstContentAt      *time.Time             `json:"first_content_at"`
	FirstContentSamples int                    `json:"first_content_samples"`
	LatestAt            *time.Time             `json:"latest_at"`
	Status              string                 `json:"status"`
	Message             string                 `json:"message"`
	Model               string                 `json:"model"`
	Total               int                    `json:"total"`
	Failed              int                    `json:"failed"`
	FirstContentP95     *int                   `json:"first_content_p95_ms"`
	Truncated           bool                   `json:"truncated"`
	WindowStart         time.Time              `json:"window_start"`
	WindowEnd           time.Time              `json:"window_end"`
}

type TrafficRecord struct {
	Source         string    `json:"-"`
	ErrorOwner     string    `json:"error_owner"`
	ErrorSource    string    `json:"error_source"`
	NativeType     string    `json:"type"`
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
	Feeds       map[string]TrafficFeed `json:"feeds"`
	Status      string                 `json:"status"`
	Message     string                 `json:"message"`
	Truncated   bool                   `json:"truncated"`
	WindowStart time.Time              `json:"window_start"`
	WindowEnd   time.Time              `json:"window_end"`
	Records     []TrafficRecord        `json:"-"`
}

func (c *Sub2Client) RecentSiteTraffic(ctx context.Context) (TrafficBatch, error) {
	return c.fetchTraffic(ctx, url.Values{})
}
func (c *Sub2Client) RecentTraffic(ctx context.Context, id int64, model string) (TrafficSummary, error) {
	batch, err := c.fetchTraffic(ctx, url.Values{"account_id": {strconv.FormatInt(id, 10)}, "model": {model}})
	return SummarizeTraffic(batch, id, model), err
}
func SummarizeTraffic(batch TrafficBatch, id int64, model string) TrafficSummary {
	s := TrafficSummary{Feeds: batch.Feeds, Incomplete: batch.Status != "ok" || batch.Truncated, FailureCategories: map[string]int{}, Status: batch.Status, Message: batch.Message, Model: model, WindowStart: batch.WindowStart, WindowEnd: batch.WindowEnd, Truncated: batch.Truncated}
	if batch.Status != "ok" && batch.Status != "partial" {
		return s
	}
	times := []int{}
	errorSource := ""
	if f := batch.Feeds[trafficUpstreamErrors]; f.Status == "ok" || f.Rows > 0 {
		errorSource = trafficUpstreamErrors
	} else if f := batch.Feeds[trafficRequestErrors]; f.Status == "ok" || f.Rows > 0 {
		errorSource = trafficRequestErrors
	}
	for _, item := range batch.Records {
		if item.AccountID != id || (model != "" && item.Model != model) || item.CreatedAt.Before(s.WindowStart) || item.CreatedAt.After(s.WindowEnd) {
			continue
		}
		if item.Kind != "success" && item.Kind != "error" {
			continue
		}
		// The upstream feed is authoritative for supplier attempts. Request errors
		// report the final result and may duplicate the last attempt.
		if item.Kind == "error" && errorSource != "" && item.Source != errorSource {
			continue
		}
		// Invalid client input is not evidence that an upstream is broken.
		if item.Kind == "error" {
			category, supplier := classifyRecordFailure(item)
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

func classifyRecordFailure(r TrafficRecord) (string, bool) {
	// Explicit ownership outranks ambiguous 429/5xx status. Unknown owners do
	// not erase legacy phase-based classification.
	switch strings.ToLower(r.ErrorOwner) {
	case "client", "user", "gateway", "platform", "internal":
		return "non_supplier", false
	}
	return classifyTrafficFailure(r.StatusCode, r.Phase, r.Reason+" "+r.NativeType+" "+r.Code)
}
