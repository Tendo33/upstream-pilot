package app

import (
	"github.com/Tendo33/upstream-pilot/internal/upstream"
	"time"
)

type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type Site struct {
	ID                       string     `json:"id"`
	OwnerID                  string     `json:"owner_id,omitempty"`
	Name                     string     `json:"name"`
	BaseURL                  string     `json:"base_url"`
	Enabled                  bool       `json:"enabled"`
	ConnectionState          string     `json:"connection_state"`
	LastError                *string    `json:"last_error,omitempty"`
	VersionHint              *string    `json:"version_hint,omitempty"`
	InventoryIntervalSeconds int        `json:"inventory_interval_seconds"`
	PriorityStart            int        `json:"priority_start"`
	PriorityStep             int        `json:"priority_step"`
	ReconcileIntervalSeconds int        `json:"reconcile_interval_seconds"`
	CacheRatePriorityEnabled bool       `json:"cache_rate_priority_enabled"`
	CacheRateWindowSeconds   int        `json:"cache_rate_window_seconds"`
	RatePriorityWeight       float64    `json:"rate_priority_weight"`
	CacheRatePriorityWeight  float64    `json:"cache_rate_priority_weight"`
	LastInventoryAt          *time.Time `json:"last_inventory_at,omitempty"`
	LastReconcileAt          *time.Time `json:"last_reconcile_at,omitempty"`
	LastCacheSampleAt        *time.Time `json:"last_cache_sample_at,omitempty"`
	AccountCount             int        `json:"account_count"`
	EnabledAutomationCount   int        `json:"enabled_automation_count"`
	CreatedAt                time.Time  `json:"created_at"`
}

type GroupSummary struct {
	ID             string   `json:"id"`
	RemoteID       int64    `json:"remote_id"`
	Name           string   `json:"name"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
	Priority       *int     `json:"priority,omitempty"`
}

type Account struct {
	ID                           string         `json:"id"`
	SiteID                       string         `json:"site_id"`
	SiteName                     string         `json:"site_name"`
	RemoteID                     int64          `json:"remote_id"`
	Name                         string         `json:"name"`
	Platform                     string         `json:"platform"`
	AccountType                  string         `json:"account_type"`
	RemoteStatus                 string         `json:"remote_status"`
	Schedulable                  bool           `json:"schedulable"`
	Priority                     int            `json:"priority"`
	RateMultiplier               *float64       `json:"rate_multiplier,omitempty"`
	HealthEnabled                bool           `json:"health_enabled"`
	ProbeIntervalSeconds         int            `json:"probe_interval_seconds"`
	ProbeTimeoutSeconds          int            `json:"probe_timeout_seconds"`
	FailureThreshold             int            `json:"failure_threshold"`
	RecoverySuccessThreshold     int            `json:"recovery_success_threshold"`
	ProbeModel                   *string        `json:"probe_model,omitempty"`
	RateSyncEnabled              bool           `json:"rate_sync_enabled"`
	RateSyncIntervalSeconds      int            `json:"rate_sync_interval_seconds"`
	SourceType                   string         `json:"source_type"`
	SourceBaseURL                *string        `json:"source_base_url,omitempty"`
	ObservedSourceBaseURL        *string        `json:"observed_source_base_url"`
	SourceTypeLocked             bool           `json:"source_type_locked"`
	SourceCredentialSet          bool           `json:"source_credential_set"`
	SourceCredentialState        string         `json:"source_credential_state"`
	SourceCredentialCheckedAt    *time.Time     `json:"source_credential_checked_at,omitempty"`
	SourceUserID                 *string        `json:"source_user_id,omitempty"`
	SourceGroup                  *string        `json:"source_group,omitempty"`
	RechargeRatio                float64        `json:"recharge_ratio"`
	SourceRateMultiplier         *float64       `json:"source_rate_multiplier,omitempty"`
	SourceRateEndpoint           *string        `json:"source_rate_endpoint,omitempty"`
	PriorityEnabled              bool           `json:"priority_enabled"`
	GuardEnabled                 bool           `json:"guard_enabled"`
	GuardOperator                string         `json:"guard_operator"`
	GuardPriority                int            `json:"guard_priority"`
	GuardHolding                 bool           `json:"guard_holding"`
	HealthState                  string         `json:"health_state"`
	ConsecutiveFailures          int            `json:"consecutive_failures"`
	ConsecutiveRecoverySuccesses int            `json:"consecutive_recovery_successes"`
	ManagedHold                  bool           `json:"managed_hold"`
	LastProbeAt                  *time.Time     `json:"last_probe_at,omitempty"`
	LastProbeLatencyMS           *int           `json:"last_probe_latency_ms,omitempty"`
	LastSuccessAt                *time.Time     `json:"last_success_at,omitempty"`
	LastFailureAt                *time.Time     `json:"last_failure_at,omitempty"`
	LastFailureReason            *string        `json:"last_failure_reason,omitempty"`
	LastFailureHTTPStatus        *int           `json:"last_failure_http_status,omitempty"`
	LastRateSyncAt               *time.Time     `json:"last_rate_sync_at,omitempty"`
	LastError                    *string        `json:"last_error,omitempty"`
	CacheRate                    *float64       `json:"cache_rate,omitempty"`
	CacheRateTokens              int64          `json:"cache_rate_tokens"`
	CacheRateSampledAt           *time.Time     `json:"cache_rate_sampled_at,omitempty"`
	UptimePercent                *float64       `json:"uptime_percent"`
	UptimeSuccesses              int            `json:"uptime_successes"`
	UptimeTotal                  int            `json:"uptime_total"`
	UptimeWindowSize             int            `json:"uptime_window_size"`
	UptimeWindowStartedAt        *time.Time     `json:"uptime_window_started_at,omitempty"`
	UptimeWindowEndedAt          *time.Time     `json:"uptime_window_ended_at,omitempty"`
	UptimeTimeline               string         `json:"uptime_timeline"`
	Groups                       []GroupSummary `json:"groups"`
}

type Identity struct {
	User
	SessionID string
	CSRFHash  []byte
}

type SiteSecret struct {
	ID               string
	OwnerID          string
	Name             string
	BaseURL          string
	APIKeyCiphertext string
	Enabled          bool
}

type AccountWork struct {
	ConfigGeneration  int64
	SourceGeneration  int64
	NativeConstraints upstream.NativeConstraints
	NativeCheckedAt   *time.Time
	Account
	OwnerID                    string
	SiteBaseURL                string
	SiteAPIKeyCiphertext       string
	SourceCredentialCiphertext string
}
