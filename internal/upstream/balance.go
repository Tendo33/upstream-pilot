package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const defaultNewAPIQuotaPerUnit = 500_000

type BalanceResult struct {
	Status    string   `json:"status"`
	Provider  string   `json:"provider,omitempty"`
	PlanName  string   `json:"plan_name,omitempty"`
	Remaining *float64 `json:"remaining"`
	Used      *float64 `json:"used,omitempty"`
	Total     *float64 `json:"total,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Message   string   `json:"message,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
}

// AccountUsageCredential is deliberately non-serializable. It exists only
// long enough to query the account's upstream usage endpoint.
type AccountUsageCredential struct {
	BaseURL string `json:"-"`
	APIKey  string `json:"-"`
}

// AccountUsageCredentials exports only the two fields needed for a usage
// query. Raw account credentials never leave the upstream package.
func (c *Sub2Client) AccountUsageCredentials(ctx context.Context, accountIDs []int64) (map[int64]AccountUsageCredential, error) {
	requested := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return nil, errors.New("Sub2API account ID must be positive")
		}
		if _, duplicate := seen[accountID]; duplicate {
			continue
		}
		seen[accountID] = struct{}{}
		requested = append(requested, accountID)
	}

	result := make(map[int64]AccountUsageCredential, len(requested))
	for start := 0; start < len(requested); start += accountExportBatchSize {
		end := min(start+accountExportBatchSize, len(requested))
		ids := make([]string, 0, end-start)
		for _, accountID := range requested[start:end] {
			ids = append(ids, strconv.FormatInt(accountID, 10))
		}
		query := url.Values{"ids": {strings.Join(ids, ",")}, "include_proxies": {"false"}}
		raw, err := c.request(ctx, http.MethodGet, "/accounts/data?"+query.Encode(), nil, "application/json")
		if err != nil {
			return nil, err
		}
		items, err := decodeExportedAccountObservations(raw)
		if err != nil {
			return nil, err
		}
		// Current Sub2API exports intentionally omit account IDs. A one-account
		// export is still unambiguous and is the compatibility path used by the
		// balance query.
		if end-start == 1 && len(items) == 1 && items[0].ID == 0 {
			items[0].ID = requested[start]
		}
		for _, item := range items {
			if _, ok := seen[item.ID]; !ok || item.Credentials == nil {
				continue
			}
			if credential, ok := parseAccountUsageCredential(item.Credentials); ok {
				result[item.ID] = credential
			}
		}
	}
	return result, nil
}

func parseAccountUsageCredential(raw json.RawMessage) (AccountUsageCredential, bool) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return AccountUsageCredential{}, false
	}

	type credentialNode struct {
		value any
		depth int
	}
	queue := []credentialNode{{value: root}}
	const maxDepth = 4
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if values, ok := node.value.([]any); ok {
			if node.depth < maxDepth {
				for _, value := range values {
					queue = append(queue, credentialNode{value: value, depth: node.depth + 1})
				}
			}
			continue
		}
		scope, ok := node.value.(map[string]any)
		if !ok {
			continue
		}
		credential := AccountUsageCredential{
			BaseURL: firstNonEmptyString(scope, "base_url", "baseUrl", "api_url", "apiUrl", "endpoint", "url"),
			APIKey:  firstNonEmptyString(scope, "api_key", "apiKey", "apikey", "key", "token", "access_token", "accessToken"),
		}
		if credential.BaseURL != "" && credential.APIKey != "" {
			return credential, true
		}
		if node.depth >= maxDepth {
			continue
		}

		keys := make([]string, 0, len(scope))
		for key := range scope {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := scope[key]
			switch nested := value.(type) {
			case map[string]any, []any:
				queue = append(queue, credentialNode{value: nested, depth: node.depth + 1})
			case string:
				trimmed := strings.TrimSpace(nested)
				if len(trimmed) < 2 || len(trimmed) > maxCredentialBytes || (trimmed[0] != '{' && trimmed[0] != '[') {
					continue
				}
				var decoded any
				if json.Unmarshal([]byte(trimmed), &decoded) == nil {
					queue = append(queue, credentialNode{value: decoded, depth: node.depth + 1})
				}
			}
		}
	}
	return AccountUsageCredential{}, false
}

// AccountUsageBalance reads the account usage snapshot exposed by recent
// Sub2API deployments. It avoids exporting account credentials for providers
// whose quota can already be read through the admin API.
func (c *Sub2Client) AccountUsageBalance(ctx context.Context, accountID int64) (BalanceResult, error) {
	if accountID <= 0 {
		return BalanceResult{}, errors.New("Sub2API account ID must be positive")
	}
	endpoint := "/accounts/" + strconv.FormatInt(accountID, 10) + "/usage"
	query := url.Values{"source": {"passive"}}
	raw, err := c.request(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, "application/json")
	if err != nil {
		return BalanceResult{}, err
	}
	data, err := unwrapJSON(raw)
	if err != nil {
		return BalanceResult{}, err
	}
	payload, err := decodeJSONObject(data)
	if err != nil {
		return BalanceResult{}, errors.New("Sub2API account usage returned invalid JSON")
	}
	result, ok := parseSub2APIUsageBalance(payload)
	if !ok {
		return BalanceResult{
			Status: "unsupported", Provider: "sub2api-admin",
			Message: "Sub2API 账号 usage 响应中没有可解析的配额字段", Endpoint: endpoint,
		}, nil
	}
	result.Endpoint = endpoint
	return result, nil
}

func parseSub2APIUsageBalance(payload map[string]any) (BalanceResult, bool) {
	if message := firstNonEmptyString(payload, "error"); message != "" {
		return BalanceResult{Status: "error", Provider: "sub2api-admin", Message: message}, true
	}
	for _, key := range []string{"needs_reauth", "is_banned", "is_forbidden"} {
		if value, ok := payload[key].(bool); ok && value {
			message := firstNonEmptyString(payload, "forbidden_reason", "message")
			if message == "" {
				message = "账号凭据无效或账号不可用"
			}
			return BalanceResult{Status: "invalid", Provider: "sub2api-admin", Message: message}, true
		}
	}

	credits := arrayValue(payload["ai_credits"])
	if len(credits) > 0 {
		var total float64
		for _, item := range credits {
			if amount := firstNumber(objectValue(item), "amount"); amount != nil {
				total += *amount
			}
		}
		return BalanceResult{Status: "ok", Provider: "sub2api-admin", Remaining: float64Pointer(total), Unit: "credits"}, true
	}

	windows := []struct {
		key  string
		name string
	}{
		{key: "five_hour", name: "5h"},
		{key: "seven_day", name: "7d"},
		{key: "seven_day_sonnet", name: "7d Sonnet"},
		{key: "seven_day_fable", name: "7d Fable"},
		{key: "gemini_shared_daily", name: "1d"},
		{key: "gemini_pro_daily", name: "Pro"},
		{key: "gemini_flash_daily", name: "Flash"},
	}
	for _, candidate := range windows {
		window := objectValue(payload[candidate.key])
		if window == nil {
			continue
		}
		usedRequests := firstNumber(window, "used_requests")
		limitRequests := firstNumber(window, "limit_requests")
		if usedRequests != nil && limitRequests != nil && *limitRequests >= 0 {
			remaining := math.Max(0, *limitRequests-*usedRequests)
			return BalanceResult{
				Status: "ok", Provider: "sub2api-admin", PlanName: candidate.name,
				Remaining: &remaining, Used: usedRequests, Total: limitRequests, Unit: "req",
				Message: utilizationMessage(firstNumber(window, "utilization")),
			}, true
		}
		if utilization := firstNumber(window, "utilization"); utilization != nil {
			used := math.Max(0, *utilization)
			remaining := math.Max(0, 100-used)
			total := 100.0
			return BalanceResult{
				Status: "ok", Provider: "sub2api-admin", PlanName: candidate.name,
				Remaining: &remaining, Used: &used, Total: &total, Unit: "%",
				Message: utilizationMessage(utilization),
			}, true
		}
	}
	return BalanceResult{}, false
}

func utilizationMessage(value *float64) string {
	if value == nil {
		return ""
	}
	return "使用率 " + strconv.FormatFloat(*value, 'f', -1, 64) + "%"
}

func float64Pointer(value float64) *float64 {
	return &value
}

func QueryUsageBalance(ctx context.Context, rawURL, apiKey string, client *http.Client) (BalanceResult, error) {
	baseURL, err := NormalizeBaseURL(rawURL)
	if err != nil {
		return BalanceResult{}, err
	}
	baseURL = trimURLSuffix(baseURL, "/v1/usage", "/v1")
	apiKey = strings.TrimSpace(apiKey)
	if err := validateHeaderValue("account API key", apiKey); err != nil {
		return BalanceResult{}, err
	}
	const endpoint = "/v1/usage"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+endpoint, nil)
	if err != nil {
		return BalanceResult{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := defaultHTTPClient(client).Do(request)
	if err != nil {
		return BalanceResult{}, fmt.Errorf("usage GET %s: %w", endpoint, err)
	}
	body, err := readResponse(response)
	if err != nil {
		return BalanceResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return BalanceResult{}, responseError("Usage", http.MethodGet, endpoint, response, body)
	}
	payload, err := decodeJSONObject(body)
	if err != nil {
		return BalanceResult{}, errors.New("usage endpoint returned invalid JSON")
	}
	if message, ok := payload["error"].(string); ok && strings.TrimSpace(message) != "" {
		return BalanceResult{}, errors.New(compactUntrustedText(message, maxErrorRunes))
	}
	result, ok := parseUsageBalance(payload)
	if !ok {
		return BalanceResult{Status: "unsupported", Provider: "usage", Message: "usage 响应中没有可解析的余额字段", Endpoint: endpoint}, nil
	}
	result.Endpoint = endpoint
	return result, nil
}

func parseUsageBalance(payload map[string]any) (BalanceResult, bool) {
	if active, ok := payload["is_active"].(bool); ok && !active {
		return BalanceResult{Status: "invalid", Provider: "usage", Message: firstString(payload, "message", "error")}, true
	}
	if valid, ok := payload["isValid"].(bool); ok && !valid {
		return BalanceResult{Status: "invalid", Provider: "usage", Message: firstString(payload, "message", "error")}, true
	}
	quota := objectValue(payload["quota"])
	remaining := firstNumber(payload, "remaining", "balance")
	if remaining == nil {
		remaining = firstNumber(quota, "remaining", "balance")
	}
	if remaining == nil {
		return BalanceResult{}, false
	}
	unit := firstNonEmptyString(payload, "unit")
	if unit == "" {
		unit = firstNonEmptyString(quota, "unit")
	}
	if unit == "" {
		unit = "USD"
	}
	return BalanceResult{Status: "ok", Provider: "usage", Remaining: remaining, Unit: unit}, true
}

func (c *NewAPIClient) Balance(ctx context.Context) (BalanceResult, error) {
	quotaPerUnit := float64(defaultNewAPIQuotaPerUnit)
	if status, err := c.request(ctx, "/api/status"); err == nil {
		if value := quotaPerUnitFromPayload(status); value != nil {
			quotaPerUnit = *value
		}
	}

	var requestErrors []error
	unsupported := make([]string, 0, 2)
	for _, endpoint := range []string{"/api/subscription/self", "/api/user/self"} {
		payload, err := c.request(ctx, endpoint)
		if err != nil {
			requestErrors = append(requestErrors, fmt.Errorf("%s: %w", endpoint, err))
			continue
		}
		if result, ok := parseNewAPIBalance(payload, quotaPerUnit); ok {
			result.Endpoint = endpoint
			return result, nil
		}
		unsupported = append(unsupported, endpoint)
	}
	if len(requestErrors) > 0 && len(unsupported) == 0 {
		return BalanceResult{}, errors.Join(requestErrors...)
	}
	detail := "NewAPI 余额响应中没有可解析字段"
	if len(unsupported) > 0 {
		detail = strings.Join(unsupported, " 和 ") + " 响应中没有可解析的余额字段"
	}
	return BalanceResult{Status: "unsupported", Provider: "newapi", Message: detail}, nil
}

func parseNewAPIBalance(payload map[string]any, quotaPerUnit float64) (BalanceResult, bool) {
	data := objectValue(payload["data"])
	if data == nil {
		data = payload
	}
	if value := quotaPerUnitFromPayload(payload); value != nil {
		quotaPerUnit = *value
	}
	if !finite(quotaPerUnit) || quotaPerUnit <= 0 {
		quotaPerUnit = defaultNewAPIQuotaPerUnit
	}

	remainingQuota := firstNumber(data, "quota", "remain_quota", "remaining_quota", "remainingQuota", "balance_quota", "balanceQuota", "total_available", "totalAvailable", "balance", "remaining")
	usedQuota := firstNumber(data, "used_quota", "usedQuota", "used")
	totalQuota := firstNumber(data, "total", "total_quota", "totalQuota", "total_granted", "totalGranted")
	if remainingQuota != nil || usedQuota != nil || totalQuota != nil {
		if remainingQuota == nil && totalQuota != nil && usedQuota != nil {
			value := math.Max(0, *totalQuota-*usedQuota)
			remainingQuota = &value
		}
		if totalQuota == nil && remainingQuota != nil && usedQuota != nil {
			value := *remainingQuota + *usedQuota
			totalQuota = &value
		}
		return BalanceResult{
			Status:    "ok",
			Provider:  "newapi",
			PlanName:  firstNonEmptyString(data, "group", "plan_name", "planName", "plan", "subscription_name", "subscriptionName"),
			Remaining: quotaToUnit(remainingQuota, quotaPerUnit),
			Used:      quotaToUnit(usedQuota, quotaPerUnit),
			Total:     quotaToUnit(totalQuota, quotaPerUnit),
			Unit:      "USD",
		}, true
	}

	rows := arrayValue(data["subscriptions"])
	if len(rows) == 0 {
		rows = arrayValue(data["all_subscriptions"])
	}
	if len(rows) == 0 {
		return BalanceResult{}, false
	}
	var remainingSum, usedSum, totalSum float64
	var hasRemaining, hasUsed, hasTotal bool
	planName := ""
	for _, row := range rows {
		record := objectValue(row)
		if record == nil {
			continue
		}
		subscription := objectValue(record["subscription"])
		if subscription == nil {
			subscription = record
		}
		plan := objectValue(record["plan"])
		if planName == "" {
			planName = firstNonEmptyString(plan, "title", "name")
			if planName == "" {
				planName = firstNonEmptyString(subscription, "title", "name", "plan_name", "planName")
			}
		}
		total := firstNumber(subscription, "amount_total", "total_amount", "totalAmount", "amountTotal", "total_quota", "totalQuota", "total_granted", "totalGranted")
		used := firstNumber(subscription, "amount_used", "used_amount", "amountUsed", "usedAmount", "used_quota", "usedQuota", "used")
		remaining := firstNumber(subscription, "amount_remaining", "remaining_amount", "amountRemaining", "remainingAmount", "remain_quota", "remaining_quota", "remainingQuota", "balance", "remaining")
		if remaining == nil && total != nil && used != nil && *total > 0 {
			value := math.Max(0, *total-*used)
			remaining = &value
		}
		if remaining != nil {
			remainingSum += *remaining
			hasRemaining = true
		}
		if used != nil {
			usedSum += *used
			hasUsed = true
		}
		if total != nil && *total > 0 {
			totalSum += *total
			hasTotal = true
		}
	}
	if !hasRemaining && !hasUsed && !hasTotal {
		return BalanceResult{}, false
	}
	return BalanceResult{
		Status:    "ok",
		Provider:  "newapi",
		PlanName:  planName,
		Remaining: optionalQuotaToUnit(remainingSum, hasRemaining, quotaPerUnit),
		Used:      optionalQuotaToUnit(usedSum, hasUsed, quotaPerUnit),
		Total:     optionalQuotaToUnit(totalSum, hasTotal, quotaPerUnit),
		Unit:      "USD",
	}, true
}

func quotaPerUnitFromPayload(payload map[string]any) *float64 {
	for _, scope := range []map[string]any{payload, objectValue(payload["data"]), objectValue(payload["status"]), objectValue(payload["meta"])} {
		if value := firstNumber(scope, "quota_per_unit"); value != nil && *value > 0 {
			return value
		}
	}
	return nil
}

func objectValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func arrayValue(value any) []any {
	result, _ := value.([]any)
	return result
}

func firstNumber(source map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if value, ok := number(source[key]); ok {
			copy := value
			return &copy
		}
	}
	return nil
}

func firstNonEmptyString(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func quotaToUnit(value *float64, quotaPerUnit float64) *float64 {
	if value == nil {
		return nil
	}
	converted := *value / quotaPerUnit
	return &converted
}

func optionalQuotaToUnit(value float64, available bool, quotaPerUnit float64) *float64 {
	if !available {
		return nil
	}
	converted := value / quotaPerUnit
	return &converted
}
