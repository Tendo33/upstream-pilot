package app

import (
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"net/http"
	"strconv"
)

type billingAuditPage struct {
	SiteID   string `json:"site_id"`
	SiteName string `json:"site_name"`
	Items    any    `json:"items"`
	Total    int    `json:"total"`
}

type billingAuditResponse struct {
	billingAuditPage
	Summary upstream.PaymentOrderSummary `json:"summary"`
}

type redeemCodeView struct {
	ID        int64   `json:"id"`
	Code      string  `json:"code"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	Value     float64 `json:"value"`
	UsedBy    *int64  `json:"used_by"`
	ExpiresAt any     `json:"expires_at"`
	CreatedAt any     `json:"created_at"`
}

func maskRedeemCode(code string) string {
	if len(code) <= 6 {
		return "******"
	}
	return code[:3] + "******" + code[len(code)-3:]
}

func (a *App) redeemCodesHandler(w http.ResponseWriter, r *http.Request) error {
	if !a.config.OperationsExtensionsEnabled {
		return &apiError{Status: http.StatusNotFound, Code: "EXTENSIONS_DISABLED", Message: "运营扩展未启用"}
	}
	identity := identityFrom(r)
	siteID := r.URL.Query().Get("site_id")
	if siteID == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "SITE_REQUIRED", Message: "请选择一个 Sub2API 站点"}
	}
	site, err := a.siteSecret(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	codes, err := client.ListRedeemCodes(r.Context(), page, pageSize)
	if err != nil {
		return err
	}
	items := make([]redeemCodeView, 0, len(codes.Items))
	for _, code := range codes.Items {
		items = append(items, redeemCodeView{ID: code.ID, Code: maskRedeemCode(code.Code), Type: code.Type, Status: code.Status, Value: code.Value, UsedBy: code.UsedBy, ExpiresAt: code.ExpiresAt, CreatedAt: code.CreatedAt})
	}
	writeData(w, http.StatusOK, map[string]any{"site_id": site.ID, "site_name": site.Name, "items": items, "total": codes.Total})
	return nil
}

// billingAuditHandler is deliberately read-only. It proxies the upstream
// administrator order listing and keeps the source site visible in the result.
func (a *App) billingAuditHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	siteID := r.URL.Query().Get("site_id")
	if siteID == "" {
		return &apiError{Status: http.StatusBadRequest, Code: "SITE_REQUIRED", Message: "请选择一个 Sub2API 站点"}
	}
	site, err := a.siteSecret(r.Context(), siteID, identity.ID)
	if err != nil {
		return err
	}
	client, err := a.sub2Client(site)
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	orders, err := client.ListPaymentOrders(r.Context(), page, pageSize)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, billingAuditResponse{billingAuditPage: billingAuditPage{SiteID: site.ID, SiteName: site.Name, Items: orders.Items, Total: orders.Total}, Summary: upstream.SummarizePaymentOrders(orders.Items)})
	return nil
}
