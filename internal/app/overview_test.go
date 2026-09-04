package app

import (
	"net/http/httptest"
	"testing"
	"time"

	"sub2api-upstream-manager/internal/auditlog"
)

func TestEventPageParameter(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		parameter string
		fallback  int
		maximum   int
		want      int
		wantCode  string
	}{
		{name: "default page", parameter: "page", fallback: 1, maximum: 1_000_000, want: 1},
		{name: "explicit page", query: "page=12", parameter: "page", fallback: 1, maximum: 1_000_000, want: 12},
		{name: "page size", query: "page_size=200", parameter: "page_size", fallback: 50, maximum: 200, want: 200},
		{name: "invalid page", query: "page=0", parameter: "page", fallback: 1, maximum: 1_000_000, wantCode: "INVALID_PAGE"},
		{name: "invalid page size", query: "page_size=201", parameter: "page_size", fallback: 50, maximum: 200, wantCode: "INVALID_PAGE_SIZE"},
		{name: "invalid number", query: "page=one", parameter: "page", fallback: 1, maximum: 1_000_000, wantCode: "INVALID_PAGE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/events?"+test.query, nil)
			got, err := eventPageParameter(request, test.parameter, test.fallback, test.maximum)
			if test.wantCode == "" {
				if err != nil || got != test.want {
					t.Fatalf("got value=%d error=%v, want %d", got, err, test.want)
				}
				return
			}
			apiErr, ok := err.(*apiError)
			if !ok || apiErr.Code != test.wantCode {
				t.Fatalf("got error %#v, want code %s", err, test.wantCode)
			}
		})
	}
}

func TestEventPageResponseDoesNotExposeTenantFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	response := eventPageResponse(auditlog.Page{
		Items: []auditlog.Record{{
			ID:          "event-id",
			OwnerID:     "owner-secret",
			ActorUserID: "actor-secret",
			SiteID:      "site-id",
			AccountID:   "account-id",
			SiteName:    "Site snapshot",
			AccountName: "Account snapshot",
			Action:      "account.probe",
			Outcome:     "success",
			CreatedAt:   createdAt,
		}},
		Page: 1, PageSize: 25, Total: 1, TotalPages: 1,
	})
	if len(response.Items) != 1 {
		t.Fatalf("unexpected items: %+v", response.Items)
	}
	item := response.Items[0]
	if item.SiteName != "Site snapshot" || item.AccountName != "Account snapshot" || !item.CreatedAt.Equal(createdAt) {
		t.Fatalf("snapshot fields were not mapped: %+v", item)
	}
	if item.Detail == nil {
		t.Fatal("API detail must be an empty object, not null")
	}
}
