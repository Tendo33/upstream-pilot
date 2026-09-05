package upstream

import (
	"encoding/json"
	"time"
)

// Only collection coverage is retained. A native declaration does not establish
// a comparable price, account balance, or the current credential's identity.
type NativeBillingObservation struct {
	AccountID     int64      `json:"account_id"`
	Name          string     `json:"name"`
	Enabled       *bool      `json:"enabled"`
	Status        string     `json:"status"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	FreshUntil    *time.Time `json:"fresh_until"`
}

func parseNativeBilling(raw json.RawMessage, id int64, name string) NativeBillingObservation {
	v := NativeBillingObservation{AccountID: id, Name: name, Status: "unknown"}
	var extra struct {
		Enabled *bool `json:"upstream_billing_probe_enabled"`
		Probe   struct {
			Status        string     `json:"status"`
			LastAttemptAt *time.Time `json:"last_attempt_at"`
			FreshUntil    *time.Time `json:"fresh_until"`
		} `json:"upstream_billing_probe"`
	}
	if json.Unmarshal(raw, &extra) != nil {
		return v
	}
	v.Enabled = extra.Enabled
	v.LastAttemptAt = extra.Probe.LastAttemptAt
	v.FreshUntil = extra.Probe.FreshUntil
	switch extra.Probe.Status {
	case "ok", "unsupported", "failed":
		v.Status = extra.Probe.Status
	}
	return v
}

func (v NativeBillingObservation) Coverage(now time.Time, inventoryAt time.Time) string {
	if inventoryAt.IsZero() || inventoryAt.After(now.Add(time.Second)) || now.Sub(inventoryAt) > 10*time.Minute {
		return "stale"
	}
	if v.Enabled == nil {
		return "unknown"
	}
	if !*v.Enabled {
		return "disabled"
	}
	if v.Status != "ok" {
		return v.Status
	}
	if v.LastAttemptAt == nil || v.LastAttemptAt.After(now.Add(time.Second)) || v.FreshUntil == nil || !v.FreshUntil.After(now) {
		return "stale"
	}
	return "fresh"
}
