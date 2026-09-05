package upstream

import "time"

type Capability struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type Capabilities struct {
	NativeBilling []NativeBillingObservation `json:"native_billing"`
	Version       string                     `json:"version"`
	CheckedAt     time.Time                  `json:"checked_at"`
	Features      map[string]Capability      `json:"features"`
}

// InventoryCapabilities describes observations, never guessed version ranges.
// A version string is diagnostic context; actual field contracts decide support.
func InventoryCapabilities(version string, accounts []Sub2Account) Capabilities {
	c := Capabilities{Version: version, CheckedAt: time.Now().UTC(), Features: map[string]Capability{
		"inventory_read":     {"available", "账号与分组列表已成功读取"},
		"control_write":      {"unknown", "尚未通过实际动作及读回验证写权限"},
		"supplier_attempts":  {"unknown", "等待上游尝试错误接口采集"},
		"request_failures":   {"unknown", "等待最终请求错误接口采集"},
		"traffic_read":       {"unknown", "等待真实请求接口采集"},
		"traffic_ttft":       {"unknown", "请求总耗时不能代替首字时间"},
		"traffic_completion": {"unknown", "等待明确的流结束字段"},
		"native_eligibility": {"unknown", "没有足够的账号运行约束样本"},
		"source_identity":    {"unknown", "缺少可验证的来源端点或凭据指纹"},
	}}
	if len(accounts) == 0 {
		return c
	}
	constraints, identities := 0, 0
	for _, a := range accounts {
		c.NativeBilling = append(c.NativeBilling, a.NativeBilling)
		if a.Native.Known && a.Native.MappingKnown && a.Native.GroupsKnown {
			constraints++
		}
		if a.ObservedSourceBaseURLKnown && a.ObservedSourceCredentialFingerprintKnown && a.ObservedSourceCredentialFingerprint != "" {
			identities++
		}
	}
	if constraints == len(accounts) {
		c.Features["native_eligibility"] = Capability{"available", "已收到原生运行约束；每个候选仍需单独核对"}
	} else if constraints > 0 {
		c.Features["native_eligibility"] = Capability{"partial", "只有部分账号提供完整运行约束"}
	}
	if identities == len(accounts) {
		c.Features["source_identity"] = Capability{"available", "来源地址及凭据指纹可核对；凭据不在报告中显示"}
	} else if identities > 0 {
		c.Features["source_identity"] = Capability{"partial", "部分账号未提供凭据身份；请检查导出权限与接口版本"}
	}
	return c
}
