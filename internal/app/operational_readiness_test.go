package app

import (
	"context"
	"encoding/json"
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGroupBackupsRequireCurrentQualityAndFreshNativeEvidence(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	_, backup := seedEnginePool(t, a, w, 20)
	engineRegressionSQL(t, a, `INSERT INTO quality_states(account_id,baseline_priority,desired_priority,status,last_sample_at,evaluated_at,source_generation) SELECT id,20,20,'healthy',now(),now(),source_generation FROM upstream_accounts WHERE site_id=$1`, w.SiteID)
	check := func(expected int) {
		t.Helper()
		out := httptest.NewRecorder()
		if err := a.qualityGroupHandler(out, qualityHandlerRequest("GET", "/quality/groups", "", w.OwnerID, "")); err != nil {
			t.Fatal(err)
		}
		var data struct {
			Data []struct {
				Healthy     int `json:"healthy"`
				Independent int `json:"independent_healthy"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Data) != 1 || data.Data[0].Healthy != expected || data.Data[0].Independent != expected {
			t.Fatalf("expected %d current candidates: %s", expected, out.Body.String())
		}
	}
	check(2)
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET native_checked_at=now()-interval '6 minutes' WHERE id=$1`, backup)
	check(1)
	engineRegressionSQL(t, a, `UPDATE upstream_accounts SET native_checked_at=now() WHERE id=$1`, backup)
	engineRegressionSQL(t, a, `UPDATE quality_states SET source_generation=source_generation+1 WHERE account_id=$1`, backup)
	check(1)
	engineRegressionSQL(t, a, `UPDATE quality_states q SET source_generation=a.source_generation,evaluated_at=now()-interval '11 minutes' FROM upstream_accounts a WHERE a.id=q.account_id AND a.id=$1`, backup)
	check(1)
	engineRegressionSQL(t, a, `UPDATE quality_states SET evaluated_at=now(),last_sample_at=now()+interval '1 hour' WHERE account_id=$1`, backup)
	check(1)
	engineRegressionSQL(t, a, `UPDATE quality_states SET last_sample_at=now() WHERE account_id=$1`, backup)
	check(2)
}

func TestOperationsShowsExpiredAndDisabledBillingCoverage(t *testing.T) {
	a, w, _, _ := newQualityIntegration(t)
	now := time.Now().UTC()
	yes, no := true, false
	expired := now.Add(-time.Minute)
	fresh := now.Add(time.Minute)
	cap := upstream.Capabilities{CheckedAt: now, NativeBilling: []upstream.NativeBillingObservation{
		{AccountID: 1, Name: "valid", Enabled: &yes, Status: "ok", LastAttemptAt: &now, FreshUntil: &fresh},
		{AccountID: 2, Name: "expired", Enabled: &yes, Status: "ok", LastAttemptAt: &now, FreshUntil: &expired},
		{AccountID: 3, Name: "disabled", Enabled: &no, Status: "ok", LastAttemptAt: &now, FreshUntil: &fresh},
		{AccountID: 4, Name: "unsupported", Enabled: &yes, Status: "unsupported"},
	}}
	raw, _ := json.Marshal(cap)
	engineRegressionSQL(t, a, `UPDATE sites SET capabilities=$2 WHERE id=$1`, w.SiteID, raw)
	out := httptest.NewRecorder()
	if err := a.operationsHandler(out, qualityHandlerRequest("GET", "/operations", "", w.OwnerID, "")); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data struct {
			Sites []struct {
				Billing struct {
					Counts map[string]int `json:"counts"`
				} `json:"native_billing"`
			} `json:"sites"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Sites) != 1 {
		t.Fatal(out.Body.String())
	}
	for _, state := range []string{"fresh", "stale", "disabled", "unsupported"} {
		if response.Data.Sites[0].Billing.Counts[state] != 1 {
			t.Fatal(out.Body.String())
		}
	}
	var changes int
	if err := a.db.QueryRow(context.Background(), `SELECT count(*) FROM engine_actions`).Scan(&changes); err != nil || changes != 0 {
		t.Fatal(changes, err)
	}
}
