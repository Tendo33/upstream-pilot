import type { Account, AccountSettingsInput } from "./types";

export function formatDate(value?: string): string {
  if (!value) return "尚未执行";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

export function formatFullDate(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

export function relativeTime(value?: string): string {
  if (!value) return "从未";
  const delta = new Date(value).getTime() - Date.now();
  if (Number.isNaN(delta)) return "-";
  const abs = Math.abs(delta);
  const unit = abs < 60_000 ? "second" : abs < 3_600_000 ? "minute" : abs < 86_400_000 ? "hour" : "day";
  const divisor = unit === "second" ? 1_000 : unit === "minute" ? 60_000 : unit === "hour" ? 3_600_000 : 86_400_000;
  return new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" }).format(Math.round(delta / divisor), unit);
}

export function formatRate(value?: number): string {
  if (value == null) return "-";
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 6 }).format(value)}x`;
}

export function formatPercent(value?: number | null): string {
  if (value == null || !Number.isFinite(value)) return "-";
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(value)}%`;
}

export function compactNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN", { notation: value >= 10_000 ? "compact" : "standard" }).format(value);
}

export function effectiveSourceBaseURL(account: Account): string | null {
  return account.source_base_url ?? account.observed_source_base_url ?? null;
}

export function settingsFromAccount(account: Account): AccountSettingsInput {
  return {
    health_enabled: account.health_enabled,
    probe_interval_seconds: account.probe_interval_seconds,
    probe_timeout_seconds: account.probe_timeout_seconds,
    failure_threshold: account.failure_threshold,
    recovery_success_threshold: account.recovery_success_threshold,
    probe_model: account.probe_model ?? null,
    rate_sync_enabled: account.rate_sync_enabled,
    rate_sync_interval_seconds: account.rate_sync_interval_seconds,
    source_type: account.source_type,
    source_type_locked: account.source_type_locked,
    source_base_url: account.source_base_url ?? null,
    source_user_id: account.source_user_id ?? null,
    source_group: account.source_group ?? null,
    recharge_ratio: account.recharge_ratio,
    priority_enabled: account.priority_enabled,
    guard_enabled: account.guard_enabled,
    guard_operator: account.guard_operator,
    guard_priority: account.guard_priority,
  };
}

export function truncate(value: string, max = 72): string {
  return value.length > max ? `${value.slice(0, max)}...` : value;
}
