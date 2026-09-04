package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/S2AM-GO/internal/auditlog"
)

func (a *App) overview(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var result struct {
		Sites          int `json:"sites"`
		Accounts       int `json:"accounts"`
		Automated      int `json:"automated"`
		Healthy        int `json:"healthy"`
		Failing        int `json:"failing"`
		Paused         int `json:"paused"`
		RecentFailures int `json:"recent_failures"`
	}
	err := a.db.QueryRow(r.Context(), `
		SELECT
		 (SELECT count(*) FROM sites WHERE owner_id=$1),
		 count(a.*),
		 count(*) FILTER (WHERE a.health_enabled OR a.rate_sync_enabled OR a.priority_enabled OR a.guard_enabled),
		 count(*) FILTER (WHERE a.health_state='healthy'),
		 count(*) FILTER (WHERE a.health_state='failing'),
		 count(*) FILTER (WHERE a.health_state='paused'),
		 (SELECT count(*) FROM probe_attempts WHERE owner_id=$1 AND success=false AND created_at>now()-interval '24 hours')
		FROM upstream_accounts a JOIN sites s ON s.id=a.site_id
		WHERE s.owner_id=$1 AND a.deleted_at IS NULL`, identity.ID).Scan(
		&result.Sites, &result.Accounts, &result.Automated, &result.Healthy, &result.Failing, &result.Paused, &result.RecentFailures,
	)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, result)
	return nil
}

func (a *App) listEvents(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	page, err := eventPageParameter(r, "page", 1, 1_000_000)
	if err != nil {
		return err
	}
	pageSizeName := "page_size"
	pageSizeRaw := strings.TrimSpace(r.URL.Query().Get(pageSizeName))
	if pageSizeRaw == "" && strings.TrimSpace(r.URL.Query().Get("limit")) != "" {
		pageSizeName = "limit"
	}
	pageSize, err := eventPageParameter(r, pageSizeName, 50, 200)
	if err != nil {
		return err
	}
	result, err := a.auditLog.List(identity.ID, page, pageSize)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, eventPageResponse(result))
	return nil
}

type auditEventResponse struct {
	ID          string         `json:"id"`
	Action      string         `json:"action"`
	Outcome     string         `json:"outcome"`
	Detail      map[string]any `json:"detail"`
	CreatedAt   time.Time      `json:"created_at"`
	SiteID      string         `json:"site_id,omitempty"`
	AccountID   string         `json:"account_id,omitempty"`
	SiteName    string         `json:"site_name"`
	AccountName string         `json:"account_name"`
}

type auditEventPageResponse struct {
	Items       []auditEventResponse `json:"items"`
	Page        int                  `json:"page"`
	PageSize    int                  `json:"page_size"`
	Total       int                  `json:"total"`
	TotalPages  int                  `json:"total_pages"`
	HasPrevious bool                 `json:"has_previous"`
	HasNext     bool                 `json:"has_next"`
}

func eventPageResponse(page auditlog.Page) auditEventPageResponse {
	items := make([]auditEventResponse, 0, len(page.Items))
	for _, record := range page.Items {
		detail := record.Detail
		if detail == nil {
			detail = map[string]any{}
		}
		items = append(items, auditEventResponse{
			ID:          record.ID,
			Action:      record.Action,
			Outcome:     record.Outcome,
			Detail:      detail,
			CreatedAt:   record.CreatedAt,
			SiteID:      record.SiteID,
			AccountID:   record.AccountID,
			SiteName:    record.SiteName,
			AccountName: record.AccountName,
		})
	}
	return auditEventPageResponse{
		Items:       items,
		Page:        page.Page,
		PageSize:    page.PageSize,
		Total:       page.Total,
		TotalPages:  page.TotalPages,
		HasPrevious: page.HasPrevious,
		HasNext:     page.HasNext,
	}
}

func eventPageParameter(r *http.Request, name string, fallback, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		code := "INVALID_PAGE"
		message := "页码必须是有效的正整数"
		if name == "page_size" || name == "limit" {
			code = "INVALID_PAGE_SIZE"
			message = "每页数量必须在 1 到 200 之间"
		}
		return 0, &apiError{Status: http.StatusBadRequest, Code: code, Message: message}
	}
	return value, nil
}
