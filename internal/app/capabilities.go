package app

import (
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"net/http"
	"time"
)

func (a *App) siteCapabilitiesHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "siteID")
	if err != nil {
		return err
	}
	if _, err = a.siteSecret(r.Context(), id, identityFrom(r).ID); err != nil {
		return err
	}
	var raw []byte
	if err = a.db.QueryRow(r.Context(), `SELECT capabilities FROM sites WHERE id=$1`, id).Scan(&raw); err != nil {
		return err
	}
	report := upstream.InventoryCapabilities("", nil)
	report.CheckedAt = time.Time{}
	report.Features["inventory_read"] = upstream.Capability{State: "unknown", Detail: "尚未成功同步库存"}
	if err = json.Unmarshal(raw, &report); err != nil {
		return err
	}
	// Permissions may differ by field and account. A successful priority update
	// is evidence only for priority, not for scheduling, capacity or every account.
	var successes, failures int
	if err = a.db.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE d.applied),count(*) FILTER(WHERE d.error<>'') FROM quality_decisions d JOIN upstream_accounts a ON a.id=d.account_id WHERE a.site_id=$1 AND d.created_at>now()-interval '24 hours' AND d.detail->>'source_generation'=a.source_generation::text`, id).Scan(&successes, &failures); err != nil {
		return err
	}
	if successes > 0 {
		report.Features["control_write"] = upstream.Capability{State: "partial", Detail: "最近 24 小时有动作通过读回；仅证明对应账号和字段权限"}
	}
	if failures > 0 {
		report.Features["control_write"] = upstream.Capability{State: "partial", Detail: "最近 24 小时存在未确认控制；请查看事件与具体动作历史"}
	}
	rows, err := a.db.Query(r.Context(), `SELECT t.snapshot FROM quality_traffic t JOIN upstream_accounts a ON a.id=t.account_id WHERE a.site_id=$1 AND t.source_generation=a.source_generation AND t.checked_at>now()-interval '10 minutes'`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var traffic upstream.TrafficSummary
		if err = json.Unmarshal(raw, &traffic); err != nil {
			return err
		}
		if traffic.Status == "ok" {
			report.Features["traffic_read"] = upstream.Capability{State: "available", Detail: "真实请求接口已成功读取"}
		} else if report.Features["traffic_read"].State != "available" {
			report.Features["traffic_read"] = upstream.Capability{State: traffic.Status, Detail: traffic.Message}
		}
		if traffic.TTFTAvailable {
			report.Features["traffic_ttft"] = upstream.Capability{State: "available", Detail: "样本包含 time_to_first_token_ms；未测量样本不参与首字统计"}
		}
		if traffic.CompletionAvailable {
			report.Features["traffic_completion"] = upstream.Capability{State: "available", Detail: "样本包含显式 stream_complete；缺失字段仍为未知"}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeData(w, 200, report)
	return nil
}
