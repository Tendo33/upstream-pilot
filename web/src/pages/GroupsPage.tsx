import {
  Check,
  CircleAlert,
  Gauge,
  Layers3,
  Link2,
  Pencil,
  Play,
  RefreshCw,
  Search,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { api, errorMessage, json } from "../api";
import { Badge, Button, EmptyState, ErrorState, Field, IconButton, Input, Modal, PageHeader, PageLoader, SelectMenu, Switch, cx, useToast } from "../components/ui";
import { formatDate, formatRate } from "../lib";
import type { Account, GroupRateMode, ManagedGroup, Site } from "../types";

interface ApplyGroupResponse {
  rate_multiplier: number;
  group: ManagedGroup;
}

export function GroupsPage() {
  const [groups, setGroups] = useState<ManagedGroup[] | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [sites, setSites] = useState<Site[]>([]);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [siteID, setSiteID] = useState("");
  const [editing, setEditing] = useState<ManagedGroup | null>(null);
  const [busy, setBusy] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const { toast } = useToast();

  const load = useCallback(async (quiet = false) => {
    setError("");
    setRefreshing(quiet);
    try {
      const [groupItems, accountItems, siteItems] = await Promise.all([
        api<ManagedGroup[]>("/groups"),
        api<Account[]>("/accounts"),
        api<Site[]>("/sites"),
      ]);
      setGroups(groupItems);
      setAccounts(accountItems);
      setSites(siteItems);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    return (groups ?? []).filter((group) => {
      if (siteID && group.site_id !== siteID) return false;
      if (!query) return true;
      return [group.name, group.site_name, group.platform ?? "", String(group.remote_id)]
        .some((value) => value.toLowerCase().includes(query));
    });
  }, [groups, search, siteID]);

  function replaceGroup(updated: ManagedGroup) {
    setGroups((current) => current?.map((group) => group.id === updated.id ? updated : group) ?? null);
    setEditing((current) => current?.id === updated.id ? updated : current);
  }

  async function applyRule(group: ManagedGroup) {
    setBusy(group.id);
    try {
      const result = await api<ApplyGroupResponse>(`/groups/${group.id}/apply`, { method: "POST" });
      replaceGroup(result.group);
      toast(`已写回分组倍率 ${formatRate(result.rate_multiplier)}`, "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
      await load(true);
    } finally {
      setBusy("");
    }
  }

  if (!groups && !error) return <PageLoader />;
  if (!groups) return <ErrorState message={error} retry={() => void load()} />;

  return (
    <div className="page groups-page">
      <PageHeader
        eyebrow="Rates"
        title="分组"
        description="管理 Sub2API 分组倍率与绑定来源"
        actions={<IconButton label="刷新分组" onClick={() => void load(true)} disabled={refreshing}><RefreshCw size={17} className={refreshing ? "spin" : undefined} /></IconButton>}
      />

      <div className="filter-bar">
        <div className="search-box">
          <Search size={16} />
          <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索分组、平台或远程 ID" aria-label="搜索分组" />
          {search ? <IconButton label="清空搜索" onClick={() => setSearch("")}><X size={15} /></IconButton> : null}
        </div>
        <SelectMenu
          className="select-control-filter"
          label="站点"
          icon={<Layers3 size={15} />}
          value={siteID}
          onChange={setSiteID}
          options={[{ value: "", label: "全部站点" }, ...sites.map((site) => ({ value: site.id, label: site.name }))]}
        />
        <span className="result-count">{visible.length} 个分组</span>
      </div>

      {error ? <div className="inline-alert"><CircleAlert size={16} /><span>{error}</span></div> : null}

      {visible.length === 0 ? (
        <section className="panel"><EmptyState title="暂无匹配分组" description={groups.length ? "调整筛选条件后重试。" : "在站点页同步库存后显示分组。"} icon={<Layers3 size={21} />} /></section>
      ) : (
        <div className="groups-table" role="table" aria-label="分组倍率列表">
          <div className="groups-head" role="row"><span>分组</span><span>当前倍率</span><span>绑定来源</span><span>规则</span><span>最近应用</span><span className="sr-only">操作</span></div>
          {visible.map((group) => (
            <article className="group-row" role="row" key={group.id}>
              <div className="group-identity" role="cell">
                <div><strong title={group.name}>{group.name}</strong>{group.platform ? <Badge>{group.platform}</Badge> : null}</div>
                <span>{group.site_name} · #{group.remote_id} · {group.member_count} 个账号</span>
              </div>
              <div className="group-rate" role="cell"><strong>{formatRate(group.rate_multiplier)}</strong><span>{group.status || "状态未知"}</span></div>
              <div className="group-bindings" role="cell">
                {group.bindings.length ? group.bindings.slice(0, 2).map((binding) => (
                  <Badge tone={binding.available && binding.rate_multiplier != null ? "info" : "warning"} key={binding.id} title={`${binding.site_name} · ${binding.account_name}`}>
                    {binding.account_name} · {formatRate(binding.rate_multiplier)}
                  </Badge>
                )) : <span>未绑定</span>}
                {group.bindings.length > 2 ? <Badge>+{group.bindings.length - 2}</Badge> : null}
              </div>
              <div className="group-rule" role="cell">
                <Badge tone={group.rule.enabled ? "success" : "neutral"}>{group.rule.enabled ? "已启用" : "已停用"}</Badge>
                <span>{ruleSummary(group)}</span>
              </div>
              <div className="group-applied" role="cell">
                <span>{formatDate(group.rule.last_applied_at)}</span>
                {group.rule.last_calculated_rate != null ? <strong>{formatRate(group.rule.last_calculated_rate)}</strong> : null}
              </div>
              <div className="group-actions" role="cell">
                <IconButton label="立即应用倍率规则" onClick={() => void applyRule(group)} disabled={Boolean(busy) || !group.rule.enabled || !group.bindings.length}>
                  <Play size={16} className={busy === group.id ? "spin" : undefined} />
                </IconButton>
                <IconButton label="编辑分组倍率规则" onClick={() => setEditing(group)}><Pencil size={16} /></IconButton>
              </div>
              {group.rule.last_error ? <div className="group-error"><CircleAlert size={14} /><span title={group.rule.last_error}>{group.rule.last_error}</span></div> : null}
            </article>
          ))}
        </div>
      )}

      <GroupRateEditor
        group={editing}
        accounts={accounts}
        onClose={() => setEditing(null)}
        onSaved={(updated) => { replaceGroup(updated); setEditing(null); }}
      />
    </div>
  );
}

function ruleSummary(group: ManagedGroup): string {
  const labels: Record<GroupRateMode, string> = { first: "首个源倍率", average: "平均源倍率", min: "最低源倍率", max: "最高源倍率", custom: "自定义公式" };
  const base = labels[group.rule.mode] ?? group.rule.mode;
  if (!group.rule.offset) return base;
  return `${base} ${group.rule.offset > 0 ? "+" : ""}${formatRate(group.rule.offset)}`;
}

interface GroupRateEditorProps {
  group: ManagedGroup | null;
  accounts: Account[];
  onClose: () => void;
  onSaved: (group: ManagedGroup) => void;
}

function GroupRateEditor({ group, accounts, onClose, onSaved }: GroupRateEditorProps) {
  const [enabled, setEnabled] = useState(false);
  const [mode, setMode] = useState<GroupRateMode>("first");
  const [offset, setOffset] = useState("0");
  const [expression, setExpression] = useState("");
  const [bindings, setBindings] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [saving, setSaving] = useState(false);
  const { toast } = useToast();

  useEffect(() => {
    if (!group) return;
    setEnabled(group.rule.enabled);
    setMode(group.rule.mode);
    setOffset(String(group.rule.offset ?? 0));
    setExpression(group.rule.expression ?? "");
    setBindings(group.bindings.map((binding) => binding.account_id));
    setSearch("");
  }, [group]);

  const sources = useMemo(() => {
    const query = search.trim().toLowerCase();
    return accounts.filter((account) => !query || [account.name, account.site_name, account.platform, String(account.remote_id)]
      .some((value) => value.toLowerCase().includes(query)));
  }, [accounts, search]);

  function toggle(accountID: string) {
    setBindings((current) => current.includes(accountID) ? current.filter((id) => id !== accountID) : [...current, accountID]);
  }

  async function save(event?: FormEvent, apply = false) {
    event?.preventDefault();
    if (!group) return;
    const numericOffset = Number(offset);
    if (!Number.isFinite(numericOffset)) {
      toast("倍率偏移必须是有效数字", "error");
      return;
    }
    if (enabled && bindings.length === 0) {
      toast("启用规则前至少绑定一个账号倍率", "error");
      return;
    }
    if (enabled && mode === "custom" && !expression.trim()) {
      toast("请输入自定义公式", "error");
      return;
    }
    setSaving(true);
    try {
      const updated = await api<ManagedGroup>(`/groups/${group.id}/config`, {
        method: "PUT",
        ...json({ enabled, mode, offset: numericOffset, expression: expression.trim() || null, bindings, apply }),
      });
      onSaved(updated);
      toast(apply && enabled ? `规则已保存并应用，当前倍率 ${formatRate(updated.rate_multiplier)}` : "分组倍率规则已保存", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={Boolean(group)}
      title="分组倍率规则"
      description={group ? `${group.name} · ${group.site_name} · 当前 ${formatRate(group.rate_multiplier)}` : undefined}
      onClose={() => !saving && onClose()}
      width="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={saving}>取消</Button>
          <Button onClick={() => void save(undefined, false)} loading={saving}>保存</Button>
          <Button variant="primary" onClick={() => void save(undefined, true)} loading={saving} disabled={!enabled}>保存并应用</Button>
        </>
      }
    >
      {group ? (
        <form className="group-editor" onSubmit={(event) => void save(event, false)}>
          <div className="setting-inline"><span>跟踪绑定账号倍率</span><Switch checked={enabled} onChange={setEnabled} label="启用分组倍率规则" /></div>
          <div className="form-grid two">
            <Field label="计算规则">
              <SelectMenu
                value={mode}
                onChange={(value) => setMode(value as GroupRateMode)}
                label="计算规则"
                icon={<Gauge size={15} />}
                options={[
                  { value: "first", label: "首个源倍率" },
                  { value: "average", label: "平均源倍率" },
                  { value: "min", label: "最低源倍率" },
                  { value: "max", label: "最高源倍率" },
                  { value: "custom", label: "自定义公式" },
                ]}
              />
            </Field>
            <Field label="倍率偏移"><Input type="number" step="any" min={-100000} max={100000} value={offset} onChange={(event) => setOffset(event.target.value)} /></Field>
          </div>
          {mode === "custom" ? <Field label="自定义公式"><Input value={expression} maxLength={500} onChange={(event) => setExpression(event.target.value)} placeholder="round(avg * 1.08, 4)" /></Field> : null}

          <section className="binding-picker">
            <header><div><Link2 size={16} /><strong>绑定账号倍率</strong></div><Badge tone={bindings.length ? "info" : "neutral"}>{bindings.length} 个</Badge></header>
            <div className="binding-search"><Search size={15} /><Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索账号或站点" aria-label="搜索倍率来源账号" /></div>
            <div className="binding-list">
              {sources.length ? sources.map((account) => {
                const selected = bindings.includes(account.id);
                return (
                  <button type="button" className={cx("binding-option", selected && "binding-option-selected")} role="checkbox" aria-checked={selected} onClick={() => toggle(account.id)} key={account.id}>
                    <span className="binding-check">{selected ? <Check size={13} /> : null}</span>
                    <span className="binding-copy"><strong>{account.name}</strong><small>{account.site_name} · {account.platform || account.account_type || "未知平台"}</small></span>
                    <span className="binding-rate">{formatRate(account.rate_multiplier)}</span>
                  </button>
                );
              }) : <div className="binding-empty">没有匹配账号</div>}
            </div>
          </section>
        </form>
      ) : null}
    </Modal>
  );
}
