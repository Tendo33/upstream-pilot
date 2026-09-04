package upstream

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestVersionedContractFieldsRemainConservative(t *testing.T) {
	for _, tc := range []struct{ file, state string }{{"sub2api-c043c247-account.json", "eligible"}, {"sub2api-legacy-account.json", "unknown"}} {
		raw, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		var account Sub2Account
		if err = json.Unmarshal(raw, &account); err != nil {
			t.Fatal(err)
		}
		if got := account.NativeEligibility("public-alias", []int64{1}, time.Now()); got.State != tc.state {
			t.Fatalf("%s: %+v", tc.file, got)
		}
		c := InventoryCapabilities("contract-test", []Sub2Account{account})
		if c.Features["traffic_ttft"].State != "unknown" || c.Features["control_write"].State != "unknown" {
			t.Fatal("version string invented capabilities")
		}
		if tc.state == "eligible" && account.ObservedSourceCredentialFingerprintKnown {
			t.Fatal("redacted key certified as a known identity")
		}
	}
}
