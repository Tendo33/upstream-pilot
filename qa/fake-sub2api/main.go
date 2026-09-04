package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type account struct {
	Concurrency    int        `json:"concurrency"`
	LoadFactor     int        `json:"load_factor"`
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Platform       string     `json:"platform"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	Schedulable    bool       `json:"schedulable"`
	Priority       int        `json:"priority"`
	RateMultiplier float64    `json:"rate_multiplier"`
	AccountGroups  []groupRef `json:"account_groups"`
}

type groupRef struct {
	GroupID  int64 `json:"group_id"`
	Priority int   `json:"priority"`
}

type usageSnapshot struct {
	Requests      int64 `json:"requests"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_tokens"`
	CacheRead     int64 `json:"cache_read_tokens"`
}

type state struct {
	mu                    sync.Mutex
	accounts              map[int64]*account
	probeSuccess          map[int64]bool
	probeFailures         map[int64]map[string]any
	probeDelayMS          map[int64]int
	probeRequests         map[int64]int
	usageRequests         map[int64]int
	usageStats            map[int64]usageSnapshot
	schedulingFailures    map[int64]bool
	billingRate           map[int64]float64
	groupRate             float64
	groupUpdates          int
	exportAvailable       bool
	exportCredentialsNull map[int64]bool
	exportRequests        int
	newAPIAuthValid       bool
}

func main() {
	listenAddr := strings.TrimSpace(os.Getenv("PILOT_FAKE_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = "127.0.0.1:33888"
	}
	if err := validateListenAddr(listenAddr); err != nil {
		log.Fatal(err)
	}

	s := newState()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/control/reset", s.resetControl)
	mux.HandleFunc("/control/stats", s.controlStats)
	mux.HandleFunc("/control/", s.control)
	mux.HandleFunc("/newapi/api/status", s.newAPIStatus)
	mux.HandleFunc("/newapi/api/user/self/groups", s.newAPIGroups)
	mux.HandleFunc("/newapi/api/user/self", s.newAPISelf)
	mux.HandleFunc("/newapi/api/pricing", s.newAPIPricing)
	mux.HandleFunc("/newapi-direct/api/user/self/groups", s.newAPIDirectGroups)
	mux.HandleFunc("/api/v1/admin/", s.admin)
	mux.HandleFunc("/v1/chat/completions", s.forward)
	log.Printf("fake Sub2API listening on %s", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, mux))
}

func newState() *state {
	return &state{
		accounts: map[int64]*account{
			101: {Concurrency: 16, LoadFactor: 16, ID: 101, Name: "Claude primary", Platform: "anthropic", Type: "apikey", Status: "active", Schedulable: true, Priority: 20, RateMultiplier: 1, AccountGroups: []groupRef{{GroupID: 1, Priority: 20}}},
			102: {Concurrency: 16, LoadFactor: 16, ID: 102, Name: "OpenAI standby", Platform: " OpenAI ", Type: "apikey", Status: "active", Schedulable: true, Priority: 20, RateMultiplier: 1, AccountGroups: []groupRef{{GroupID: 1, Priority: 20}}},
		},
		probeSuccess:          map[int64]bool{101: true, 102: true},
		probeFailures:         make(map[int64]map[string]any),
		probeDelayMS:          make(map[int64]int),
		probeRequests:         make(map[int64]int),
		usageRequests:         make(map[int64]int),
		usageStats:            make(map[int64]usageSnapshot),
		schedulingFailures:    make(map[int64]bool),
		billingRate:           map[int64]float64{101: 0.75, 102: 1.25},
		groupRate:             1,
		exportAvailable:       true,
		exportCredentialsNull: make(map[int64]bool),
		newAPIAuthValid:       true,
	}
}

func validateListenAddr(value string) error {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("invalid PILOT_FAKE_LISTEN_ADDR: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("PILOT_FAKE_LISTEN_ADDR must use a loopback address")
	}
	return nil
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *state) admin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != "test-admin-key" {
		write(w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "invalid key"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin")
	if strings.HasPrefix(path, "/accounts/") && strings.HasSuffix(path, "/test") && r.Method == http.MethodPost {
		s.accountProbe(w, r, path)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case path == "/groups/all" && r.Method == http.MethodGet:
		write(w, http.StatusOK, map[string]any{"code": 0, "data": []map[string]any{{"id": 1, "name": "default", "platform": "all", "status": "active", "rate_multiplier": s.groupRate}}})
	case path == "/groups/1" && r.Method == http.MethodGet:
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"id": 1, "name": "default", "platform": "all", "status": "active", "rate_multiplier": s.groupRate}})
	case path == "/groups/1" && r.Method == http.MethodPut:
		var update struct {
			RateMultiplier *float64 `json:"rate_multiplier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil || update.RateMultiplier == nil || *update.RateMultiplier <= 0 {
			write(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "invalid group rate"})
			return
		}
		s.groupRate = *update.RateMultiplier
		s.groupUpdates++
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"id": 1, "name": "default", "platform": "all", "status": "active", "rate_multiplier": s.groupRate}})
	case path == "/accounts" && r.Method == http.MethodGet:
		items := make([]*account, 0, len(s.accounts))
		for _, item := range s.accounts {
			copy := *item
			items = append(items, &copy)
		}
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 200}})
	case path == "/accounts/data" && r.Method == http.MethodGet:
		s.exportAccountData(w, r)
	case path == "/system/version" && r.Method == http.MethodGet:
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]string{"version": "qa-1.0"}})
	case path == "/usage/stats" && r.Method == http.MethodGet:
		s.usageStatsResponse(w, r)
	case strings.HasPrefix(path, "/accounts/"):
		s.accountRoute(w, r, path)
	default:
		write(w, http.StatusNotFound, map[string]any{"code": 404, "message": "not found"})
	}
}

func (s *state) exportAccountData(w http.ResponseWriter, r *http.Request) {
	s.exportRequests++
	if !s.exportAvailable {
		write(w, http.StatusNotFound, map[string]any{"code": 404, "message": "account export is unavailable"})
		return
	}
	if r.URL.Query().Get("include_proxies") != "false" {
		write(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "include_proxies must be false"})
		return
	}
	requested := make(map[int64]struct{})
	for _, rawID := range strings.Split(r.URL.Query().Get("ids"), ",") {
		accountID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err == nil && accountID > 0 {
			requested[accountID] = struct{}{}
		}
	}
	items := make([]map[string]any, 0, len(requested))
	for accountID := range requested {
		if s.accounts[accountID] == nil {
			continue
		}
		baseURL := "https://source.example.test/v1/?api_key=query-secret#fragment"
		if accountID == 102 {
			baseURL = "http://" + r.Host + "/newapi/v1?api_key=query-secret#fragment"
		}
		var credentials any = map[string]any{
			"base_url": baseURL,
			"api_key":  "qa-export-secret-never-return",
		}
		if s.exportCredentialsNull[accountID] {
			credentials = nil
		}
		// Match the current Sub2API DataAccount export shape: selected account
		// IDs are intentionally not included in each exported row.
		items = append(items, map[string]any{
			"name": s.accounts[accountID].Name, "platform": s.accounts[accountID].Platform,
			"type": s.accounts[accountID].Type, "credentials": credentials,
		})
	}
	write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"type": "s2api.accounts", "version": 1, "accounts": items,
	}})
}

func (s *state) accountRoute(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		write(w, http.StatusNotFound, map[string]any{"code": 404})
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	item := s.accounts[id]
	if item == nil {
		write(w, http.StatusNotFound, map[string]any{"code": 404})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		write(w, http.StatusOK, map[string]any{"code": 0, "data": item})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPut {
		var update struct {
			Concurrency    *int     `json:"concurrency"`
			LoadFactor     *int     `json:"load_factor"`
			Priority       *int     `json:"priority"`
			RateMultiplier *float64 `json:"rate_multiplier"`
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		if update.Concurrency != nil {
			item.Concurrency = *update.Concurrency
		}
		if update.LoadFactor != nil {
			item.LoadFactor = *update.LoadFactor
		}
		if update.Priority != nil {
			item.Priority = *update.Priority
		}
		if update.RateMultiplier != nil {
			item.RateMultiplier = *update.RateMultiplier
		}
		write(w, http.StatusOK, map[string]any{"code": 0, "data": item})
		return
	}
	if len(parts) != 3 {
		write(w, http.StatusNotFound, map[string]any{"code": 404})
		return
	}
	switch parts[2] {
	case "usage":
		if r.Method != http.MethodGet || r.URL.Query().Get("source") != "passive" {
			write(w, http.StatusBadRequest, map[string]any{"code": 400, "message": "passive usage required"})
			return
		}
		s.usageRequests[id]++
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"five_hour": map[string]any{"utilization": 25},
		}})
	case "schedulable":
		if s.schedulingFailures[id] {
			write(w, http.StatusServiceUnavailable, map[string]any{"code": 503, "message": "simulated scheduling update failure"})
			return
		}
		var update struct {
			Schedulable bool `json:"schedulable"`
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		item.Schedulable = update.Schedulable
		write(w, http.StatusOK, map[string]any{"code": 0, "data": item})
	case "models":
		write(w, http.StatusOK, map[string]any{"code": 0, "data": []map[string]any{
			{"id": "test-model", "type": "model", "display_name": "Test model"},
			{"id": "fallback-model", "type": "model", "display_name": "Fallback model"},
		}})
	case "upstream-billing-probe":
		write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"account_id": id,
			"snapshot":   map[string]any{"status": "ok", "data": map[string]any{"effective_rate_multiplier": s.billingRate[id]}},
		}})
	default:
		write(w, http.StatusNotFound, map[string]any{"code": 404})
	}
}

func (s *state) accountProbe(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		write(w, http.StatusNotFound, map[string]any{"code": 404})
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	s.mu.Lock()
	if s.accounts[id] == nil {
		s.mu.Unlock()
		write(w, http.StatusNotFound, map[string]any{"code": 404})
		return
	}
	probeSuccess := s.probeSuccess[id]
	probeFailure := s.probeFailures[id]
	probeDelay := s.probeDelayMS[id]
	s.probeRequests[id]++
	s.mu.Unlock()

	var input struct {
		Model string `json:"model_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.Model == "" {
		input.Model = "fixture-default"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	start, _ := json.Marshal(map[string]any{"type": "test_start", "model": input.Model})
	fmt.Fprintf(w, "data: %s\n\n", start)
	w.(http.Flusher).Flush()
	delay := time.NewTimer(time.Duration(max(probeDelay, 30)) * time.Millisecond)
	defer delay.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-delay.C:
	}
	if probeSuccess {
		fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"fixture response\"}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"type\":\"test_complete\",\"success\":true}\n\n")
		return
	}
	failure := map[string]any{"type": "test_complete", "status": "failed", "success": false, "error": "simulated failure"}
	for key, value := range probeFailure {
		failure[key] = value
	}
	encoded, _ := json.Marshal(failure)
	fmt.Fprintf(w, "data: %s\n", encoded)
	fmt.Fprintln(w)
}

func (s *state) resetControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		write(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	fresh := newState()
	s.mu.Lock()
	s.accounts = fresh.accounts
	s.probeSuccess = fresh.probeSuccess
	s.probeFailures = fresh.probeFailures
	s.probeDelayMS = fresh.probeDelayMS
	s.probeRequests = fresh.probeRequests
	s.usageRequests = fresh.usageRequests
	s.usageStats = fresh.usageStats
	s.schedulingFailures = fresh.schedulingFailures
	s.billingRate = fresh.billingRate
	s.groupRate = fresh.groupRate
	s.groupUpdates = fresh.groupUpdates
	s.exportAvailable = fresh.exportAvailable
	s.exportCredentialsNull = fresh.exportCredentialsNull
	s.exportRequests = fresh.exportRequests
	s.newAPIAuthValid = fresh.newAPIAuthValid
	s.mu.Unlock()
	write(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *state) controlStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		write(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	write(w, http.StatusOK, map[string]any{
		"group_rate": s.groupRate, "group_updates": s.groupUpdates, "export_requests": s.exportRequests,
		"probe_requests": s.probeRequests,
		"usage_requests": s.usageRequests,
	})
}

func (s *state) control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		write(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/control/"), 10, 64)
	if id <= 0 {
		write(w, http.StatusBadRequest, map[string]any{"error": "invalid account id"})
		return
	}
	var update struct {
		ProbeSuccess          *bool          `json:"probe_success"`
		ProbeFailure          map[string]any `json:"probe_failure"`
		ProbeDelayMS          *int           `json:"probe_delay_ms"`
		Schedulable           *bool          `json:"schedulable"`
		SchedulingFailure     *bool          `json:"scheduling_failure"`
		BillingRate           *float64       `json:"billing_rate"`
		ExportAvailable       *bool          `json:"export_available"`
		ExportCredentialsNull *bool          `json:"export_credentials_null"`
		NewAPIAuthValid       *bool          `json:"newapi_auth_valid"`
		Usage                 *usageSnapshot `json:"usage"`
	}
	_ = json.NewDecoder(r.Body).Decode(&update)
	s.mu.Lock()
	defer s.mu.Unlock()
	if update.ProbeSuccess != nil {
		s.probeSuccess[id] = *update.ProbeSuccess
	}
	if update.ProbeFailure != nil {
		s.probeFailures[id] = update.ProbeFailure
	}
	if update.ProbeDelayMS != nil && *update.ProbeDelayMS >= 0 && *update.ProbeDelayMS <= 10_000 {
		s.probeDelayMS[id] = *update.ProbeDelayMS
	}
	if update.Schedulable != nil && s.accounts[id] != nil {
		s.accounts[id].Schedulable = *update.Schedulable
	}
	if update.SchedulingFailure != nil {
		s.schedulingFailures[id] = *update.SchedulingFailure
	}
	if update.BillingRate != nil {
		s.billingRate[id] = *update.BillingRate
	}
	if update.ExportAvailable != nil {
		s.exportAvailable = *update.ExportAvailable
	}
	if update.ExportCredentialsNull != nil {
		s.exportCredentialsNull[id] = *update.ExportCredentialsNull
	}
	if update.NewAPIAuthValid != nil {
		s.newAPIAuthValid = *update.NewAPIAuthValid
	}
	if update.Usage != nil {
		s.usageStats[id] = *update.Usage
	}
	write(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *state) usageStatsResponse(w http.ResponseWriter, r *http.Request) {
	accountID, _ := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64)
	if accountID <= 0 || s.accounts[accountID] == nil {
		write(w, http.StatusNotFound, map[string]any{"code": 404, "message": "account not found"})
		return
	}
	s.usageRequests[accountID]++
	stats := s.usageStats[accountID]
	write(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"total_requests":              stats.Requests,
		"total_input_tokens":          stats.InputTokens,
		"total_output_tokens":         stats.OutputTokens,
		"total_cache_creation_tokens": stats.CacheCreation,
		"total_cache_read_tokens":     stats.CacheRead,
	}})
}

func (s *state) authNewAPI(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	authValid := s.newAPIAuthValid
	s.mu.Unlock()
	if !authValid {
		write(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "session expired"})
		return false
	}
	if r.Header.Get("Authorization") != "Bearer test-newapi-key" || r.Header.Get("New-Api-User") != "7" {
		write(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "invalid credential"})
		return false
	}
	return true
}

func (s *state) newAPIStatus(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"system_name": "QA NewAPI", "quota_per_unit": 500000, "version": "qa-1.0",
	}})
}

func (s *state) newAPIGroups(w http.ResponseWriter, r *http.Request) {
	if !s.authNewAPI(w, r) {
		return
	}
	write(w, http.StatusOK, map[string]any{"success": true, "message": "", "data": map[string]any{
		"codex-Plus": map[string]any{"desc": "plus pool", "ratio": 0.055},
		"default":    map[string]any{"desc": "default pool", "ratio": 0.6},
		"svip":       map[string]any{"desc": "user group", "ratio": 1.0},
	}})
}

func (s *state) newAPIDirectGroups(w http.ResponseWriter, r *http.Request) {
	if !s.authNewAPI(w, r) {
		return
	}
	write(w, http.StatusOK, map[string]any{"success": true, "groups": map[string]any{
		"direct": map[string]any{"desc": "root-level groups fixture", "ratio": 0.4},
	}})
}

func (s *state) newAPISelf(w http.ResponseWriter, r *http.Request) {
	if !s.authNewAPI(w, r) {
		return
	}
	write(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"group": "default", "quota": 1_250_000, "used_quota": 750_000,
	}})
}

func (s *state) newAPIPricing(w http.ResponseWriter, r *http.Request) {
	write(w, http.StatusOK, map[string]any{"success": true, "group_ratio": map[string]any{"default": 0.6}})
}

// This deterministic mock selects the lowest numeric priority. It intentionally
// does not emulate Sub2API's sticky sessions, weighted load or request retries.
func (s *state) forward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected *account
	for _, a := range s.accounts {
		if !a.Schedulable || a.Status != "active" {
			continue
		}
		member := false
		for _, g := range a.AccountGroups {
			if g.GroupID == 1 {
				member = true
			}
		}
		if !member {
			continue
		}
		if selected == nil || a.Priority < selected.Priority || (a.Priority == selected.Priority && a.ID < selected.ID) {
			selected = a
		}
	}
	if selected == nil {
		write(w, 503, map[string]any{"error": "no schedulable upstream"})
		return
	}
	if !s.probeSuccess[selected.ID] {
		write(w, 503, map[string]any{"error": "mock upstream failure", "upstream_account_id": selected.ID})
		return
	}
	write(w, 200, map[string]any{"model": input.Model, "upstream_account_id": selected.ID, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "mock response"}, "finish_reason": "stop"}}})
}
