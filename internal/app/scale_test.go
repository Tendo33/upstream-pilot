package app

import (
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestScale500GroupDashboardIsBounded(t *testing.T) {
	if os.Getenv("PILOT_SCALE_TESTS") != "1" {
		t.Skip("set PILOT_SCALE_TESTS=1 for scale acceptance")
	}
	a, w, _, _ := newQualityIntegration(t)
	group, other := seedEnginePool(t, a, w, 20)
	seedSupplierIdentity(t, a, w.ID)
	seedSupplierIdentity(t, a, other)
	engineRegressionSQL(t, a, `WITH added AS(INSERT INTO upstream_accounts(id,site_id,remote_id,name,platform,account_type,remote_status,schedulable,priority,probe_model,source_mapping_fingerprint,native_constraints,native_checked_at) SELECT gen_random_uuid(),$1,1000+n,'scale-'||n,'openai','apikey','active',true,20,'test-model',$2,(SELECT native_constraints FROM upstream_accounts WHERE id=$3),now() FROM generate_series(1,498)n RETURNING id) INSERT INTO account_group_memberships(account_id,group_id,site_id) SELECT id,$4,$1 FROM added`, w.SiteID, qualityTestMappingFingerprint(), w.ID, group)
	engineRegressionSQL(t, a, `INSERT INTO quality_states(account_id,baseline_priority,desired_priority,status,last_sample_at,source_generation,evaluated_at) SELECT id,20,20,'healthy',now(),source_generation,now() FROM upstream_accounts WHERE site_id=$1 ON CONFLICT(account_id) DO UPDATE SET status='healthy',last_sample_at=now(),source_generation=excluded.source_generation,evaluated_at=now()`, w.SiteID)
	engineRegressionSQL(t, a, `INSERT INTO account_operations(account_id,source_generation,config) SELECT id,source_generation,jsonb_build_object('provider',id::text,'failure_domain',id::text,'quota_pool',id::text,'confirmed',true) FROM upstream_accounts WHERE site_id=$1 ON CONFLICT(account_id) DO UPDATE SET source_generation=excluded.source_generation,config=excluded.config`, w.SiteID)
	beforeQueries := a.db.Stat().AcquireCount()
	var before, runtimeAfter runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	request := qualityHandlerRequest("GET", "/quality/groups", w.ID, w.OwnerID, "")
	response := httptest.NewRecorder()
	if err := a.qualityGroupHandler(response, request); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&runtimeAfter)
	queries := a.db.Stat().AcquireCount() - beforeQueries
	if response.Code != 200 || elapsed > 2*time.Second || queries > 10 {
		t.Fatalf("scale dashboard: status=%d elapsed=%s DB acquisitions=%d", response.Code, elapsed, queries)
	}
	if runtimeAfter.TotalAlloc-before.TotalAlloc > 64<<20 {
		t.Fatalf("scale dashboard allocated %.1f MB", float64(runtimeAfter.TotalAlloc-before.TotalAlloc)/(1<<20))
	}
	t.Logf("500 accounts: elapsed=%s DB acquisitions=%d allocated=%.1fMB", elapsed, queries, float64(runtimeAfter.TotalAlloc-before.TotalAlloc)/(1<<20))
}
