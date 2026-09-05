package upstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeBillingCoverageDoesNotLeakValuesAndExpires(t *testing.T) {
	now := time.Now().UTC()
	raw, _ := json.Marshal(map[string]any{"id": 7, "name": "synthetic", "extra": map[string]any{"upstream_billing_probe_enabled": true, "upstream_billing_probe": map[string]any{"status": "ok", "last_attempt_at": now, "fresh_until": now.Add(time.Minute), "data": map[string]any{"api_key": "do-not-retain", "balance": 123, "effective_rate_multiplier": 0.1}, "last_error": "private-error"}}})
	var a Sub2Account
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	c := InventoryCapabilities("0.2.0", []Sub2Account{a})
	encoded, _ := json.Marshal(c)
	for _, secret := range []string{"do-not-retain", "private-error", "effective_rate_multiplier", "balance"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("retained arbitrary declaration data: %s", secret)
		}
	}
	if a.NativeBilling.Coverage(now, now) != "fresh" || a.NativeBilling.Coverage(now.Add(2*time.Minute), now) != "stale" || a.NativeBilling.Coverage(now, now.Add(-11*time.Minute)) != "stale" {
		t.Fatal(a.NativeBilling)
	}
	no := false
	a.NativeBilling.Enabled = &no
	if a.NativeBilling.Coverage(now, now) != "disabled" {
		t.Fatal("disabled collector reported fresh")
	}
}
