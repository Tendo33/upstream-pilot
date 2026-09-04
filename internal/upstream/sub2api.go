package upstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	listTotalUnknown          = -1
	listTotalDirect           = -2
	maxObservedSourceURLBytes = 2048
	accountExportBatchSize    = 100
	accountExportConcurrency  = 4
	accountExportTimeout      = 10 * time.Second
)

type Sub2Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type Sub2Group struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Platform       string   `json:"platform"`
	Status         any      `json:"status"`
	RateMultiplier *float64 `json:"rate_multiplier"`
}

type Sub2AccountGroup struct {
	GroupID  int64      `json:"group_id"`
	Priority *int       `json:"priority"`
	Group    *Sub2Group `json:"group"`
}

type Sub2Account struct {
	Native                                   NativeConstraints  `json:"-"`
	SourceMappingFingerprint                 string             `json:"-"`
	SourceMappingKnown                       bool               `json:"-"`
	LoadFactor                               *int               `json:"load_factor"`
	Concurrency                              *int               `json:"concurrency"`
	ID                                       int64              `json:"id"`
	Name                                     string             `json:"name"`
	Platform                                 string             `json:"platform"`
	Type                                     string             `json:"type"`
	Status                                   any                `json:"status"`
	Schedulable                              bool               `json:"schedulable"`
	Priority                                 int                `json:"priority"`
	RateMultiplier                           *float64           `json:"rate_multiplier"`
	UpdatedAt                                *time.Time         `json:"updated_at"`
	AccountGroups                            []Sub2AccountGroup `json:"account_groups"`
	SourceCredentialsPresent                 bool               `json:"-"`
	ObservedSourceBaseURLKnown               bool               `json:"-"`
	ObservedSourceBaseURL                    *string            `json:"-"`
	ObservedSourceCredentialFingerprintKnown bool               `json:"-"`
	ObservedSourceCredentialFingerprint      string             `json:"-"`
	SourceTypeHint                           string             `json:"-"`
}

func (a *Sub2Account) UnmarshalJSON(data []byte) error {
	type accountAlias Sub2Account
	var payload struct {
		*accountAlias
		Credentials       json.RawMessage `json:"credentials"`
		Extra             json.RawMessage `json:"extra"`
		CredentialsStatus map[string]bool `json:"credentials_status"`
	}
	payload.accountAlias = (*accountAlias)(a)
	a.SourceCredentialsPresent = false
	a.ObservedSourceBaseURLKnown = false
	a.ObservedSourceBaseURL = nil
	a.ObservedSourceCredentialFingerprintKnown = false
	a.ObservedSourceCredentialFingerprint = ""
	a.SourceTypeHint = ""
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	a.Native = parseNativeConstraints(data, *a)
	a.SourceMappingKnown = a.Native.MappingKnown
	if a.SourceMappingKnown {
		raw, _ := json.Marshal(struct {
			Mapping     map[string]string
			Passthrough bool
		}{a.Native.Mapping, a.Native.Passthrough})
		sum := sha256.Sum256(raw)
		a.SourceMappingFingerprint = hex.EncodeToString(sum[:])
	}
	a.SourceTypeHint = inferSourceTypeHint(a.Platform, a.Type, payload.Credentials, payload.Extra)
	if payload.Credentials == nil {
		return nil
	}
	a.SourceCredentialsPresent = true
	a.ObservedSourceBaseURLKnown, a.ObservedSourceBaseURL = observeSourceBaseURL(payload.Credentials)
	a.ObservedSourceCredentialFingerprintKnown, a.ObservedSourceCredentialFingerprint = observeSourceCredentialFingerprint(payload.Credentials)
	if payload.CredentialsStatus["has_api_key"] && a.ObservedSourceCredentialFingerprint == "" {
		a.ObservedSourceCredentialFingerprintKnown = false
	}
	return nil
}

func observeSourceCredentialFingerprint(credentials json.RawMessage) (bool, string) {
	trimmed := bytes.TrimSpace(credentials)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return false, ""
	}
	credential, ok := parseAccountUsageCredential(credentials)
	if !ok {
		return true, ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(credential.APIKey)))
	return true, hex.EncodeToString(sum[:])
}

func observeSourceBaseURL(credentials json.RawMessage) (bool, *string) {
	trimmed := bytes.TrimSpace(credentials)
	if len(trimmed) == 0 {
		return false, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true, nil
	}
	var payload struct {
		BaseURL      json.RawMessage `json:"base_url"`
		BaseURLCamel json.RawMessage `json:"baseUrl"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return false, nil
	}
	rawURL := payload.BaseURL
	if rawURL == nil {
		rawURL = payload.BaseURLCamel
	}
	if rawURL == nil || bytes.Equal(bytes.TrimSpace(rawURL), []byte("null")) {
		return true, nil
	}
	var value string
	if err := json.Unmarshal(rawURL, &value); err != nil {
		return false, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return true, nil
	}
	if len(value) > maxObservedSourceURLBytes {
		return false, nil
	}
	normalized, err := NormalizeBaseURL(value)
	if err != nil || len(normalized) > maxObservedSourceURLBytes {
		return false, nil
	}
	return true, &normalized
}

type AccountUpdate struct {
	Priority       *int     `json:"priority,omitempty"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
}

type ProbeResult struct {
	ControlPlaneError bool   `json:"control_plane_error"`
	FirstContentMS    *int   `json:"first_content_ms"`
	DurationMS        int    `json:"duration_ms"`
	ActualModel       string `json:"actual_model"`
	StreamComplete    bool   `json:"stream_complete"`
	Success           bool   `json:"success"`
	Message           string `json:"message"`
	LatencyMS         int    `json:"latency_ms"`
	Model             string `json:"model,omitempty"`
	HTTPStatus        *int   `json:"http_status,omitempty"`
	Code              string `json:"code,omitempty"`
	FailureData       string `json:"failure_data,omitempty"`
}

type AccountModel struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type BillingResult struct {
	Status                  string
	EffectiveRateMultiplier float64
	Endpoint                string
}

type AccountUsageStats struct {
	TotalRequests            int64 `json:"total_requests"`
	TotalInputTokens         int64 `json:"total_input_tokens"`
	TotalOutputTokens        int64 `json:"total_output_tokens"`
	TotalCacheCreationTokens int64 `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64 `json:"total_cache_read_tokens"`
}

func NewSub2Client(rawURL, apiKey string, client *http.Client) (*Sub2Client, error) {
	baseURL, err := NormalizeBaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	baseURL = trimURLSuffix(baseURL, "/api/v1/admin", "/api/v1")
	apiKey = strings.TrimSpace(apiKey)
	if err := validateHeaderValue("Sub2API admin API key", apiKey); err != nil {
		return nil, err
	}
	return &Sub2Client{baseURL: baseURL + "/api/v1/admin", apiKey: apiKey, http: defaultHTTPClient(client)}, nil
}

func (c *Sub2Client) request(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-api-key", c.apiKey)
	request.Header.Set("Accept", accept)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Sub2API %s %s: %w", method, path, err)
	}
	responseBody, readErr := readResponse(response)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError("Sub2API", method, path, response, responseBody)
	}
	return responseBody, nil
}

func unwrapJSON(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, errors.New("upstream returned invalid JSON")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("upstream returned null JSON")
	}
	if trimmed[0] != '{' {
		return trimmed, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, errors.New("upstream returned invalid JSON")
	}
	codeRaw, hasCode := object["code"]
	if !hasCode || string(bytes.TrimSpace(codeRaw)) == "null" {
		return trimmed, nil
	}
	var code int
	if err := json.Unmarshal(codeRaw, &code); err != nil {
		// Account-test events may use a string "code" field and are not API
		// envelopes. Only a numeric code identifies the Sub2API envelope.
		return trimmed, nil
	}
	message := ""
	_ = json.Unmarshal(object["message"], &message)
	if code != 0 {
		message = compactUntrustedText(message, maxErrorRunes)
		if message == "" {
			message = "request rejected"
		}
		return nil, fmt.Errorf("upstream rejected request (code %d): %s", code, message)
	}
	data, ok := object["data"]
	if !ok || len(data) == 0 || string(data) == "null" {
		return nil, errors.New("upstream success envelope did not include data")
	}
	return data, nil
}

func decodeList[T any](raw []byte) ([]T, int, error) {
	data, err := unwrapJSON(raw)
	if err != nil {
		return nil, 0, err
	}
	var direct []T
	if json.Unmarshal(data, &direct) == nil {
		return direct, listTotalDirect, nil
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, 0, errors.New("unexpected upstream list response")
	}
	itemsRaw, ok := page["items"]
	if !ok {
		nestedRaw, nested := page["data"]
		if nested {
			if json.Unmarshal(nestedRaw, &direct) == nil {
				return direct, listTotalDirect, nil
			}
			var nestedPage map[string]json.RawMessage
			if json.Unmarshal(nestedRaw, &nestedPage) == nil {
				itemsRaw, ok = nestedPage["items"]
				if _, exists := page["total"]; !exists {
					page["total"] = nestedPage["total"]
				}
			}
		}
	}
	if !ok {
		return nil, 0, errors.New("unexpected upstream list response")
	}
	var items []T
	if string(itemsRaw) == "null" {
		items = []T{}
	} else if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, 0, errors.New("unexpected upstream list items")
	}
	total := listTotalUnknown
	if totalRaw, exists := page["total"]; exists && len(totalRaw) > 0 && string(totalRaw) != "null" {
		if err := json.Unmarshal(totalRaw, &total); err != nil || total < 0 {
			return nil, 0, errors.New("unexpected upstream list total")
		}
	}
	return items, total, nil
}

func decodeObject[T any](raw []byte) (T, error) {
	var result T
	data, err := unwrapJSON(raw)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, errors.New("unexpected upstream object response")
	}
	return result, nil
}

func (c *Sub2Client) ListGroups(ctx context.Context) ([]Sub2Group, error) {
	raw, err := c.request(ctx, http.MethodGet, "/groups/all", nil, "application/json")
	if err != nil {
		return nil, err
	}
	items, _, err := decodeList[Sub2Group](raw)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID <= 0 {
			return nil, errors.New("Sub2API returned an invalid group ID")
		}
		if item.RateMultiplier != nil && (!finite(*item.RateMultiplier) || *item.RateMultiplier < 0) {
			return nil, errors.New("Sub2API returned an invalid group rate multiplier")
		}
	}
	return items, nil
}

func (c *Sub2Client) ListAccounts(ctx context.Context) ([]Sub2Account, error) {
	return c.listAccounts(ctx, true)
}
func (c *Sub2Client) ListAccountRuntime(ctx context.Context) ([]Sub2Account, error) {
	return c.listAccounts(ctx, false)
}
func (c *Sub2Client) listAccounts(ctx context.Context, hydrate bool) ([]Sub2Account, error) {
	const pageSize = 200
	all := make([]Sub2Account, 0, pageSize)
	seen := make(map[int64]struct{})
	for page := 1; page <= 10000; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}, "include_scheduler_score": {"false"}}
		raw, err := c.request(ctx, http.MethodGet, "/accounts?"+query.Encode(), nil, "application/json")
		if err != nil {
			return nil, err
		}
		items, total, err := decodeList[Sub2Account](raw)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.ID <= 0 {
				return nil, errors.New("Sub2API returned an invalid account ID")
			}
			if item.RateMultiplier != nil && (!finite(*item.RateMultiplier) || *item.RateMultiplier < 0) {
				return nil, fmt.Errorf("Sub2API returned an invalid rate multiplier for account %d", item.ID)
			}
			if _, duplicate := seen[item.ID]; duplicate {
				return nil, fmt.Errorf("Sub2API account pagination repeated account ID %d", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
		all = append(all, items...)
		if total == listTotalDirect {
			if hydrate {
				return c.fillMissingAccountSourceURLs(ctx, all), nil
			}
			return all, nil
		}
		if total >= 0 && len(all) >= total {
			if hydrate {
				return c.fillMissingAccountSourceURLs(ctx, all), nil
			}
			return all, nil
		}
		if len(items) == 0 {
			if total > len(all) {
				return nil, fmt.Errorf("Sub2API account pagination stalled after %d of %d accounts", len(all), total)
			}
			if hydrate {
				return c.fillMissingAccountSourceURLs(ctx, all), nil
			}
			return all, nil
		}
		if total == listTotalUnknown && len(items) < pageSize {
			if hydrate {
				return c.fillMissingAccountSourceURLs(ctx, all), nil
			}
			return all, nil
		}
	}
	return nil, errors.New("Sub2API account pagination exceeded 10000 pages")
}

func (c *Sub2Client) fillMissingAccountSourceURLs(ctx context.Context, accounts []Sub2Account) []Sub2Account {
	indexes := make(map[int64]int, len(accounts))
	missing := make([]int64, 0)
	for index := range accounts {
		indexes[accounts[index].ID] = index
		if !accounts[index].SourceCredentialsPresent || !accounts[index].ObservedSourceCredentialFingerprintKnown {
			missing = append(missing, accounts[index].ID)
		}
	}
	if len(missing) == 0 {
		return accounts
	}
	apply := func(observations map[int64]sourceURLObservation) {
		for accountID, observation := range observations {
			index, ok := indexes[accountID]
			if !ok {
				continue
			}
			if observation.URLKnown {
				accounts[index].SourceCredentialsPresent = true
				accounts[index].ObservedSourceBaseURLKnown = true
				accounts[index].ObservedSourceBaseURL = observation.URL
			}
			if observation.CredentialFingerprintKnown {
				accounts[index].SourceCredentialsPresent = true
				accounts[index].ObservedSourceCredentialFingerprintKnown = true
				accounts[index].ObservedSourceCredentialFingerprint = observation.CredentialFingerprint
			}
		}
	}
	exportBatch := func(batch []int64) (map[int64]sourceURLObservation, error) {
		batchCtx, cancel := context.WithTimeout(ctx, accountExportTimeout)
		defer cancel()
		return c.exportAccountSourceURLs(batchCtx, batch)
	}

	firstEnd := min(accountExportBatchSize, len(missing))
	first, err := exportBatch(missing[:firstEnd])
	if err == nil {
		apply(first)
	} else if accountExportUnavailable(err) {
		return accounts
	}
	if firstEnd == len(missing) || ctx.Err() != nil {
		return accounts
	}

	type exportBatchRange struct{ start, end int }
	batchCount := (len(missing) - firstEnd + accountExportBatchSize - 1) / accountExportBatchSize
	jobs := make(chan exportBatchRange, batchCount)
	results := make(chan map[int64]sourceURLObservation, batchCount)
	for start := firstEnd; start < len(missing); start += accountExportBatchSize {
		jobs <- exportBatchRange{start: start, end: min(start+accountExportBatchSize, len(missing))}
	}
	close(jobs)

	workerCount := min(accountExportConcurrency, batchCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for batch := range jobs {
				observations, batchErr := exportBatch(missing[batch.start:batch.end])
				if batchErr == nil {
					results <- observations
				}
			}
		}()
	}
	workers.Wait()
	close(results)
	for observations := range results {
		apply(observations)
	}
	return accounts
}

func accountExportUnavailable(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && (httpErr.Status == http.StatusNotFound || httpErr.Status == http.StatusMethodNotAllowed || httpErr.Status == http.StatusGone || httpErr.Status == http.StatusNotImplemented)
}

type sourceURLObservation struct {
	URLKnown                   bool
	URL                        *string
	CredentialFingerprintKnown bool
	CredentialFingerprint      string
}

type exportedAccountObservation struct {
	ID          int64
	Credentials json.RawMessage
}

func (a *exportedAccountObservation) UnmarshalJSON(data []byte) error {
	var payload struct {
		ID          json.RawMessage `json:"id"`
		AccountID   json.RawMessage `json:"account_id"`
		AccountIDV2 json.RawMessage `json:"accountId"`
		Credentials json.RawMessage `json:"credentials"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	rawID := payload.ID
	if rawID == nil {
		rawID = payload.AccountID
	}
	if rawID == nil {
		rawID = payload.AccountIDV2
	}
	a.ID, _ = decodePositiveInt64(rawID)
	a.Credentials = payload.Credentials
	return nil
}

func decodePositiveInt64(raw json.RawMessage) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		return value, err == nil && value > 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return value, err == nil && value > 0
}

func (c *Sub2Client) exportAccountSourceURLs(ctx context.Context, accountIDs []int64) (map[int64]sourceURLObservation, error) {
	if len(accountIDs) == 0 {
		return map[int64]sourceURLObservation{}, nil
	}
	requested := make(map[int64]struct{}, len(accountIDs))
	ids := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, duplicate := requested[accountID]; duplicate {
			continue
		}
		requested[accountID] = struct{}{}
		ids = append(ids, strconv.FormatInt(accountID, 10))
	}
	if len(ids) == 0 {
		return map[int64]sourceURLObservation{}, nil
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
	if len(requested) == 1 && len(items) == 1 && items[0].ID == 0 {
		for accountID := range requested {
			items[0].ID = accountID
		}
	}
	result := make(map[int64]sourceURLObservation, len(items))
	for _, item := range items {
		if _, ok := requested[item.ID]; !ok || item.Credentials == nil {
			continue
		}
		urlKnown, observedURL := observeSourceBaseURL(item.Credentials)
		fingerprintKnown, fingerprint := observeSourceCredentialFingerprint(item.Credentials)
		if urlKnown || fingerprintKnown {
			result[item.ID] = sourceURLObservation{
				URLKnown: urlKnown, URL: observedURL,
				CredentialFingerprintKnown: fingerprintKnown, CredentialFingerprint: fingerprint,
			}
		}
	}
	if len(requested) > 1 && len(result) < len(requested) {
		missing := make(chan int64, len(requested)-len(result))
		for accountID := range requested {
			if _, ok := result[accountID]; !ok {
				missing <- accountID
			}
		}
		close(missing)

		singles := make(chan map[int64]sourceURLObservation, cap(missing))
		var workers sync.WaitGroup
		workerCount := min(accountExportConcurrency, cap(missing))
		workers.Add(workerCount)
		for range workerCount {
			go func() {
				defer workers.Done()
				for accountID := range missing {
					observation, singleErr := c.exportAccountSourceURLs(ctx, []int64{accountID})
					if singleErr == nil {
						singles <- observation
					}
				}
			}()
		}
		workers.Wait()
		close(singles)
		for observations := range singles {
			for accountID, observation := range observations {
				result[accountID] = observation
			}
		}
	}
	return result, nil
}

func decodeExportedAccountObservations(raw []byte) ([]exportedAccountObservation, error) {
	data, err := unwrapJSON(raw)
	if err != nil {
		return nil, err
	}
	return findExportedAccountArray(data, 0)
}

func findExportedAccountArray(raw json.RawMessage, depth int) ([]exportedAccountObservation, error) {
	if depth > 3 {
		return nil, errors.New("unexpected Sub2API account export response")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("unexpected Sub2API account export response")
	}
	if trimmed[0] == '[' {
		var accounts []exportedAccountObservation
		if err := json.Unmarshal(trimmed, &accounts); err != nil {
			return nil, errors.New("unexpected Sub2API account export response")
		}
		return accounts, nil
	}
	if trimmed[0] != '{' {
		return nil, errors.New("unexpected Sub2API account export response")
	}
	var payload struct {
		Accounts json.RawMessage `json:"accounts"`
		Items    json.RawMessage `json:"items"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, errors.New("unexpected Sub2API account export response")
	}
	for _, candidate := range []json.RawMessage{payload.Accounts, payload.Items, payload.Data} {
		if candidate != nil && !bytes.Equal(bytes.TrimSpace(candidate), []byte("null")) {
			return findExportedAccountArray(candidate, depth+1)
		}
	}
	return nil, errors.New("unexpected Sub2API account export response")
}

func (c *Sub2Client) GetAccount(ctx context.Context, accountID int64) (Sub2Account, error) {
	if accountID <= 0 {
		return Sub2Account{}, errors.New("Sub2API account ID must be positive")
	}
	raw, err := c.request(ctx, http.MethodGet, "/accounts/"+strconv.FormatInt(accountID, 10), nil, "application/json")
	if err != nil {
		return Sub2Account{}, err
	}
	account, err := decodeObject[Sub2Account](raw)
	if err != nil {
		return Sub2Account{}, err
	}
	return validateSub2Account(account, accountID)
}

func (c *Sub2Client) ListAccountModels(ctx context.Context, accountID int64) ([]AccountModel, error) {
	path := "/accounts/" + strconv.FormatInt(accountID, 10) + "/models"
	raw, err := c.request(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return nil, err
	}
	data, err := unwrapJSON(raw)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("invalid Sub2API model response")
	}
	if object, ok := value.(map[string]any); ok {
		if items, exists := object["items"]; exists {
			value = items
		} else if nested, exists := object["data"]; exists {
			value = nested
		}
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("unexpected Sub2API model response")
	}
	models := make([]AccountModel, 0, len(items))
	seen := make(map[string]struct{})
	for _, rawItem := range items {
		model := AccountModel{}
		switch item := rawItem.(type) {
		case string:
			model.ID = strings.TrimSpace(item)
		case map[string]any:
			model.ID = strings.TrimSpace(fmt.Sprint(item["id"]))
			if item["id"] == nil {
				model.ID = ""
			}
			if text, ok := item["type"].(string); ok {
				model.Type = strings.TrimSpace(text)
			}
			if text, ok := item["display_name"].(string); ok {
				model.DisplayName = strings.TrimSpace(text)
			}
		}
		if model.ID == "" {
			continue
		}
		if _, duplicate := seen[model.ID]; duplicate {
			continue
		}
		seen[model.ID] = struct{}{}
		models = append(models, model)
	}
	return models, nil
}

func (c *Sub2Client) UpdateAccount(ctx context.Context, accountID int64, update AccountUpdate) (Sub2Account, error) {
	if accountID <= 0 {
		return Sub2Account{}, errors.New("Sub2API account ID must be positive")
	}
	if update.Priority == nil && update.RateMultiplier == nil {
		return Sub2Account{}, errors.New("Sub2API account update is empty")
	}
	if update.RateMultiplier != nil && (!finite(*update.RateMultiplier) || *update.RateMultiplier < 0) {
		return Sub2Account{}, errors.New("Sub2API account rate multiplier must be finite and non-negative")
	}
	path := "/accounts/" + strconv.FormatInt(accountID, 10)
	raw, err := c.request(ctx, http.MethodPut, path, update, "application/json")
	if err != nil {
		return Sub2Account{}, err
	}
	account, err := decodeObject[Sub2Account](raw)
	if err != nil {
		return Sub2Account{}, err
	}
	return validateSub2Account(account, accountID)
}

func (c *Sub2Client) SetSchedulable(ctx context.Context, accountID int64, enabled bool) (Sub2Account, error) {
	if accountID <= 0 {
		return Sub2Account{}, errors.New("Sub2API account ID must be positive")
	}
	path := "/accounts/" + strconv.FormatInt(accountID, 10) + "/schedulable"
	raw, err := c.request(ctx, http.MethodPost, path, map[string]bool{"schedulable": enabled}, "application/json")
	if err != nil {
		return Sub2Account{}, err
	}
	account, err := decodeObject[Sub2Account](raw)
	if err != nil {
		return Sub2Account{}, err
	}
	return validateSub2Account(account, accountID)
}

func (c *Sub2Client) TestAccount(ctx context.Context, accountID int64, model string) (ProbeResult, error) {
	return c.streamAccountTest(ctx, accountID, strings.TrimSpace(model))
}

func (c *Sub2Client) ProbeBilling(ctx context.Context, accountID int64) (BillingResult, error) {
	if accountID <= 0 {
		return BillingResult{}, errors.New("Sub2API account ID must be positive")
	}
	path := "/accounts/" + strconv.FormatInt(accountID, 10) + "/upstream-billing-probe"
	raw, err := c.request(ctx, http.MethodPost, path, nil, "application/json")
	if err != nil {
		return BillingResult{}, err
	}
	data, err := unwrapJSON(raw)
	if err != nil {
		return BillingResult{}, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return BillingResult{}, errors.New("invalid Sub2API billing probe response")
	}
	if accountIDRaw, ok := payload["account_id"]; ok {
		var responseAccountID int64
		if err := json.Unmarshal(accountIDRaw, &responseAccountID); err != nil || responseAccountID != accountID {
			return BillingResult{}, errors.New("Sub2API billing probe returned a mismatched account ID")
		}
	}
	snapshot := data
	if snapshotRaw, ok := payload["snapshot"]; ok && len(snapshotRaw) > 0 && string(snapshotRaw) != "null" {
		snapshot = snapshotRaw
	}
	var decodedSnapshot struct {
		Status string                     `json:"status"`
		Data   map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(snapshot, &decodedSnapshot); err != nil {
		return BillingResult{}, errors.New("invalid Sub2API billing probe snapshot")
	}
	status := strings.ToLower(strings.TrimSpace(decodedSnapshot.Status))
	if status != "ok" {
		return BillingResult{}, fmt.Errorf("Sub2API billing probe status is %q", status)
	}
	rateRaw, ok := decodedSnapshot.Data["effective_rate_multiplier"]
	if !ok {
		return BillingResult{}, errors.New("Sub2API billing probe did not include effective_rate_multiplier")
	}
	rate, ok := decodeFiniteNumber(rateRaw)
	if !ok || rate < 0 {
		return BillingResult{}, errors.New("Sub2API billing probe returned an invalid effective_rate_multiplier")
	}
	return BillingResult{Status: status, EffectiveRateMultiplier: rate, Endpoint: path}, nil
}

func (c *Sub2Client) AccountUsageStats(ctx context.Context, accountID int64) (AccountUsageStats, error) {
	if accountID <= 0 {
		return AccountUsageStats{}, errors.New("Sub2API account ID must be positive")
	}
	query := url.Values{}
	query.Set("account_id", strconv.FormatInt(accountID, 10))
	query.Set("period", "today")
	query.Set("nocache", "true")
	raw, err := c.request(ctx, http.MethodGet, "/usage/stats?"+query.Encode(), nil, "application/json")
	if err != nil {
		return AccountUsageStats{}, err
	}
	stats, err := decodeObject[AccountUsageStats](raw)
	if err != nil {
		return AccountUsageStats{}, err
	}
	stats.TotalRequests = max(stats.TotalRequests, 0)
	stats.TotalInputTokens = max(stats.TotalInputTokens, 0)
	stats.TotalOutputTokens = max(stats.TotalOutputTokens, 0)
	stats.TotalCacheCreationTokens = max(stats.TotalCacheCreationTokens, 0)
	stats.TotalCacheReadTokens = max(stats.TotalCacheReadTokens, 0)
	return stats, nil
}

func (c *Sub2Client) Version(ctx context.Context) string {
	raw, err := c.request(ctx, http.MethodGet, "/system/version", nil, "application/json")
	if err != nil {
		return ""
	}
	data, err := unwrapJSON(raw)
	if err != nil {
		return ""
	}
	var direct string
	if json.Unmarshal(data, &direct) == nil {
		return direct
	}
	var object struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(data, &object)
	return object.Version
}

func decodeFiniteNumber(raw json.RawMessage) (float64, bool) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, err := number.Float64()
		return value, err == nil && finite(value)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return value, err == nil && finite(value)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateSub2Account(account Sub2Account, expectedID int64) (Sub2Account, error) {
	if account.ID <= 0 || account.ID != expectedID {
		return Sub2Account{}, errors.New("Sub2API returned a mismatched account ID")
	}
	if account.RateMultiplier != nil && (!finite(*account.RateMultiplier) || *account.RateMultiplier < 0) {
		return Sub2Account{}, errors.New("Sub2API returned an invalid account rate multiplier")
	}
	return account, nil
}

// UpdateCapacity only accepts scheduling-owned fields; billing fields cannot pass.
func (c *Sub2Client) UpdateCapacity(ctx context.Context, id int64, fields map[string]any) (Sub2Account, error) {
	if id <= 0 || len(fields) == 0 {
		return Sub2Account{}, errors.New("empty control update")
	}
	for k, v := range fields {
		if k != "priority" && k != "load_factor" && k != "concurrency" {
			return Sub2Account{}, errors.New("unsupported control field")
		}
		if v == nil && k == "load_factor" {
			continue
		}
		n, ok := v.(float64)
		if !ok {
			if i, yes := v.(int); yes {
				n = float64(i)
				ok = true
			}
		}
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > 1000000 || ((k == "concurrency" || k == "load_factor") && n < 1) {
			return Sub2Account{}, errors.New("invalid control value")
		}
	}
	raw, err := c.request(ctx, http.MethodPut, fmt.Sprintf("/accounts/%d", id), fields, "application/json")
	if err != nil {
		return Sub2Account{}, err
	}
	account, err := decodeObject[Sub2Account](raw)
	if err != nil {
		return Sub2Account{}, err
	}
	return validateSub2Account(account, id)
}
