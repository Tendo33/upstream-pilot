export type Role = "admin" | "user";

export interface User {
  id: string;
  email: string;
  role: Role;
  enabled: boolean;
  created_at: string;
  last_login_at?: string;
}

export interface SetupStatus {
  initialized: boolean;
}

export interface VersionStatus {
  current_version: string;
  latest_version?: string;
  update_available: boolean;
  repository_url: string;
  release_url?: string;
  commit: string;
  build_time: string;
  checked_at: string;
}

export interface Overview {
  sites: number;
  accounts: number;
  automated: number;
  healthy: number;
  failing: number;
  paused: number;
  recent_failures: number;
}

export interface Site {
  id: string;
  owner_id?: string;
  name: string;
  base_url: string;
  enabled: boolean;
  connection_state: "unknown" | "healthy" | "unreachable" | "auth_error" | string;
  last_error?: string;
  version_hint?: string;
  inventory_interval_seconds: number;
  priority_start: number;
  priority_step: number;
  reconcile_interval_seconds: number;
  cache_rate_priority_enabled: boolean;
  cache_rate_window_seconds: number;
  rate_priority_weight: number;
  cache_rate_priority_weight: number;
  last_inventory_at?: string;
  last_reconcile_at?: string;
  last_cache_sample_at?: string;
  account_count: number;
  enabled_automation_count: number;
  created_at: string;
}

export interface SiteInput {
  name: string;
  base_url: string;
  api_key: string;
  enabled: boolean;
  inventory_interval_seconds: number;
  priority_start: number;
  priority_step: number;
  reconcile_interval_seconds: number;
  cache_rate_priority_enabled: boolean;
  cache_rate_window_seconds: number;
  rate_priority_weight: number;
  cache_rate_priority_weight: number;
}

export interface GroupSummary {
  id: string;
  remote_id: number;
  name: string;
  rate_multiplier?: number;
  priority?: number;
}

export type GroupRateMode = "first" | "average" | "min" | "max" | "custom";

export interface GroupRateRule {
  enabled: boolean;
  mode: GroupRateMode;
  offset: number;
  expression?: string;
  last_calculated_rate?: number;
  last_applied_at?: string;
  last_error?: string;
}

export interface GroupRateBinding {
  id: string;
  account_id: string;
  account_name: string;
  site_id: string;
  site_name: string;
  platform: string;
  rate_multiplier?: number;
  source_rate_multiplier?: number;
  position: number;
  available: boolean;
}

export interface ManagedGroup {
  id: string;
  site_id: string;
  site_name: string;
  remote_id: number;
  name: string;
  platform?: string;
  status?: string;
  rate_multiplier?: number;
  member_count: number;
  observed_at: string;
  updated_at: string;
  rule: GroupRateRule;
  bindings: GroupRateBinding[];
}

export type HealthState = "unknown" | "healthy" | "failing" | "paused";
export type SourceType = "sub2api" | "newapi";
export type SourceCredentialState = "unknown" | "valid" | "invalid";
export type FailureReason = "AUTH" | "BALANCE" | "RATE_LIMIT" | "UPSTREAM" | "TIMEOUT" | "CONFIGURATION" | "UNKNOWN";
export type AccountBalanceStatus = "pending" | "ok" | "unsupported" | "invalid" | "error";

export interface AccountBalance {
  account_id: string;
  status: AccountBalanceStatus;
  provider?: "usage" | "newapi" | string;
  plan_name?: string;
  remaining: number | null;
  used?: number | null;
  total?: number | null;
  unit?: string;
  message?: string;
  endpoint?: string;
  checked_at: string | null;
}

export interface BalanceAlertSettings {
  enabled: boolean;
  threshold: number;
  cooldown_seconds: number;
  webhook_configured: boolean;
  cooldown_until?: string;
  last_attempt_at?: string;
  last_notified_at?: string;
  last_error?: string;
}

export interface Account {
  id: string;
  site_id: string;
  site_name: string;
  remote_id: number;
  name: string;
  platform: string;
  account_type: string;
  remote_status: string;
  schedulable: boolean;
  priority: number;
  rate_multiplier?: number;
  health_enabled: boolean;
  probe_interval_seconds: number;
  probe_timeout_seconds: number;
  failure_threshold: number;
  recovery_success_threshold: number;
  probe_model?: string;
  rate_sync_enabled: boolean;
  rate_sync_interval_seconds: number;
  source_type: SourceType;
  source_base_url?: string;
  observed_source_base_url: string | null;
  source_type_locked: boolean;
  source_credential_set: boolean;
  source_credential_state: SourceCredentialState;
  source_credential_checked_at?: string;
  source_user_id?: string;
  source_group?: string;
  recharge_ratio: number;
  source_rate_multiplier?: number;
  source_rate_endpoint?: string;
  priority_enabled: boolean;
  guard_enabled: boolean;
  guard_operator: "gt" | "gte";
  guard_priority: number;
  guard_holding: boolean;
  health_state: HealthState;
  consecutive_failures: number;
  consecutive_recovery_successes: number;
  managed_hold: boolean;
  last_probe_at?: string;
  last_probe_latency_ms?: number;
  last_success_at?: string;
  last_failure_at?: string;
  last_failure_reason?: FailureReason;
  last_failure_http_status?: number;
  last_rate_sync_at?: string;
  last_error?: string;
  cache_rate?: number;
  cache_rate_tokens: number;
  cache_rate_sampled_at?: string;
  uptime_percent: number | null;
  uptime_successes: number;
  uptime_total: number;
  uptime_window_size: number;
  uptime_window_started_at?: string;
  uptime_window_ended_at?: string;
  uptime_timeline: string;
  groups: GroupSummary[];
}

export interface AccountSettingsInput {
  health_enabled: boolean;
  probe_interval_seconds: number;
  probe_timeout_seconds: number;
  failure_threshold: number;
  recovery_success_threshold: number;
  probe_model: string | null;
  rate_sync_enabled: boolean;
  rate_sync_interval_seconds: number;
  source_type: SourceType;
  source_type_locked: boolean;
  source_base_url: string | null;
  source_credential?: string;
  clear_source_credential?: boolean;
  source_user_id: string | null;
  source_group: string | null;
  recharge_ratio: number;
  priority_enabled: boolean;
  guard_enabled: boolean;
  guard_operator: "gt" | "gte";
  guard_priority: number;
}

export interface BulkAccountSettingsInput {
  account_ids: string[];
  health?: {
    enabled: boolean;
    probe_interval_seconds: number;
    probe_timeout_seconds: number;
    failure_threshold: number;
    recovery_success_threshold: number;
    probe_model: string | null;
  };
  rate_sync?: {
    enabled: boolean;
    interval_seconds: number;
  };
  priority?: {
    enabled: boolean;
  };
  guard?: {
    enabled: boolean;
    operator: "gt" | "gte";
    priority: number;
  };
}

export interface BulkAccountSettingsResponse {
  accounts: Account[];
  updated_count: number;
}

export interface AuditEvent {
  id: string;
  action: string;
  outcome: "success" | "failed" | "skipped" | string;
  detail: Record<string, unknown>;
  created_at: string;
  site_id?: string;
  account_id?: string;
  site_name: string;
  account_name: string;
}

export interface AuditEventPage {
  items: AuditEvent[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
  has_previous: boolean;
  has_next: boolean;
}

export interface AuditLogSettings {
  retention_days: number;
  configured: boolean;
  last_purged_at?: string;
  last_purge_removed_files: number;
  last_purge_removed_records: number;
}

export interface SourceGroup {
  group: string;
  description?: string;
  rate: number;
  endpoint: string;
}

export interface ProbeModel {
  id: string;
  type?: string;
  display_name?: string;
}
