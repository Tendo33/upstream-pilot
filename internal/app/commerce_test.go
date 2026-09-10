package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizePaymentStatus(t *testing.T) {
	for input, want := range map[string]string{"PENDING": "pending", "PAID": "paid", "RECHARGING": "recharging", "COMPLETED": "completed", "EXPIRED": "expired", "FAILED": "failed", "cancelled": "cancelled", "other": "unknown"} {
		if got := NormalizePaymentStatus(input); got != want {
			t.Errorf("%q -> %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeBillingResponsePreservesTotals(t *testing.T) {
	v := sourceBillingResponse{Items: []PaymentOrder{{NormalizedStatus: "COMPLETED"}}, Summary: PaymentOrderSummary{CountByStatus: map[string]int64{"COMPLETED": 2, "success": 3}, AmountByStatus: map[string]float64{"COMPLETED": 10, "success": 20}}}
	normalizeBillingResponse(&v)
	if v.Items[0].NormalizedStatus != "completed" || v.Summary.CountByStatus["completed"] != 5 || v.Summary.AmountByStatus["completed"] != 30 {
		t.Fatalf("normalization lost data: %+v", v)
	}
	if v.Items[0].Currency != nil {
		t.Fatal("unknown currency must stay unknown")
	}
}

func TestRetiredReadRoutesAreUnavailable(t *testing.T) {
	router, ok := (&App{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("router does not expose chi routes")
	}
	for _, path := range []string{"/api/v1/operations/billing/orders", "/api/v1/operations/billing/summary", "/api/v1/operations/redemptions", "/api/v1/operations/risk/summary", "/api/v1/operations/capabilities"} {
		if router.Match(chi.NewRouteContext(), http.MethodGet, path) {
			t.Fatalf("retired %s still registered", path)
		}
	}
	if !router.Match(chi.NewRouteContext(), http.MethodGet, "/api/v1/source/billing/orders") {
		t.Fatal("source billing route missing")
	}
	if !router.Match(chi.NewRouteContext(), http.MethodGet, "/api/v1/source/audit-logs") {
		t.Fatal("source audit route missing")
	}
}

func TestCommerceWithoutDatabaseDoesNotFallBack(t *testing.T) {
	app := &App{}
	for _, run := range []handler{
		app.sourceBillingHandler,
		app.sourceRedeemHandler,
		app.sourceOverviewHandler,
		app.sourceRiskHandler,
		app.serviceUsersHandler,
		app.serviceUserDetailHandler,
		app.sourceAuditHandler,
		app.sourceModerationHandler,
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/source/read", nil)
		request = request.WithContext(context.WithValue(request.Context(), identityKey, Identity{User: User{Role: "admin"}}))
		response := httptest.NewRecorder()
		app.wrap(run)(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "SOURCE_DATABASE_REQUIRED") {
			t.Fatalf("missing source was hidden: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestSourceCapabilityErrorDistinguishesMissingColumn(t *testing.T) {
	err := sourceCapabilityError(&pgconn.PgError{Code: "42703", Message: `column "load_factor" does not exist`}, "Sub2API 数据库缺少 accounts 表，无法同步库存")
	apiErr, ok := err.(*apiError)
	if !ok || !strings.Contains(apiErr.Message, "字段不完整") {
		t.Fatalf("column error = %#v", err)
	}
	err = sourceCapabilityError(&pgconn.PgError{Code: "42P01", Message: `relation "accounts" does not exist`}, "Sub2API 数据库缺少 accounts 表，无法同步库存")
	apiErr, ok = err.(*apiError)
	if !ok || apiErr.Message != "Sub2API 数据库缺少 accounts 表，无法同步库存" {
		t.Fatalf("table error = %#v", err)
	}
}

func TestSaveRiskReviewRejectsInvalidSubject(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/operations/risk/reviews", strings.NewReader(`{"subject_type":"account","subject_id":"1","status":"open"}`))
	request = request.WithContext(context.WithValue(request.Context(), identityKey, Identity{User: User{Role: "admin"}}))
	response := httptest.NewRecorder()
	app.wrap(app.saveRiskReview)(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_RISK_REVIEW") {
		t.Fatalf("invalid review was accepted: %d %s", response.Code, response.Body.String())
	}
}
