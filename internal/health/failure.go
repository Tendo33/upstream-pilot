package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FailureReason is the stable, API-facing classification for an account probe.
type FailureReason string

const (
	FailureAuth          FailureReason = "AUTH"
	FailureBalance       FailureReason = "BALANCE"
	FailureRateLimit     FailureReason = "RATE_LIMIT"
	FailureUpstream      FailureReason = "UPSTREAM"
	FailureTimeout       FailureReason = "TIMEOUT"
	FailureConfiguration FailureReason = "CONFIGURATION"
	FailureUnknown       FailureReason = "UNKNOWN"
)

// Classification contains the reason and the best HTTP status found in the
// upstream evidence. HTTPStatus is nil when the failure happened before an
// HTTP response was available (for example, a timeout or DNS error).
type Classification struct {
	Reason     FailureReason
	HTTPStatus *int
}

type statusCoder interface {
	StatusCode() int
}

type failureDetailer interface {
	FailureDetail() string
}

type timeoutError interface {
	Timeout() bool
}

var statusPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bHTTP\s*[:=]?\s*([1-5]\d{2})\b`),
	regexp.MustCompile(`(?i)\bAPI\s+returned\s+([1-5]\d{2})\b`),
	regexp.MustCompile(`(?i)\b(?:status(?:code)?|httpstatus|code)\s*["'\s]*[:=]\s*["']?([1-5]\d{2})\b`),
}

// ClassifyFailure accepts an error, a ProbeResult-shaped map, or any nested
// JSON-compatible value. It intentionally inspects semantic error text before
// HTTP status so quota exhaustion reported as 403/429 remains BALANCE.
func ClassifyFailure(value any) Classification {
	text, status := collect(value, 0)
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	if status == nil {
		status = statusFromText(text)
	}

	if (status != nil && *status == 402) || containsAny(text,
		"insufficient balance", "insufficient account balance", "insufficient_balance", "balance insufficient",
		"insufficient quota", "insufficient_user_quota", "pre_consume_token_quota_failed", "quota exhausted",
		"user quota is not enough", "token quota is not enough", "out of credits", "credit balance",
		"余额不足", "额度不足", "预扣费额度失败", "配额耗尽", "用户额度不足", "订阅额度不足", "欠费",
	) {
		return Classification{Reason: FailureBalance, HTTPStatus: status}
	}

	if (status != nil && (*status == 401 || *status == 403)) || containsAny(text,
		"unauthorized", "forbidden", "authentication failed", "invalid api key", "invalid token",
		"token expired", "invalid credential", "expired credential", "鉴权", "认证失败", "凭证无效", "凭证过期",
	) {
		return Classification{Reason: FailureAuth, HTTPStatus: status}
	}

	if (status != nil && *status == 429) || containsAny(text,
		"rate limit", "too many requests", "model cooldown", "cooling down", "throttl", "限流", "请求过多", "冷却中",
	) {
		return Classification{Reason: FailureRateLimit, HTTPStatus: status}
	}

	if (status != nil && (*status == 408 || *status == 504)) || containsAny(text,
		"timeout", "timed out", "etimedout", "aborterror", "request aborted", "deadline exceeded", "超时",
	) {
		return Classification{Reason: FailureTimeout, HTTPStatus: status}
	}

	if (status != nil && (*status == 400 || *status == 404 || *status == 405 || *status == 422)) || containsAny(text,
		"model not found", "model_not_found", "unsupported model", "invalid request", "configuration", "misconfigured",
		"配置错误", "不支持的模型", "模型不存在", "无效请求",
	) {
		return Classification{Reason: FailureConfiguration, HTTPStatus: status}
	}

	if (status != nil && *status >= 500) || containsAny(text,
		"upstream error", "upstream service", "bad gateway", "service unavailable", "gateway error",
		"econnreset", "econnrefused", "socket hang up", "上游异常", "服务不可用", "网关错误",
	) {
		return Classification{Reason: FailureUpstream, HTTPStatus: status}
	}

	return Classification{Reason: FailureUnknown, HTTPStatus: status}
}

func containsAny(text string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(text, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func statusFromText(text string) *int {
	for _, pattern := range statusPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) == 2 {
			value, err := strconv.Atoi(match[1])
			if err == nil {
				return validStatus(value)
			}
		}
	}
	return nil
}

func validStatus(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	return &value
}

// collect flattens only bounded, untrusted evidence. The depth limit prevents
// recursive error/cause graphs from making a probe response unexpectedly huge.
func collect(value any, depth int) (string, *int) {
	if value == nil || depth > 5 {
		return "", nil
	}
	if err, ok := value.(error); ok {
		parts := []string{err.Error()}
		var status *int
		if typed, ok := err.(statusCoder); ok {
			status = validStatus(typed.StatusCode())
		}
		if detailer, ok := err.(failureDetailer); ok {
			parts = append(parts, detailer.FailureDetail())
		}
		if timeout, ok := err.(timeoutError); ok && timeout.Timeout() {
			parts = append(parts, "timeout")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			parts = append(parts, "deadline exceeded timeout")
		}
		if errors.Is(err, context.Canceled) {
			parts = append(parts, "request aborted")
		}
		if cause := errors.Unwrap(err); cause != nil {
			causeText, causeStatus := collect(cause, depth+1)
			parts = append(parts, causeText)
			if status == nil {
				status = causeStatus
			}
		}
		return strings.Join(parts, " "), status
	}
	if value, ok := value.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return collect(decoded, depth+1)
		}
		return string(value), nil
	}
	if value, ok := value.([]byte); ok {
		return collect(json.RawMessage(value), depth+1)
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case fmt.Stringer:
		return typed.String(), nil
	case map[string]any:
		return collectMap(typed, depth)
	case []any:
		var parts []string
		var status *int
		for _, item := range typed {
			itemText, itemStatus := collect(item, depth+1)
			parts = append(parts, itemText)
			if status == nil {
				status = itemStatus
			}
		}
		return strings.Join(parts, " "), status
	}

	// Structs and typed maps are uncommon here, but accepting them keeps the
	// classifier useful in tests and for future upstream client types.
	rv := reflect.ValueOf(value)
	if rv.IsValid() && (rv.Kind() == reflect.Struct || rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Pointer) {
		encoded, err := json.Marshal(value)
		if err == nil {
			return collect(json.RawMessage(encoded), depth+1)
		}
	}
	return fmt.Sprint(value), nil
}

func collectMap(value map[string]any, depth int) (string, *int) {
	var parts []string
	var status *int
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, wanted := range []string{"httpstatus", "http_status", "statuscode", "status_code", "status", "code"} {
		for _, key := range keys {
			if strings.ToLower(strings.TrimSpace(key)) == wanted {
				if candidate := statusValue(value[key]); candidate != nil {
					status = candidate
					break
				}
			}
		}
		if status != nil {
			break
		}
	}
	for _, key := range keys {
		item := value[key]
		itemText, itemStatus := collect(item, depth+1)
		if itemText != "" {
			parts = append(parts, itemText)
		}
		if status == nil {
			status = itemStatus
		}
	}
	return strings.Join(parts, " "), status
}

func statusValue(value any) *int {
	switch typed := value.(type) {
	case *int:
		if typed != nil {
			return validStatus(*typed)
		}
	case *int64:
		if typed != nil {
			return validStatus(int(*typed))
		}
	case float64:
		if typed == float64(int(typed)) {
			return validStatus(int(typed))
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return validStatus(int(parsed))
		}
	case int:
		return validStatus(typed)
	case int8:
		return validStatus(int(typed))
	case int16:
		return validStatus(int(typed))
	case int32:
		return validStatus(int(typed))
	case int64:
		return validStatus(int(typed))
	case uint:
		if typed <= 599 {
			return validStatus(int(typed))
		}
	case uint8:
		return validStatus(int(typed))
	case uint16:
		return validStatus(int(typed))
	case uint32:
		if typed <= 599 {
			return validStatus(int(typed))
		}
	case uint64:
		if typed <= 599 {
			return validStatus(int(typed))
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return validStatus(parsed)
		}
	}
	return nil
}
