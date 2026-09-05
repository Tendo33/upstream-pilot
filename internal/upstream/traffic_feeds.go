package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	trafficRequests       = "requests"
	trafficUpstreamErrors = "upstream_errors"
	trafficRequestErrors  = "request_errors"
)

type TrafficFeed struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Rows      int    `json:"rows"`
	Truncated bool   `json:"truncated"`
}

// Each bounded feed has its own budget, so successful traffic cannot crowd
// supplier failures out of the sample. All three share the same time window.
func (c *Sub2Client) fetchTraffic(ctx context.Context, query url.Values) (TrafficBatch, error) {
	now := time.Now().UTC()
	b := TrafficBatch{Status: "ok", WindowStart: now.Add(-15 * time.Minute), WindowEnd: now, Feeds: map[string]TrafficFeed{}}
	types := []string{trafficRequests, trafficUpstreamErrors, trafficRequestErrors}
	paths := []string{"/ops/requests", "/ops/upstream-errors", "/ops/request-errors"}
	type result struct {
		feed    TrafficFeed
		records []TrafficRecord
		err     error
	}
	results := make([]result, len(types))
	var wg sync.WaitGroup
	for i := range types {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q, _ := url.ParseQuery(query.Encode())
			q.Set("start_time", b.WindowStart.Format(time.RFC3339Nano))
			q.Set("end_time", b.WindowEnd.Format(time.RFC3339Nano))
			results[i].feed, results[i].records, results[i].err = c.fetchTrafficFeed(ctx, paths[i], types[i], q, b.WindowStart, b.WindowEnd)
		}(i)
	}
	wg.Wait()
	available := 0
	var errs []error
	for i, r := range results {
		b.Feeds[types[i]] = r.feed
		b.Records = append(b.Records, r.records...)
		b.Truncated = b.Truncated || r.feed.Truncated
		if r.feed.Status == "ok" {
			available++
		}
		if r.err != nil {
			errs = append(errs, r.err)
		}
	}
	if available < len(types) {
		b.Status = "partial"
		b.Message = "部分请求接口不可用；已收到的失败仍保留，不能据此认证完整成功率"
	}
	if available == 0 && len(b.Records) == 0 {
		b.Status = "unsupported"
		b.Message = "站点未提供兼容的请求明细接口"
		if len(errs) > 0 {
			b.Status = "error"
			b.Message = "请求接口采集失败"
		}
	}
	return b, errors.Join(errs...)
}

func (c *Sub2Client) fetchTrafficFeed(ctx context.Context, path, source string, q url.Values, start, end time.Time) (TrafficFeed, []TrafficRecord, error) {
	f := TrafficFeed{Status: "ok"}
	records := []TrafficRecord{}
	seen := map[string]bool{}
	q.Set("page_size", "100")
	if source == trafficRequests {
		q.Set("kind", "all")
		q.Set("sort", "created_at_desc")
	} else {
		q.Set("sort_by", "created_at")
		q.Set("sort_order", "desc")
		q.Set("view", "all")
	}
	for page := 1; page <= 3; page++ {
		q.Set("page", strconv.Itoa(page))
		raw, err := c.request(ctx, http.MethodGet, path+"?"+q.Encode(), nil, "application/json")
		if err != nil {
			var he *HTTPError
			if errors.As(err, &he) && (he.Status == 404 || he.Status == 405) {
				f.Status = "unsupported"
				f.Message = "接口未提供"
				return f, records, nil
			}
			f.Status = "error"
			f.Message = "接口读取失败，请检查权限与连接"
			return f, records, err
		}
		payload, err := unwrapJSON(raw)
		var list struct {
			Items []json.RawMessage `json:"items"`
			Total *int              `json:"total"`
		}
		if err == nil {
			err = json.Unmarshal(payload, &list)
		}
		if err != nil || list.Items == nil {
			f.Status = "unsupported"
			f.Message = "接口记录结构不兼容"
			return f, records, err
		}
		for _, raw := range list.Items {
			var r TrafficRecord
			if err = json.Unmarshal(raw, &r); err != nil {
				f.Status = "partial"
				f.Message = "部分记录格式不兼容"
				continue
			}
			r.Source = source
			if source != trafficRequests {
				var extra struct {
					ID              int64  `json:"id"`
					ClientRequestID string `json:"client_request_id"`
				}
				if json.Unmarshal(raw, &extra) != nil || extra.ID <= 0 {
					f.Status = "partial"
					f.Message = "错误记录缺少有效标识"
					continue
				}
				r.ErrorID = &extra.ID
				r.Kind = "error"
				if r.RequestID == "" {
					r.RequestID = extra.ClientRequestID
				}
			}
			if len(r.Model) > 256 || len(r.RequestID) > 256 || r.CreatedAt.Before(start) || r.CreatedAt.After(end) {
				continue
			}
			group := ""
			if r.GroupID != nil {
				group = strconv.FormatInt(*r.GroupID, 10)
			}
			key := group + "/" + r.RequestID + "/" + r.Kind + "/" + strconv.FormatInt(r.AccountID, 10) + "/" + r.CreatedAt.Format(time.RFC3339Nano)
			if r.ErrorID != nil {
				key += "/" + strconv.FormatInt(*r.ErrorID, 10)
			}
			if (r.RequestID != "" || r.ErrorID != nil) && seen[key] {
				continue
			}
			seen[key] = true
			records = append(records, r)
		}
		f.Rows = len(records)
		if len(list.Items) < 100 || list.Total != nil && page*100 >= *list.Total {
			break
		}
		if page == 3 {
			f.Truncated = true
		}
	}
	return f, records, nil
}
