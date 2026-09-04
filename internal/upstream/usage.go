package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Only operational identifiers and token counts are retained. Customer billing
// costs, prompts, user agent, email and IP are deliberately not collected.
type UsageRecord struct {
	ID                 int64     `json:"id"`
	AccountID          int64     `json:"account_id"`
	GroupID            *int64    `json:"group_id"`
	RequestID          string    `json:"request_id"`
	Model              string    `json:"model"`
	InputTokens        int64     `json:"input_tokens"`
	OutputTokens       int64     `json:"output_tokens"`
	CacheReadTokens    int64     `json:"cache_read_tokens"`
	CacheWriteTokens   int64     `json:"cache_creation_tokens"`
	NativeFirstChunkMS *int      `json:"first_token_ms"`
	SessionID          string    `json:"session_id"`
	CreatedAt          time.Time `json:"created_at"`
}
type UsageCollection struct {
	Status    string        `json:"status"`
	Message   string        `json:"message"`
	CheckedAt time.Time     `json:"checked_at"`
	Truncated bool          `json:"truncated"`
	Count     int           `json:"count"`
	Records   []UsageRecord `json:"-"`
}

func (c *Sub2Client) RecentUsage(ctx context.Context) (UsageCollection, error) {
	now := time.Now().UTC()
	result := UsageCollection{Status: "unknown", CheckedAt: now}
	seen := map[int64]bool{}
	q := url.Values{"page_size": {"100"}, "start_date": {now.Add(-24 * time.Hour).Format("2006-01-02")}, "end_date": {now.Add(24 * time.Hour).Format("2006-01-02")}}
	for page := 1; page <= 3; page++ {
		q.Set("page", strconv.Itoa(page))
		raw, err := c.request(ctx, http.MethodGet, "/usage?"+q.Encode(), nil, "application/json")
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) && (httpErr.Status == 404 || httpErr.Status == 405) {
				result.Status = "unsupported"
				result.Message = "未提供 usage 接口"
				return result, nil
			}
			result.Status = "error"
			result.Message = "用量采集失败"
			return result, err
		}
		data, err := unwrapJSON(raw)
		if err != nil {
			return result, err
		}
		var list struct {
			Items []UsageRecord `json:"items"`
			Total int           `json:"total"`
		}
		if err = json.Unmarshal(data, &list); err != nil {
			return result, err
		}
		if list.Items == nil {
			result.Status = "unsupported"
			result.Message = "用量接口结构不兼容"
			return result, nil
		}
		for _, v := range list.Items {
			if v.ID <= 0 || seen[v.ID] || len(v.Model) > 256 || len(v.RequestID) > 256 || v.CreatedAt.Before(now.Add(-24*time.Hour)) || v.CreatedAt.After(now) {
				continue
			}
			seen[v.ID] = true
			if v.InputTokens < 0 || v.OutputTokens < 0 || v.CacheReadTokens < 0 || v.CacheWriteTokens < 0 {
				continue
			}
			result.Records = append(result.Records, v)
		}
		if len(list.Items) < 100 || page*100 >= list.Total {
			break
		}
		if page == 3 {
			result.Truncated = true
		}
	}
	result.Status = "ok"
	result.Count = len(result.Records)
	return result, nil
}
