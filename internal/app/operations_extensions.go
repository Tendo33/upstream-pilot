package app

import (
	"net/http"
)

type operationsCapability struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Reason  string `json:"reason"`
}

// operationsCapabilitiesHandler exposes the local feature gates without probing
// or writing to an upstream. Adapters can refine these entries once a site's
// version capability scan has confirmed the corresponding API contract.
func (a *App) operationsCapabilitiesHandler(w http.ResponseWriter, r *http.Request) error {
	_ = r
	mode := func(enabled, readOnly bool) operationsCapability {
		if !a.config.OperationsExtensionsEnabled || !enabled {
			return operationsCapability{Enabled: false, Mode: "disabled", Reason: "运营扩展未启用"}
		}
		if readOnly {
			return operationsCapability{Enabled: true, Mode: "read_only", Reason: "等待站点能力探测确认实际字段"}
		}
		return operationsCapability{Enabled: true, Mode: "controlled_write", Reason: "需要官方管理接口和单独权限"}
	}
	writeData(w, http.StatusOK, map[string]any{
		"billing_audit": mode(a.config.BillingAuditEnabled, true),
		"redemption":    mode(a.config.RedemptionEnabled, false),
		"risk_analysis": mode(a.config.RiskAnalysisEnabled, true),
		"abusehub":      mode(a.config.AbuseHubEnabled, false),
		"source":        "local_configuration",
	})
	return nil
}
