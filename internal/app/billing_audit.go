package app

import (
	"strings"
	"time"
)

// Commerce response models belong to the database read path, not Sub2API's HTTP client.
type PaymentOrder struct {
	ID               int64      `json:"id"`
	UserID           int64      `json:"user_id"`
	NormalizedStatus string     `json:"normalized_status"`
	Amount           float64    `json:"amount"`
	Currency         *string    `json:"currency"`
	CreatedAt        *time.Time `json:"created_at"`
	CompletedAt      *time.Time `json:"completed_at"`
}
type PaymentOrderSummary struct {
	CountByStatus  map[string]int64   `json:"count_by_status"`
	AmountByStatus map[string]float64 `json:"amount_by_status"`
}
type sourceBillingResponse struct {
	Items    []PaymentOrder      `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	Summary  PaymentOrderSummary `json:"summary"`
}

func normalizeBillingResponse(v *sourceBillingResponse) {
	for i := range v.Items {
		v.Items[i].NormalizedStatus = NormalizePaymentStatus(v.Items[i].NormalizedStatus)
	}
	counts := map[string]int64{}
	amounts := map[string]float64{}
	for k, n := range v.Summary.CountByStatus {
		counts[NormalizePaymentStatus(k)] += n
	}
	for k, n := range v.Summary.AmountByStatus {
		amounts[NormalizePaymentStatus(k)] += n
	}
	v.Summary.CountByStatus = counts
	v.Summary.AmountByStatus = amounts
}

type redeemCodeView struct {
	ID        int64      `json:"id"`
	Code      string     `json:"code"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	Value     float64    `json:"value"`
	UsedBy    *int64     `json:"used_by"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt *time.Time `json:"created_at"`
}

func maskRedeemCode(code string) string {
	if len(code) <= 6 {
		return "******"
	}
	return code[:3] + "******" + code[len(code)-3:]
}

func NormalizePaymentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return "pending"
	case "paid":
		return "paid"
	case "recharging", "recharge", "processing":
		return "recharging"
	case "completed", "complete", "success":
		return "completed"
	case "expired", "timeout":
		return "expired"
	case "failed", "failure":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "unknown"
	}
}
