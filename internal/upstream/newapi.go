package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type NewAPIClient struct {
	baseURL       string
	authorization string
	cookie        string
	userID        string
	http          *http.Client
}

const newAPIBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

type NewAPIGroupRate struct {
	Group       string  `json:"group"`
	Description string  `json:"description,omitempty"`
	Rate        float64 `json:"rate"`
	Endpoint    string  `json:"endpoint"`
}

func NewNewAPIClient(rawURL, credential, userID string, client *http.Client) (*NewAPIClient, error) {
	baseURL, err := NormalizeBaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	baseURL = trimURLSuffix(baseURL, "/api/user/self/groups", "/api/user/self", "/api/pricing", "/api/v1", "/v1")
	authorization, cookie, resolvedUserID, err := normalizeNewAPICredential(credential, userID)
	if err != nil {
		return nil, err
	}
	return &NewAPIClient{
		baseURL:       baseURL,
		authorization: authorization,
		cookie:        cookie,
		userID:        resolvedUserID,
		http:          defaultHTTPClient(client),
	}, nil
}

func (c *NewAPIClient) headers(request *http.Request) {
	request.Header.Set("Accept", "application/json")
	if c.authorization != "" {
		request.Header.Set("Authorization", c.authorization)
	}
	if c.cookie != "" {
		request.Header.Set("Cookie", c.cookie)
		// Some NewAPI forks protect dashboard endpoints with a browser-only
		// check. Keep cookie-authenticated requests compatible without changing
		// the Bearer/PAT request contract.
		request.Header.Set("User-Agent", newAPIBrowserUserAgent)
		request.Header.Set("Referer", c.baseURL+"/keys")
	}
	if c.userID != "" {
		request.Header.Set("New-Api-User", c.userID)
	}
}

func (c *NewAPIClient) request(ctx context.Context, path string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.headers(request)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("NewAPI GET %s: %w", path, err)
	}
	body, readErr := readResponse(response)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError("NewAPI", http.MethodGet, path, response, body)
	}
	payload, err := decodeJSONObject(body)
	if err != nil {
		return nil, errors.New("NewAPI returned invalid JSON")
	}
	if success, ok := payload["success"].(bool); ok && !success {
		message := compactUntrustedText(firstString(payload, "message", "error"), maxErrorRunes)
		if message == "" {
			message = "request rejected"
		}
		return nil, fmt.Errorf("NewAPI rejected request: %s", message)
	}
	return payload, nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("invalid JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid trailing JSON data")
	}
	return payload, nil
}

func (c *NewAPIClient) ListGroupRates(ctx context.Context) ([]NewAPIGroupRate, error) {
	payload, err := c.request(ctx, "/api/user/self/groups")
	endpoint := "/api/user/self/groups"
	if err != nil {
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || !newAPIEndpointUnavailable(httpErr.Status) {
			return nil, err
		}
		// Authenticate before using a public pricing fallback.
		if _, authErr := c.request(ctx, "/api/user/self"); authErr != nil {
			return nil, authErr
		}
		payload, err = c.request(ctx, "/api/pricing")
		endpoint = "/api/pricing"
		if err != nil {
			return nil, err
		}
	}
	rates := parseNewAPIGroups(payload, endpoint)
	if len(rates) == 0 {
		return nil, fmt.Errorf("NewAPI %s returned no group ratios", endpoint)
	}
	sort.Slice(rates, func(i, j int) bool { return rates[i].Group < rates[j].Group })
	return rates, nil
}

func newAPIEndpointUnavailable(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusGone || status == http.StatusNotImplemented
}

// IsNewAPIAuthenticationError reports only errors that strongly indicate an
// expired or rejected NewAPI credential. Connectivity and response-shape
// failures deliberately remain ordinary synchronization errors.
func IsNewAPIAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if !strings.EqualFold(httpErr.System, "NewAPI") {
			return false
		}
		if httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden {
			return true
		}
		message += " " + strings.ToLower(httpErr.Detail)
	}
	for _, marker := range []string{
		"session expired", "session invalid", "invalid session",
		"token expired", "invalid token", "credential expired", "invalid credential", "credential is invalid",
		"credential and configured new-api-user do not match",
		"unauthorized", "authentication required", "login required", "not logged in",
		"会话已过期", "会话失效", "登录已过期", "登录失效", "未登录", "请先登录",
		"令牌已过期", "令牌无效", "凭据已过期", "凭据无效", "认证失败", "身份验证失败",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func parseNewAPIGroups(payload map[string]any, endpoint string) []NewAPIGroupRate {
	data := object(payload["data"])
	var candidate any
	if endpoint == "/api/pricing" {
		candidate = payload["group_ratio"]
		if candidate == nil {
			candidate = data["group_ratio"]
		}
	} else {
		for _, key := range []string{"groups", "group_ratio", "ratios"} {
			if value := data[key]; value != nil {
				candidate = value
				break
			}
		}
		if candidate == nil {
			for _, key := range []string{"groups", "group_ratio", "ratios"} {
				if value := payload[key]; value != nil {
					candidate = value
					break
				}
			}
		}
		if candidate == nil {
			if payload["data"] != nil {
				candidate = payload["data"]
			} else {
				candidate = payload
			}
		}
	}
	result := make([]NewAPIGroupRate, 0)
	seen := make(map[string]struct{})
	appendRate := func(name, description string, rate float64, ok bool) {
		name = strings.TrimSpace(name)
		if name == "" || !ok || rate < 0 || !finite(rate) {
			return
		}
		if _, duplicate := seen[name]; duplicate {
			return
		}
		seen[name] = struct{}{}
		result = append(result, NewAPIGroupRate{Group: name, Description: strings.TrimSpace(description), Rate: rate, Endpoint: endpoint})
	}
	switch value := candidate.(type) {
	case map[string]any:
		for name, raw := range value {
			item := object(raw)
			rate, ok := number(raw)
			if !ok {
				rate, ok = rateFromObject(item)
			}
			appendRate(name, firstString(item, "desc", "description"), rate, ok)
		}
	case []any:
		for _, raw := range value {
			item := object(raw)
			name := firstString(item, "group", "group_name", "name", "id", "key")
			rate, ok := rateFromObject(item)
			appendRate(name, firstString(item, "desc", "description"), rate, ok)
		}
	}
	return result
}

func (c *NewAPIClient) CurrentGroup(ctx context.Context) (string, error) {
	payload, err := c.request(ctx, "/api/user/self")
	if err != nil {
		return "", err
	}
	data := object(payload["data"])
	user := object(data["user"])
	for _, source := range []map[string]any{data, user, payload} {
		if value := firstString(source, "group", "group_name", "group_id"); value != "" {
			return value, nil
		}
	}
	return "", errors.New("NewAPI user response did not include a group")
}

func (c *NewAPIClient) ResolveRate(ctx context.Context, selectedGroup string) (NewAPIGroupRate, error) {
	rates, err := c.ListGroupRates(ctx)
	if err != nil {
		return NewAPIGroupRate{}, err
	}
	selectedGroup = strings.TrimSpace(selectedGroup)
	if selectedGroup == "" {
		if len(rates) == 1 {
			return rates[0], nil
		}
		selectedGroup, err = c.CurrentGroup(ctx)
		if err != nil {
			return NewAPIGroupRate{}, fmt.Errorf("select a NewAPI group when multiple rates are available: %w", err)
		}
	}
	for _, rate := range rates {
		if rate.Group == selectedGroup {
			return rate, nil
		}
	}
	return NewAPIGroupRate{}, fmt.Errorf("NewAPI group %q was not found", selectedGroup)
}

func object(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func firstString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			switch typed := value.(type) {
			case string:
				if text := strings.TrimSpace(typed); text != "" {
					return text
				}
			case json.Number:
				return typed.String()
			case float64:
				if finite(typed) {
					return strconv.FormatFloat(typed, 'g', -1, 64)
				}
			}
		}
	}
	return ""
}

func rateFromObject(item map[string]any) (float64, bool) {
	for _, key := range []string{"ratio", "rate", "rate_multiplier", "actual_rate_multiplier", "effective_rate_multiplier", "group_ratio", "multiplier"} {
		if rate, ok := number(item[key]); ok {
			return rate, true
		}
	}
	return 0, false
}

func number(value any) (float64, bool) {
	valid := func(value float64, err error) (float64, bool) {
		return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	switch numeric := value.(type) {
	case float64:
		return valid(numeric, nil)
	case float32:
		return valid(float64(numeric), nil)
	case int:
		return float64(numeric), true
	case int64:
		return float64(numeric), true
	case int32:
		return float64(numeric), true
	case uint:
		return float64(numeric), true
	case uint64:
		return float64(numeric), true
	case json.Number:
		parsed, err := numeric.Float64()
		return valid(parsed, err)
	case string:
		result, err := strconv.ParseFloat(strings.TrimSpace(numeric), 64)
		return valid(result, err)
	default:
		return 0, false
	}
}

func normalizeNewAPICredential(rawCredential, rawUserID string) (authorization, cookie, userID string, err error) {
	credential := strings.TrimSpace(rawCredential)
	userID = strings.TrimSpace(rawUserID)
	if credential == "" {
		return "", "", "", errors.New("NewAPI credential is required")
	}
	if embeddedCredential, embeddedUserID, ok := splitEmbeddedNewAPIUserID(credential); ok {
		credential = embeddedCredential
		if userID != "" && userID != embeddedUserID {
			return "", "", "", errors.New("NewAPI credential and configured New-Api-User do not match")
		}
		if userID == "" {
			userID = embeddedUserID
		}
	}
	if userID != "" {
		if err := validateHeaderValue("New-Api-User", userID); err != nil {
			return "", "", "", err
		}
	}

	lower := strings.ToLower(credential)
	switch {
	case strings.HasPrefix(lower, "authorization:"):
		credential = strings.TrimSpace(credential[len("authorization:"):])
		if strings.HasPrefix(strings.ToLower(credential), "bearer ") {
			authorization = "Bearer " + strings.TrimSpace(credential[len("bearer "):])
		} else {
			authorization = "Bearer " + credential
		}
	case strings.HasPrefix(lower, "bearer "):
		authorization = "Bearer " + strings.TrimSpace(credential[len("bearer "):])
	case strings.HasPrefix(lower, "session:"):
		cookie = strings.TrimSpace(credential[len("session:"):])
		if !strings.Contains(cookie, "=") {
			cookie = "session=" + cookie
		}
	case strings.HasPrefix(lower, "cookie:"):
		cookie = strings.TrimSpace(credential[len("cookie:"):])
	case looksLikeNewAPISessionCookie(credential):
		cookie = credential
	default:
		authorization = "Bearer " + credential
	}
	if authorization != "" {
		if err := validateHeaderValue("NewAPI authorization credential", authorization); err != nil {
			return "", "", "", err
		}
		if strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" {
			return "", "", "", errors.New("NewAPI credential is required")
		}
	}
	if cookie != "" {
		if err := validateHeaderValue("NewAPI cookie credential", cookie); err != nil {
			return "", "", "", err
		}
		if !validCookieHeader(cookie) {
			return "", "", "", errors.New("NewAPI cookie credential is invalid")
		}
	}
	if authorization == "" && cookie == "" {
		return "", "", "", errors.New("NewAPI credential is required")
	}
	return authorization, cookie, userID, nil
}

func splitEmbeddedNewAPIUserID(credential string) (string, string, bool) {
	index := strings.LastIndex(credential, "::")
	if index <= 0 || index+2 >= len(credential) {
		return credential, "", false
	}
	userID := strings.TrimSpace(credential[index+2:])
	if userID == "" {
		return credential, "", false
	}
	for _, r := range userID {
		if r < '0' || r > '9' {
			return credential, "", false
		}
	}
	return strings.TrimSpace(credential[:index]), userID, true
}

func looksLikeNewAPISessionCookie(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "session=") || (strings.Contains(value, ";") && strings.Contains(value, "="))
}

func validCookieHeader(value string) bool {
	hasValue := false
	for _, pair := range strings.Split(value, ";") {
		name, cookieValue, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || name == "" {
			return false
		}
		if cookieValue != "" {
			hasValue = true
		}
		for _, r := range name {
			if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
				return false
			}
		}
	}
	return hasValue
}
