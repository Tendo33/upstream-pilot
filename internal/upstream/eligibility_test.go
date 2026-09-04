package upstream

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeBackupEligibilityContract(t *testing.T) {
	now := time.Now().UTC()
	base := func() map[string]any {
		return map[string]any{"id": 8, "platform": "openai", "type": "apikey", "status": "active", "schedulable": true, "concurrency": 8, "rate_limit_reset_at": nil, "overload_until": nil, "temp_unschedulable_until": nil, "expires_at": nil, "auto_pause_on_expired": true, "extra": map[string]any{}, "credentials": map[string]any{"model_mapping": map[string]string{"test-model": "test-model"}}, "account_groups": []map[string]any{{"group_id": 1}}}
	}
	for _, tc := range []struct {
		name, want string
		change     func(map[string]any)
	}{
		{"compatible", "eligible", func(m map[string]any) {}},
		{"cooldown", "blocked", func(m map[string]any) { m["temp_unschedulable_until"] = now.Add(time.Hour) }},
		{"overload", "blocked", func(m map[string]any) { m["overload_until"] = now.Add(time.Hour) }},
		{"rate limit", "blocked", func(m map[string]any) { m["rate_limit_reset_at"] = now.Add(time.Hour) }},
		{"expired", "blocked", func(m map[string]any) { m["expires_at"] = now.Add(-time.Hour).Unix() }},
		{"quota", "blocked", func(m map[string]any) { m["extra"] = map[string]any{"quota_limit": 10, "quota_used": 10} }},
		{"quota unknown", "unknown", func(m map[string]any) { m["extra"] = map[string]any{"quota_daily_limit": 10} }},
		{"missing fields", "unknown", func(m map[string]any) { delete(m, "overload_until") }},
		{"bad timestamp", "unknown", func(m map[string]any) { m["overload_until"] = "garbled" }},
		{"missing group", "blocked", func(m map[string]any) { m["account_groups"] = []any{} }},
		{"group unknown", "unknown", func(m map[string]any) { delete(m, "account_groups") }},
		{"model mismatch", "blocked", func(m map[string]any) {
			m["credentials"] = map[string]any{"model_mapping": map[string]string{"other": "other"}}
		}},
		{"model unknown", "unknown", func(m map[string]any) { delete(m, "credentials") }},
		{"unsupported OAuth", "unknown", func(m map[string]any) { m["type"] = "oauth" }},
		{"protocol family", "unknown", func(m map[string]any) { m["platform"] = "gemini" }},
		{"manual stop", "blocked", func(m map[string]any) { m["schedulable"] = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.change(m)
			raw, _ := json.Marshal(m)
			var a Sub2Account
			if err := json.Unmarshal(raw, &a); err != nil {
				t.Fatal(err)
			}
			if got := a.NativeEligibility("test-model", []int64{1}, now); got.State != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRedactedCredentialDoesNotEraseKnownFingerprint(t *testing.T) {
	var a Sub2Account
	if err := json.Unmarshal([]byte(`{"credentials":{"base_url":"https://example.test"},"credentials_status":{"has_api_key":true}}`), &a); err != nil {
		t.Fatal(err)
	}
	if a.ObservedSourceCredentialFingerprintKnown {
		t.Fatal("redacted key was treated as removed")
	}
}

func TestTrafficFailureTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		status              int
		phase, reason, want string
		supplier            bool
	}{
		{401, "account_auth", "", "upstream_auth", true}, {401, "auth", "", "auth", false},
		{429, "request", "rate_limit_error", "request", false}, {503, "routing", "", "routing", false},
		{503, "upstream", "", "upstream_http", true}, {502, "network", "", "network", true},
		{429, "upstream", "insufficient_quota", "balance", true}, {429, "upstream", "", "rate_limit", true},
		{404, "upstream", "", "model_or_request", true}, {400, "", "", "unclassified", false},
	} {
		got, supplier := classifyTrafficFailure(tc.status, tc.phase, tc.reason)
		if got != tc.want || supplier != tc.supplier {
			t.Errorf("%+v: %s %v", tc, got, supplier)
		}
	}
}
