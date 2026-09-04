import {
  CircleAlert,
  Database,
  ExternalLink,
  Gauge,
  Layers3,
  HeartPulse,
  ListChecks,
  ListFilter,
  Pencil,
  Play,
  Power,
  Percent,
  RefreshCw,
  Search,
  ShieldAlert,
  WalletCards,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { api, errorMessage, json } from "../api";
import { effectiveSourceBaseURL, formatDate, formatPercent, formatRate, settingsFromAccount } from "../lib";
import type {
  Account,
  AccountBalance,
  AccountSettingsInput,
  BulkAccountSettingsInput,
  BulkAccountSettingsResponse,
  FailureReason,
  HealthState,
  ProbeModel,
  Site,
  SourceGroup,
  SourceType,
} from "../types";
import {
  Badge,
  Button,
  Combobox,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Modal,
  PageHeader,
  PageLoader,
  SelectMenu,
  Switch,
  cx,
  useToast,
} from "../components/ui";

type ToggleKey = "health_enabled" | "rate_sync_enabled" | "priority_enabled" | "guard_enabled";

interface ProbeResponse {
  result: { success: boolean; message: string; latency_ms: number; failure_reason?: FailureReason; http_status?: number; paused: boolean; restored: boolean };
  account: Account;
}

interface RateSyncResponse {
  result: { source_rate: number; effective_rate: number; endpoint: string };
  account: Account;
}

interface AccountFilterOptions {
  platforms: string[];
  groups: Array<{ id: string; name: string; site_id: string; site_name: string }>;
  invalid_source_credentials: number;
}

export function AccountsPage() {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [siteID, setSiteID] = useState("");
  const [state, setState] = useState("all");
  const [platform, setPlatform] = useState("");
  const [groupID, setGroupID] = useState("");
  const [filterOptions, setFilterOptions] = useState<AccountFilterOptions>({ platforms: [], groups: [], invalid_source_credentials: 0 });
  const [balances, setBalances] = useState<Record<string, AccountBalance>>({});
  const [balanceError, setBalanceError] = useState("");
  const balanceRequestSequence = useRef(0);
  const [editing, setEditing] = useState<Account | null>(null);
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [bulkEditing, setBulkEditing] = useState(false);
  const [busy, setBusy] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const { toast } = useToast();

  const query = useMemo(() => {
    const params = new URLSearchParams();
    if (search.trim()) params.set("search", search.trim());
    if (siteID) params.set("site_id", siteID);
    if (state !== "all") params.set("state", state);
    if (platform) params.set("platform", platform);
    if (groupID) params.set("group_id", groupID);
    const value = params.toString();
    return value ? `?${value}` : "";
  }, [groupID, platform, search, siteID, state]);

  const loadBalances = useCallback(async (items: Account[], merge = false) => {
    const requestSequence = ++balanceRequestSequence.current;
    if (items.length === 0) {
      if (!merge) setBalances({});
      setBalanceError("");
      return;
    }
    setBalanceError("");
    try {
      const results: AccountBalance[] = [];
      for (let start = 0; start < items.length; start += 200) {
        results.push(...await api<AccountBalance[]>("/accounts/balances", {
          method: "POST",
          ...json({ account_ids: items.slice(start, start + 200).map((account) => account.id) }),
        }));
      }
      if (requestSequence !== balanceRequestSequence.current) return;
      const next = Object.fromEntries(results.map((balance) => [balance.account_id, balance]));
      setBalances((current) => merge ? { ...current, ...next } : next);
    } catch (cause) {
      if (requestSequence !== balanceRequestSequence.current) return;
      setBalanceError(errorMessage(cause));
      if (!merge) setBalances({});
    }
  }, []);

  const loadAccounts = useCallback(async (quiet = false) => {
    setError("");
    setRefreshing(quiet);
    try {
      const items = await api<Account[]>(`/accounts${query}`);
      setAccounts(items);
      if (quiet) await loadBalances(items);
      else void loadBalances(items);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setRefreshing(false);
    }
  }, [loadBalances, query]);

  const loadFilterOptions = useCallback(async () => {
    setFilterOptions(await api<AccountFilterOptions>("/accounts/filter-options"));
  }, []);

  useEffect(() => {
    void Promise.all([api<Site[]>("/sites"), api<AccountFilterOptions>("/accounts/filter-options")])
      .then(([siteItems, options]) => { setSites(siteItems); setFilterOptions(options); })
      .catch((cause) => setError(errorMessage(cause)));
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(() => void loadAccounts(), search ? 280 : 0);
    return () => window.clearTimeout(timer);
  }, [loadAccounts, search]);

  useEffect(() => {
    if (!accounts?.length || !Object.values(balances).some((balance) => balance.status === "pending")) return;
    const timer = window.setTimeout(() => void loadBalances(accounts, true), 5_000);
    return () => window.clearTimeout(timer);
  }, [accounts, balances, loadBalances]);

  useEffect(() => {
    if (!accounts?.length) return;
    const timer = window.setInterval(() => void loadBalances(accounts, true), 60_000);
    return () => window.clearInterval(timer);
  }, [accounts, loadBalances]);

  useEffect(() => {
    if (!accounts) return;
    const visibleIDs = new Set(accounts.map((account) => account.id));
    setSelectedIDs((current) => {
      const next = current.filter((accountID) => visibleIDs.has(accountID));
      return next.length === current.length ? current : next;
    });
  }, [accounts]);

  function replaceAccount(updated: Account) {
    setAccounts((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? null);
    setEditing((current) => current?.id === updated.id ? updated : current);
  }

  function toggleAccountSelection(accountID: string, selected: boolean) {
    setSelectedIDs((current) => selected
      ? current.includes(accountID) ? current : [...current, accountID]
      : current.filter((item) => item !== accountID));
  }

  async function quickToggle(account: Account, key: ToggleKey, value: boolean) {
    setBusy(`${account.id}:${key}`);
    try {
      const updated = await api<Account>(`/accounts/${account.id}/settings`, {
        method: "PUT",
        ...json({ ...settingsFromAccount(account), [key]: value }),
      });
      replaceAccount(updated);
      toast(`${toggleLabel(key)}已${value ? "启用" : "停用"}`, "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setBusy("");
    }
  }

  async function updateScheduling(account: Account, schedulable: boolean) {
    setBusy(`${account.id}:scheduling`);
    try {
      const updated = await api<Account>(`/accounts/${account.id}/scheduling`, {
        method: "PUT",
        ...json({ schedulable }),
      });
      replaceAccount(updated);
      toast(schedulable ? "账号调度已手动开启" : "账号调度已手动关闭", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
      await loadAccounts(true);
    } finally {
      setBusy("");
    }
  }

  async function runAccountAction(account: Account, action: "probe" | "rate-sync" | "cache-sample") {
    setBusy(`${account.id}:${action}`);
    try {
      if (action === "probe") {
        const response = await api<ProbeResponse>(`/accounts/${account.id}/probe`, { method: "POST" });
        replaceAccount(response.account);
        const result = response.result;
        if (result.success) {
          toast(`测活成功 · ${result.latency_ms} ms${result.restored ? " · 已恢复调度" : ""}`, "success");
        } else {
          toast(`测活失败${result.paused ? " · 已暂停调度" : ""}：${result.message || "上游未通过检测"}`, "error");
        }
      } else if (action === "rate-sync") {
        const response = await api<RateSyncResponse>(`/accounts/${account.id}/rate-sync`, { method: "POST" });
        replaceAccount(response.account);
        await loadFilterOptions();
        toast(`倍率已写回：${formatRate(response.result.effective_rate)}（源站 ${formatRate(response.result.source_rate)}）`, "success");
      } else {
        const response = await api<{ account: Account }>(`/accounts/${account.id}/cache-sample`, { method: "POST" });
        replaceAccount(response.account);
        toast(response.account.cache_rate != null ? `缓存率 ${formatPercent(response.account.cache_rate * 100)}` : "已采样，窗口内暂无有效缓存率", "success");
      }
    } catch (cause) {
      toast(errorMessage(cause), "error");
      await Promise.all([loadAccounts(true), loadFilterOptions()]);
    } finally {
      setBusy("");
    }
  }

  if (!accounts && !error) return <PageLoader />;
  if (!accounts) return <ErrorState message={error} retry={() => void loadAccounts()} />;

  const filtersActive = Boolean(search || siteID || state !== "all" || platform || groupID);
  const selectedIDSet = new Set(selectedIDs);
  const selectedAccounts = accounts.filter((account) => selectedIDSet.has(account.id));
  const allSelected = accounts.length > 0 && selectedAccounts.length === accounts.length;
  const visibleInvalidSourceAccounts = accounts.filter((account) => account.source_type === "newapi" && account.source_credential_state === "invalid");
  const invalidSourceCredentialCount = Math.max(filterOptions.invalid_source_credentials, visibleInvalidSourceAccounts.length);

  return (
    <div className="page accounts-page">
      <PageHeader
        eyebrow="Automation"
        title="账号"
        description="每项自动化策略均可按账号独立启用"
        actions={
          <IconButton label="刷新账号及余额快照" onClick={() => void Promise.all([loadAccounts(true), loadFilterOptions()])} disabled={refreshing}>
            <RefreshCw size={17} className={refreshing ? "spin" : undefined} />
          </IconButton>
        }
      />

      {invalidSourceCredentialCount ? (
        <div className="source-auth-alert" role="alert">
          <ShieldAlert size={18} aria-hidden="true" />
          <span>
            <strong>{invalidSourceCredentialCount} 个 NewAPI 账号的源站凭据已失效</strong>
            <small>请更新对应账号的 Session 或 Token；凭据验证成功后警告会自动解除。</small>
          </span>
        </div>
      ) : null}

      <div className="filter-bar">
        <div className="search-box">
          <Search size={16} />
          <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索账号、平台或远程 ID" aria-label="搜索账号" />
          {search ? <IconButton label="清空搜索" onClick={() => setSearch("")}><X size={15} /></IconButton> : null}
        </div>
        <SelectMenu
          className="select-control-filter"
          label="站点"
          icon={<Database size={15} />}
          value={siteID}
          onChange={setSiteID}
          options={[{ value: "", label: "全部站点" }, ...sites.map((site) => ({ value: site.id, label: site.name }))]}
        />
        <SelectMenu
          className="select-control-filter"
          label="测活状态"
          icon={<ListFilter size={15} />}
          value={state}
          onChange={setState}
          options={[
            { value: "all", label: "全部状态" },
            { value: "healthy", label: "健康" },
            { value: "failing", label: "异常" },
            { value: "paused", label: "已暂停" },
            { value: "unknown", label: "未检测" },
          ]}
        />
        <SelectMenu
          className="select-control-filter"
          label="平台"
          icon={<Gauge size={15} />}
          value={platform}
          onChange={setPlatform}
          options={[{ value: "", label: "全部平台" }, ...filterOptions.platforms.map((item) => ({ value: item, label: item }))]}
        />
        <SelectMenu
          className="select-control-filter"
          label="分组"
          icon={<Layers3 size={15} />}
          value={groupID}
          onChange={setGroupID}
          options={[
            { value: "", label: "全部分组" },
            ...filterOptions.groups.map((group) => ({ value: group.id, label: group.name, description: group.site_name })),
          ]}
        />
        <label className="account-mobile-select-all">
          <SelectionCheckbox
            checked={allSelected}
            indeterminate={selectedAccounts.length > 0 && !allSelected}
            label={allSelected ? "取消选择全部账号" : "选择全部账号"}
            onChange={(selected) => setSelectedIDs(selected ? accounts.map((account) => account.id) : [])}
          />
          <span>全选当前</span>
        </label>
        <span className="result-count">{accounts.length} 个账号</span>
      </div>

      {selectedAccounts.length ? (
        <div className="account-bulk-bar" role="region" aria-label="批量账号操作">
          <strong>已选择 {selectedAccounts.length} 个账号</strong>
          <span>{new Set(selectedAccounts.map((account) => account.site_id)).size} 个站点</span>
          <div>
            <Button size="sm" onClick={() => setSelectedIDs([])}>清除选择</Button>
            <Button size="sm" variant="primary" onClick={() => setBulkEditing(true)}><ListChecks size={15} />批量编辑</Button>
          </div>
        </div>
      ) : null}

      {error ? <div className="inline-alert"><CircleAlert size={16} /><span>{error}</span></div> : null}

      {accounts.length === 0 ? (
        <section className="panel">
          <EmptyState
            title={filtersActive ? "没有匹配的账号" : sites.length ? "暂无账号库存" : "请先添加站点"}
            description={filtersActive ? "调整搜索条件或清除筛选后重试。" : sites.length ? "在站点页执行一次库存同步。" : "账号会在站点同步后显示。"}
            action={filtersActive ? <Button onClick={() => { setSearch(""); setSiteID(""); setState("all"); setPlatform(""); setGroupID(""); }}>清除筛选</Button> : undefined}
            icon={<Database size={21} />}
          />
        </section>
      ) : (
        <div className="account-table" role="table" aria-label="账号列表">
          <div className="account-table-head" role="row">
            <span className="account-select-heading">
              <SelectionCheckbox
                checked={allSelected}
                indeterminate={selectedAccounts.length > 0 && !allSelected}
                label={allSelected ? "取消选择全部账号" : "选择全部账号"}
                onChange={(selected) => setSelectedIDs(selected ? accounts.map((account) => account.id) : [])}
              />
              账号
            </span><span>探测</span><span>倍率</span><span>缓存率</span><span>余额</span><span>优先级</span><span>自动化</span><span className="sr-only">操作</span>
          </div>
          {accounts.map((account) => {
            const sourceBaseURL = effectiveSourceBaseURL(account);
            const sourceCredentialInvalid = account.source_type === "newapi" && account.source_credential_state === "invalid";
            return (
            <article className={cx("account-row", selectedIDSet.has(account.id) && "account-row-selected", account.last_failure_reason === "BALANCE" && "account-row-balance", sourceCredentialInvalid && "account-row-source-auth-error")} role="row" key={account.id}>
              <div className="account-identity" role="cell">
                <div className="account-name-line">
                  <SelectionCheckbox
                    checked={selectedIDSet.has(account.id)}
                    label={`${selectedIDSet.has(account.id) ? "取消选择" : "选择"}账号 ${account.name}`}
                    onChange={(selected) => toggleAccountSelection(account.id, selected)}
                  />
                  {sourceBaseURL ? (
                    <a className="account-source-link" href={sourceBaseURL} target="_blank" rel="noopener noreferrer" title={sourceBaseURL}>
                      <strong title={account.name}>{account.name}</strong>
                      <ExternalLink size={12} aria-hidden="true" />
                    </a>
                  ) : <strong title={account.name}>{account.name}</strong>}
                  {account.managed_hold
                    ? <Badge tone="warning">测活暂停</Badge>
                    : !account.schedulable ? <Badge tone="neutral">手动关闭</Badge> : null}
                  {sourceCredentialInvalid ? <Badge tone="danger">NewAPI 凭据失效</Badge> : null}
                </div>
                <span>{account.site_name} · {account.platform || account.account_type || "未知平台"} · #{account.remote_id}</span>
                {account.groups.length ? <div className="account-groups">{account.groups.slice(0, 2).map((group) => <Badge key={group.id}>{group.name}</Badge>)}{account.groups.length > 2 ? <Badge>+{account.groups.length - 2}</Badge> : null}</div> : null}
                <div
                  className={cx("account-scheduling-control", account.managed_hold && "account-scheduling-managed")}
                  title={account.managed_hold ? "调度意图为开启，当前因测活失败由系统暂停" : account.schedulable ? "调度已手动开启" : "调度已手动关闭，探测成功不会自动开启"}
                >
                  <span><Power size={13} />调度</span>
                  <Switch
                    checked={account.schedulable || account.managed_hold}
                    onChange={(value) => void updateScheduling(account, value)}
                    label="账号调度"
                    disabled={busy.startsWith(account.id)}
                  />
                </div>
              </div>
              <div className="account-health" role="cell">
                <HealthBadge state={account.health_state} />
                <AccountUptime account={account} />
                <small>{account.last_probe_latency_ms != null ? `${account.last_probe_latency_ms} ms` : formatDate(account.last_probe_at)}</small>
                {account.managed_hold && account.consecutive_recovery_successes > 0 ? <small>连续正常 {account.consecutive_recovery_successes}/{account.recovery_success_threshold} 次</small> : null}
                {account.consecutive_failures > 0 ? <small className="danger-text">连续失败 {account.consecutive_failures} 次</small> : null}
                {account.last_failure_reason ? <small className="danger-text">{failureReasonLabel(account.last_failure_reason)}{account.last_failure_http_status ? ` · HTTP ${account.last_failure_http_status}` : ""}</small> : null}
              </div>
              <div className="account-rate" role="cell">
                <strong>{formatRate(account.rate_multiplier)}</strong>
                <span>{account.source_type === "newapi" ? "NewAPI" : "Sub2API"}{account.source_rate_multiplier != null ? ` · 源 ${formatRate(account.source_rate_multiplier)}` : ""}</span>
              </div>
              <div className="account-cache" role="cell" title={account.cache_rate_tokens ? `${account.cache_rate_tokens} tokens` : undefined}>
                <strong>{account.cache_rate != null ? formatPercent(account.cache_rate * 100) : "暂无"}</strong>
                <span>{account.cache_rate_sampled_at ? formatDate(account.cache_rate_sampled_at) : "尚未采样"}</span>
              </div>
              <AccountBalanceValue balance={balances[account.id]} requestError={balanceError} />
              <div className="account-priority" role="cell">
                <strong>{account.priority}</strong>
                {account.guard_holding ? <Badge tone="warning">保护中</Badge> : <span>{account.priority_enabled ? "自动排序" : "固定"}</span>}
              </div>
              <div className="automation-switches" role="cell">
                <AutomationToggle icon={<HeartPulse size={15} />} label="测活" checked={account.health_enabled} disabled={busy.startsWith(account.id)} onChange={(value) => void quickToggle(account, "health_enabled", value)} />
                <AutomationToggle icon={<RefreshCw size={15} />} label="倍率采集" checked={account.rate_sync_enabled} disabled={busy.startsWith(account.id)} onChange={(value) => void quickToggle(account, "rate_sync_enabled", value)} />
              </div>
              <div className="account-actions" role="cell">
                <IconButton label="立即测活" onClick={() => void runAccountAction(account, "probe")} disabled={Boolean(busy)}>
                  <Play size={16} className={busy === `${account.id}:probe` ? "spin" : undefined} />
                </IconButton>
                <IconButton label="立即同步倍率" onClick={() => void runAccountAction(account, "rate-sync")} disabled={Boolean(busy)}>
                  <Gauge size={16} className={busy === `${account.id}:rate-sync` ? "spin" : undefined} />
                </IconButton>
                <IconButton label="采样缓存率" onClick={() => void runAccountAction(account, "cache-sample")} disabled={Boolean(busy)}>
                  <Percent size={16} className={busy === `${account.id}:cache-sample` ? "spin" : undefined} />
                </IconButton>
                <IconButton label="账号设置" onClick={() => setEditing(account)}><Pencil size={16} /></IconButton>
              </div>
              {sourceCredentialInvalid ? (
                <div className="account-error account-source-auth-error">
                  <ShieldAlert size={14} />
                  <span title={account.last_error}>NewAPI Session 或 Token 已过期或无效，请更新源站凭据</span>
                </div>
              ) : account.last_error ? <div className="account-error"><CircleAlert size={14} /><span title={account.last_error}>{account.last_error}</span></div> : null}
            </article>
            );
          })}
        </div>
      )}

      <AccountSettingsModal
        account={editing}
        onClose={() => setEditing(null)}
        onSaved={(updated) => { replaceAccount(updated); setEditing(null); void Promise.all([loadFilterOptions(), loadBalances([updated], true)]); }}
      />
      <BulkAccountSettingsModal
        accounts={selectedAccounts}
        open={bulkEditing && selectedAccounts.length > 0}
        onClose={() => setBulkEditing(false)}
        onSaved={(updated) => {
          const byID = new Map(updated.map((account) => [account.id, account]));
          setAccounts((current) => current?.map((account) => byID.get(account.id) ?? account) ?? null);
          setSelectedIDs([]);
          setBulkEditing(false);
          void Promise.all([loadFilterOptions(), loadBalances(updated, true)]);
        }}
      />
    </div>
  );
}

function SelectionCheckbox({ checked, indeterminate = false, label, disabled, onChange }: {
  checked: boolean;
  indeterminate?: boolean;
  label: string;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (inputRef.current) inputRef.current.indeterminate = indeterminate;
  }, [indeterminate]);

  return (
    <input
      ref={inputRef}
      className="selection-checkbox"
      type="checkbox"
      checked={checked}
      disabled={disabled}
      aria-label={label}
      title={label}
      onChange={(event) => onChange(event.target.checked)}
    />
  );
}

type BulkSectionKey = "health" | "rateSync" | "priority" | "guard";

interface BulkSettingsValues {
  health_enabled: boolean;
  probe_interval_seconds: number;
  probe_timeout_seconds: number;
  failure_threshold: number;
  recovery_success_threshold: number;
  probe_model: string;
  rate_sync_enabled: boolean;
  rate_sync_interval_seconds: number;
  priority_enabled: boolean;
  guard_enabled: boolean;
  guard_operator: "gt" | "gte";
  guard_priority: number;
}

function bulkSettingsFromAccount(account: Account): BulkSettingsValues {
  return {
    health_enabled: account.health_enabled,
    probe_interval_seconds: account.probe_interval_seconds,
    probe_timeout_seconds: account.probe_timeout_seconds,
    failure_threshold: account.failure_threshold,
    recovery_success_threshold: account.recovery_success_threshold,
    probe_model: account.probe_model ?? "",
    rate_sync_enabled: account.rate_sync_enabled,
    rate_sync_interval_seconds: account.rate_sync_interval_seconds,
    priority_enabled: account.priority_enabled,
    guard_enabled: account.guard_enabled,
    guard_operator: account.guard_operator,
    guard_priority: account.guard_priority,
  };
}

function BulkAccountSettingsModal({ accounts, open, onClose, onSaved }: {
  accounts: Account[];
  open: boolean;
  onClose: () => void;
  onSaved: (accounts: Account[]) => void;
}) {
  const [values, setValues] = useState<BulkSettingsValues | null>(null);
  const [applied, setApplied] = useState<Record<BulkSectionKey, boolean>>({ health: false, rateSync: false, priority: false, guard: false });
  const [saving, setSaving] = useState(false);
  const { toast } = useToast();
  const accountKey = accounts.map((account) => account.id).join(",");

  useEffect(() => {
    if (!open || accounts.length === 0) {
      setValues(null);
      return;
    }
    setValues(bulkSettingsFromAccount(accounts[0]));
    setApplied({ health: false, rateSync: false, priority: false, guard: false });
  }, [accountKey, open]);

  if (!values) {
    return null;
  }

  const siteCount = new Set(accounts.map((account) => account.site_id)).size;
  const anyApplied = Object.values(applied).some(Boolean);
  const healthMixed = !accounts.every((account) =>
    account.health_enabled === accounts[0].health_enabled
    && account.probe_interval_seconds === accounts[0].probe_interval_seconds
    && account.probe_timeout_seconds === accounts[0].probe_timeout_seconds
    && account.failure_threshold === accounts[0].failure_threshold
    && account.recovery_success_threshold === accounts[0].recovery_success_threshold
    && (account.probe_model ?? "") === (accounts[0].probe_model ?? ""));
  const rateSyncMixed = !accounts.every((account) => account.rate_sync_enabled === accounts[0].rate_sync_enabled && account.rate_sync_interval_seconds === accounts[0].rate_sync_interval_seconds);

  function update<K extends keyof BulkSettingsValues>(key: K, value: BulkSettingsValues[K]) {
    setValues((current) => current ? { ...current, [key]: value } : current);
  }

  function setSection(section: BulkSectionKey, checked: boolean) {
    setApplied((current) => ({ ...current, [section]: checked }));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const submittedValues = values;
    if (!anyApplied || !submittedValues) return;
    const input: BulkAccountSettingsInput = { account_ids: accounts.map((account) => account.id) };
    if (applied.health) {
      input.health = {
        enabled: submittedValues.health_enabled,
        probe_interval_seconds: submittedValues.probe_interval_seconds,
        probe_timeout_seconds: submittedValues.probe_timeout_seconds,
        failure_threshold: submittedValues.failure_threshold,
        recovery_success_threshold: submittedValues.recovery_success_threshold,
        probe_model: submittedValues.probe_model.trim() || null,
      };
    }
    if (applied.rateSync) {
      input.rate_sync = {
        enabled: submittedValues.rate_sync_enabled,
        interval_seconds: submittedValues.rate_sync_interval_seconds,
      };
    }
    if (applied.priority) input.priority = { enabled: submittedValues.priority_enabled };
    if (applied.guard) {
      input.guard = {
        enabled: submittedValues.guard_enabled,
        operator: submittedValues.guard_operator,
        priority: submittedValues.guard_priority,
      };
    }
    setSaving(true);
    try {
      const response = await api<BulkAccountSettingsResponse>("/accounts/bulk-settings", {
        method: "PATCH",
        ...json(input),
      });
      onSaved(response.accounts);
      toast(`已批量更新 ${response.updated_count} 个账号`, "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      title="批量编辑账号"
      description={`已选择 ${accounts.length} 个账号 · ${siteCount} 个站点`}
      onClose={() => !saving && onClose()}
      width="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={saving}>取消</Button>
          <Button variant="primary" type="submit" form="bulk-account-settings-form" loading={saving} disabled={!anyApplied}>应用到所选账号</Button>
        </>
      }
    >
      <form id="bulk-account-settings-form" className="settings-form bulk-settings-form" onSubmit={save}>
        <BulkAutomationSection
          icon={<HeartPulse size={17} />}
          title="账号测活"
          meta={applied.health ? (values.health_enabled ? `每 ${values.probe_interval_seconds} 秒` : "将停用") : healthMixed ? "当前值不一致" : "当前值一致"}
          applied={applied.health}
          onApply={(checked) => setSection("health", checked)}
        >
          <div className="setting-inline"><span>启用账号测活</span><Switch checked={values.health_enabled} onChange={(checked) => update("health_enabled", checked)} label="启用账号测活" /></div>
          <div className="form-grid four">
            <Field label="探测间隔" hint="10 至 86400 秒"><NumberInput value={values.probe_interval_seconds} min={10} max={86400} onChange={(value) => update("probe_interval_seconds", value)} /></Field>
            <Field label="超时时间" hint="3 至 600 秒"><NumberInput value={values.probe_timeout_seconds} min={3} max={600} onChange={(value) => update("probe_timeout_seconds", value)} /></Field>
            <Field className="probe-model-field" label="探测模型" hint="留空使用各上游默认"><Input value={values.probe_model} maxLength={200} onChange={(event) => update("probe_model", event.target.value)} /></Field>
          </div>
        </BulkAutomationSection>

        <BulkAutomationSection
          icon={<RefreshCw size={17} />}
          title="倍率采集"
          meta={applied.rateSync ? (values.rate_sync_enabled ? `每 ${values.rate_sync_interval_seconds} 秒` : "将停用") : rateSyncMixed ? "当前值不一致" : "当前值一致"}
          applied={applied.rateSync}
          onApply={(checked) => setSection("rateSync", checked)}
        >
          <div className="setting-inline"><span>启用倍率采集</span><Switch checked={values.rate_sync_enabled} onChange={(checked) => update("rate_sync_enabled", checked)} label="启用倍率采集" /></div>
          <div className="form-grid two">
            <Field label="同步间隔" hint="30 秒至 7 天"><NumberInput value={values.rate_sync_interval_seconds} min={30} max={604800} onChange={(value) => update("rate_sync_interval_seconds", value)} /></Field>
            <Field label="倍率源配置" hint="不会批量覆盖账号专属信息">
              <div className="source-summary"><Gauge size={16} /><span>地址、凭据、分组及换算倍率保持不变</span></div>
            </Field>
          </div>
        </BulkAutomationSection>
      </form>
    </Modal>
  );
}

function BulkAutomationSection({ icon, title, meta, applied, onApply, children }: {
  icon: React.ReactNode;
  title: string;
  meta: string;
  applied: boolean;
  onApply: (checked: boolean) => void;
  children: React.ReactNode;
}) {
  return (
    <section className={cx("automation-section", "bulk-automation-section", !applied && "bulk-automation-section-off")}>
      <header>
        <span className="automation-section-icon">{icon}</span>
        <div><h3>{title}</h3><span>{meta}</span></div>
        <label className="bulk-apply-toggle">
          <SelectionCheckbox checked={applied} label={`应用${title}设置`} onChange={onApply} />
          <span>应用</span>
        </label>
      </header>
      <fieldset disabled={!applied} className="bulk-section-fields">
        <div className="automation-section-body">{children}</div>
      </fieldset>
    </section>
  );
}

function AccountBalanceValue({ balance, requestError }: { balance?: AccountBalance; requestError: string }) {
  if (!balance) {
    return <div className="account-balance account-balance-state" role="cell" title={requestError}><span>{requestError ? "读取失败" : "暂无快照"}</span></div>;
  }
  const title = [balance.provider, balance.plan_name, balance.endpoint, balance.message, balance.checked_at ? formatDate(balance.checked_at) : ""].filter(Boolean).join(" · ");
  if (balance.status !== "ok") {
    const labels = { pending: "待刷新", unsupported: "不支持", invalid: "凭据无效", error: "查询失败" } as const;
    return <div className="account-balance account-balance-state" role="cell" title={title}><span>{labels[balance.status]}</span></div>;
  }
  const unit = balance.unit || "USD";
  const details = [
    balance.plan_name,
    balance.used != null ? `已用 ${formatBalanceNumber(balance.used)}` : "",
    balance.total != null ? `总 ${formatBalanceNumber(balance.total)}` : "",
  ].filter(Boolean).join(" · ");
  return (
    <div className="account-balance" role="cell" title={title}>
      <strong>{formatBalanceNumber(balance.remaining)} <small>{unit}</small></strong>
      <span><WalletCards size={12} aria-hidden="true" />{details || (balance.provider === "newapi" ? "NewAPI" : "Usage")}</span>
    </div>
  );
}

function formatBalanceNumber(value: number | null | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value)) return "-";
  if (Math.abs(value) >= 1000) return value.toFixed(0);
  if (Math.abs(value) >= 1) return value.toFixed(2).replace(/\.?0+$/, "");
  return value.toFixed(4).replace(/\.?0+$/, "");
}

function AutomationToggle({ icon, label, checked, disabled, onChange }: { icon: React.ReactNode; label: string; checked: boolean; disabled: boolean; onChange: (value: boolean) => void }) {
  return (
    <div className={cx("automation-toggle", checked && "automation-toggle-on")} title={label}>
      <span>{icon}<span>{label}</span></span>
      <Switch checked={checked} onChange={onChange} label={label} disabled={disabled} />
    </div>
  );
}

function HealthBadge({ state }: { state: HealthState }) {
  const values: Record<HealthState, { label: string; tone: "neutral" | "success" | "warning" | "danger" }> = {
    healthy: { label: "健康", tone: "success" },
    failing: { label: "异常", tone: "danger" },
    paused: { label: "已暂停", tone: "warning" },
    unknown: { label: "未检测", tone: "neutral" },
  };
  const current = values[state] ?? values.unknown;
  return <Badge tone={current.tone}>{current.label}</Badge>;
}

function AccountUptime({ account }: { account: Account }) {
  const successes = account.uptime_successes ?? 0;
  const total = account.uptime_total ?? 0;
  const windowSize = account.uptime_window_size ?? 60;
  const percent = formatPercent(account.uptime_percent);
  const range = account.uptime_window_started_at && account.uptime_window_ended_at
    ? `，${formatDate(account.uptime_window_started_at)} 至 ${formatDate(account.uptime_window_ended_at)}`
    : "";
  const description = total > 0
    ? `Uptime ${percent}，最近 ${total} 次探测中成功 ${successes} 次，窗口上限 ${windowSize} 次${range}`
    : `Uptime 暂无样本，统计窗口上限 ${windowSize} 次`;
  const results = [...(account.uptime_timeline ?? "")].reverse();
  const emptyCount = Math.max(0, windowSize - results.length);
  return (
    <div className="account-uptime" title={description} aria-label={description}>
      <div className="account-uptime-summary">
        <span>Uptime</span>
        <strong>{percent}</strong>
        <small>{successes}/{total}</small>
      </div>
      <div className="account-uptime-timeline" aria-hidden="true">
        {Array.from({ length: emptyCount }, (_, index) => <span className="uptime-empty" key={`empty-${index}`} />)}
        {results.map((result, index) => (
          <span
            className={result === "S" ? "uptime-success" : "uptime-failed"}
            title={result === "S" ? "成功" : "失败"}
            key={`${result}-${index}`}
          />
        ))}
      </div>
    </div>
  );
}

function failureReasonLabel(reason: FailureReason): string {
  return {
    AUTH: "认证异常",
    BALANCE: "余额不足",
    RATE_LIMIT: "请求限流",
    UPSTREAM: "上游异常",
    TIMEOUT: "探测超时",
    CONFIGURATION: "配置异常",
    UNKNOWN: "未知异常",
  }[reason];
}

function toggleLabel(key: ToggleKey): string {
  return { health_enabled: "账号测活", rate_sync_enabled: "倍率采集", priority_enabled: "全局排序", guard_enabled: "分组保护" }[key];
}

interface AccountSettingsModalProps {
  account: Account | null;
  onClose: () => void;
  onSaved: (account: Account) => void;
}

function AccountSettingsModal({ account, onClose, onSaved }: AccountSettingsModalProps) {
  const [values, setValues] = useState<AccountSettingsInput | null>(null);
  const [credential, setCredential] = useState("");
  const [clearCredential, setClearCredential] = useState(false);
  const [groups, setGroups] = useState<SourceGroup[]>([]);
	const [models, setModels] = useState<ProbeModel[]>([]);
  const [loadingGroups, setLoadingGroups] = useState(false);
  const [saving, setSaving] = useState(false);
  const { toast } = useToast();

  useEffect(() => {
    if (account) {
      setValues(settingsFromAccount(account));
    } else {
      setValues(null);
    }
    setCredential("");
    setClearCredential(false);
    setGroups([]);
  }, [account]);

	useEffect(() => {
		let active = true;
		setModels([]);
		if (account) {
			void api<ProbeModel[]>(`/accounts/${account.id}/models`)
				.then((result) => { if (active) setModels(result); })
				.catch(() => undefined);
		}
		return () => { active = false; };
	}, [account]);

  function update<K extends keyof AccountSettingsInput>(key: K, value: AccountSettingsInput[K]) {
    setValues((current) => current ? { ...current, [key]: value } : current);
  }

  function selectSourceType(sourceType: SourceType) {
    setValues((current) => {
      if (!current) return current;
      const sourceBaseURL = sourceType === "newapi" && !current.source_base_url
        ? account?.observed_source_base_url ?? null
        : current.source_base_url;
      return { ...current, source_type: sourceType, source_type_locked: true, source_base_url: sourceBaseURL };
    });
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!account || !values) return;
    if (values.rate_sync_enabled && values.source_type === "newapi") {
      if (!values.source_base_url || !values.source_group) {
        toast("开启 NewAPI 倍率采集前，请填写源站地址并拉取、绑定分组", "error");
        return;
      }
      if ((!account.source_credential_set && !credential.trim()) || clearCredential) {
        toast("开启 NewAPI 倍率采集前，请填写并保留源站凭据", "error");
        return;
      }
    }
    setSaving(true);
    try {
      const updated = await api<Account>(`/accounts/${account.id}/settings`, {
        method: "PUT",
        ...json({ ...values, source_credential: credential, clear_source_credential: clearCredential }),
      });
      onSaved(updated);
      toast("账号自动化设置已保存", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  async function loadGroups() {
    if (!account || !values) return;
    if (!values.source_base_url) {
      toast("请先填写 NewAPI 地址", "error");
      return;
    }
    setLoadingGroups(true);
    try {
      const sourceChanged = account.source_type !== "newapi" || values.source_base_url !== (effectiveSourceBaseURL(account) ?? "") || values.source_user_id !== (account.source_user_id ?? null);
      let result: SourceGroup[];
      if (credential.trim()) {
        result = await api<SourceGroup[]>(`/accounts/${account.id}/source-groups`, {
          method: "POST",
          ...json({
            source_base_url: values.source_base_url,
            source_credential: credential,
            source_user_id: values.source_user_id ?? "",
          }),
        });
      } else if (account.source_credential_set && !sourceChanged) {
        result = await api<SourceGroup[]>(`/accounts/${account.id}/source-groups`);
      } else {
        toast("预览新地址或用户 ID 时，需要重新填写访问凭据", "error");
        return;
      }
      setGroups(result);
      if (!result.some((group) => group.group === values.source_group)) update("source_group", null);
      toast(`读取到 ${result.length} 个源站分组`, "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setLoadingGroups(false);
    }
  }

  return (
    <Modal
      open={Boolean(account && values)}
      title="账号自动化"
      description={account ? `${account.name} · ${account.site_name} · #${account.remote_id}` : undefined}
      onClose={() => !saving && onClose()}
      width="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={saving}>取消</Button>
          <Button variant="primary" type="submit" form="account-settings-form" loading={saving}>保存设置</Button>
        </>
      }
    >
      {account && values ? (
        <form id="account-settings-form" className="settings-form" onSubmit={save}>
          <AutomationSection
            icon={<HeartPulse size={17} />}
            title="账号测活"
            meta={values.health_enabled ? `每 ${values.probe_interval_seconds} 秒` : "已停用"}
            checked={values.health_enabled}
            onChange={(checked) => update("health_enabled", checked)}
          >
            <div className="form-grid four">
              <Field label="探测间隔" hint="10 至 86400 秒"><NumberInput value={values.probe_interval_seconds} min={10} max={86400} onChange={(value) => update("probe_interval_seconds", value)} /></Field>
              <Field label="超时时间" hint="3 至 600 秒"><NumberInput value={values.probe_timeout_seconds} min={3} max={600} onChange={(value) => update("probe_timeout_seconds", value)} /></Field>
              <Field className="probe-model-field" label="探测模型" hint="OpenAI 默认 gpt-5.5；留空使用上游默认">
                <Combobox
                  value={values.probe_model ?? ""}
                  onChange={(model) => update("probe_model", model || null)}
                  options={models.map((model) => ({ value: model.id, label: model.display_name || model.id, description: model.display_name ? model.id : model.type }))}
                  label="探测模型"
                  placeholder={account.platform.trim().toLowerCase() === "openai" ? "gpt-5.5" : "选择或输入模型"}
                  maxLength={200}
                />
              </Field>
            </div>
          </AutomationSection>

          <AutomationSection
            icon={<RefreshCw size={17} />}
            title="倍率采集"
            meta={values.rate_sync_enabled ? `每 ${values.rate_sync_interval_seconds} 秒` : "已停用"}
            checked={values.rate_sync_enabled}
            onChange={(checked) => update("rate_sync_enabled", checked)}
          >
            <div className="form-grid two">
              <Field label="倍率源类型">
                <div className="segmented" role="group" aria-label="倍率源类型">
                  <button type="button" className={values.source_type === "sub2api" ? "active" : ""} onClick={() => selectSourceType("sub2api")}>Sub2API</button>
                  <button type="button" className={values.source_type === "newapi" ? "active" : ""} onClick={() => selectSourceType("newapi")}>NewAPI</button>
                </div>
              </Field>
              <Field label="同步间隔" hint="30 秒至 7 天"><NumberInput value={values.rate_sync_interval_seconds} min={30} max={604800} onChange={(value) => update("rate_sync_interval_seconds", value)} /></Field>
            </div>
            {values.source_type === "newapi" ? (
              <div className="newapi-fields">
                <div className="form-grid two">
                  <Field
                    label="NewAPI 地址"
                    hint={!account.source_base_url && account.observed_source_base_url && values.source_base_url === account.observed_source_base_url ? "上游账号同步值" : account.source_base_url ? "手工覆盖值" : undefined}
                    required
                  >
                    <Input type="url" value={values.source_base_url ?? ""} placeholder="https://api.example.com" onChange={(event) => update("source_base_url", event.target.value || null)} />
                  </Field>
                  <Field label="用户 ID" hint="可选，用于 New-Api-User 请求头"><Input value={values.source_user_id ?? ""} onChange={(event) => update("source_user_id", event.target.value || null)} placeholder="123" /></Field>
                  <Field label="访问凭据" hint={account.source_credential_set ? "已保存；预览新地址时请重新填写" : "支持 Token、Bearer 或 Cookie"} required={!account.source_credential_set}>
                    <Input type="password" value={credential} onChange={(event) => setCredential(event.target.value)} placeholder={account.source_credential_set ? "保持不变" : "输入访问凭据"} autoComplete="new-password" disabled={clearCredential} />
                  </Field>
                  <Field label="充值换算倍率" hint="本站倍率 = 源站倍率 / 此值"><NumberInput value={values.recharge_ratio} min={0.000001} max={1_000_000} step="any" onChange={(value) => update("recharge_ratio", value)} /></Field>
                </div>
                {account.source_credential_set ? (
                  <div className="setting-inline subtle-setting"><span>清除已保存的源站凭据</span><Switch checked={clearCredential} onChange={setClearCredential} label="清除源站凭据" /></div>
                ) : null}
                <div className="source-group-row">
                  <Field label="源站分组" hint="绑定 /api/user/self/groups data 中的实际键">
                    <SelectMenu
                      value={values.source_group ?? ""}
                      onChange={(selectedGroup) => update("source_group", selectedGroup || null)}
                      label="NewAPI 源站分组"
                      placeholder={groups.length ? "请选择分组" : "请先拉取分组"}
                      options={[
                        { value: "", label: groups.length ? "请选择分组" : "请先拉取分组" },
                        ...(values.source_group && !groups.some((group) => group.group === values.source_group)
                          ? [{ value: values.source_group, label: values.source_group, description: "当前绑定" }]
                          : []),
                        ...groups.map((group) => ({
                          value: group.group,
                          label: group.group,
                          description: `${group.description ? `${group.description} · ` : ""}${formatRate(group.rate)}`,
                        })),
                      ]}
                    />
                  </Field>
                  <Button size="sm" onClick={() => void loadGroups()} loading={loadingGroups} disabled={clearCredential}><RefreshCw size={14} />拉取分组</Button>
                </div>
              </div>
            ) : (
              <div className="source-summary"><Gauge size={16} /><span>通过本站 Sub2API 的账号账单探测接口解析实际倍率</span></div>
            )}
          </AutomationSection>
        </form>
      ) : null}
    </Modal>
  );
}

function AutomationSection({ icon, title, meta, checked, onChange, children }: { icon: React.ReactNode; title: string; meta: string; checked: boolean; onChange: (checked: boolean) => void; children: React.ReactNode }) {
  return (
    <section className={cx("automation-section", !checked && "automation-section-off")}>
      <header>
        <span className="automation-section-icon">{icon}</span>
        <div><h3>{title}</h3><span>{meta}</span></div>
        <Switch checked={checked} onChange={onChange} label={title} />
      </header>
      <div className="automation-section-body">{children}</div>
    </section>
  );
}

function NumberInput({ value, onChange, min, max, step }: { value: number; onChange: (value: number) => void; min: number; max: number; step?: string }) {
  return <Input type="number" value={value} min={min} max={max} step={step} onChange={(event) => onChange(Number(event.target.value))} />;
}
