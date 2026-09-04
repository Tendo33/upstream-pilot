package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var sourceProductMarkerKeys = []string{
	"source_type", "sourceType", "site_type", "siteType", "upstream_type", "upstreamType",
	"provider_type", "providerType", "provider",
}

var newAPIStatusKeys = map[string]struct{}{
	"system_name": {}, "quota_per_unit": {}, "server_address": {}, "setup": {},
	"turnstile_check": {}, "register_enabled": {}, "password_login_enabled": {},
	"logo": {}, "docs_link": {}, "version": {},
}

var nonProductCharacters = regexp.MustCompile(`[^a-z0-9]`)

func normalizedProduct(value string) string {
	return nonProductCharacters.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "")
}

func sourceProductMarker(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	for _, key := range sourceProductMarkerKeys {
		var value string
		if json.Unmarshal(object[key], &value) == nil {
			switch normalizedProduct(value) {
			case "newapi", "oneapi":
				return "newapi"
			case "sub2api", "s2api":
				return "sub2api"
			}
		}
	}
	return ""
}

func hasNativeSub2APIBillingSnapshot(raw json.RawMessage) bool {
	var extra struct {
		Probe struct {
			Data struct {
				Object string `json:"object"`
			} `json:"data"`
		} `json:"upstream_billing_probe"`
	}
	return json.Unmarshal(raw, &extra) == nil && extra.Probe.Data.Object == "sub2api.key_billing"
}

func inferSourceTypeHint(platform, accountType string, credentials, extra json.RawMessage) string {
	if hasNativeSub2APIBillingSnapshot(extra) {
		return "sub2api"
	}
	for _, value := range []string{platform, accountType} {
		switch normalizedProduct(value) {
		case "newapi", "oneapi":
			return "newapi"
		case "sub2api", "s2api":
			return "sub2api"
		}
	}
	if marker := sourceProductMarker(credentials); marker != "" {
		return marker
	}
	return sourceProductMarker(extra)
}

func IsNewAPISourceCandidate(account Sub2Account) bool {
	if account.SourceTypeHint != "" || account.ObservedSourceBaseURL == nil {
		return false
	}
	platform := normalizedProduct(account.Platform)
	accountType := normalizedProduct(account.Type)
	return platform == "openai" && (accountType == "apikey" || accountType == "upstream")
}

func NewAPIStatusURL(rawURL string) (string, error) {
	baseURL, err := NormalizeBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(baseURL)
	for _, suffix := range []string{
		"/api/user/self/groups", "/api/user/groups", "/api/user/self", "/api/pricing", "/api/status", "/api/v1", "/v1",
	} {
		if strings.HasSuffix(lower, suffix) {
			baseURL = strings.TrimRight(baseURL[:len(baseURL)-len(suffix)], "/")
			break
		}
	}
	return baseURL + "/api/status", nil
}

func ProbeNewAPISource(ctx context.Context, rawURL string, client *http.Client) bool {
	statusURL, err := NewAPIStatusURL(rawURL)
	if err != nil {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return false
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "UpstreamPilot/source-detection")
	response, err := defaultHTTPClient(client).Do(request)
	if err != nil {
		return false
	}
	body, err := readResponse(response)
	if err != nil || response.StatusCode != http.StatusOK {
		return false
	}
	if response.Header.Get("X-Oneapi-Request-Id") != "" {
		return true
	}
	var envelope struct {
		Success *bool          `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Success == nil || envelope.Data == nil {
		return false
	}
	signals := 0
	for key := range envelope.Data {
		if _, ok := newAPIStatusKeys[key]; ok {
			signals++
		}
	}
	return signals >= 2
}
