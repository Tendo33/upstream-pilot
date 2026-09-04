import {
  ArrowDownUp,
  CircleAlert,
  CloudCog,
  KeyRound,
  Pencil,
  Percent,
  Plus,
  RefreshCw,
  Server,
  Trash2,
  Unplug,
  Zap,
} from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, json } from "../api";
import { formatDate } from "../lib";
import type { Site, SiteInput } from "../types";
import {
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Modal,
  PageHeader,
  PageLoader,
  Switch,
  useToast,
} from "../components/ui";

const newSite: SiteInput = {
  name: "",
  base_url: "",
  api_key: "",
  enabled: true,
  inventory_interval_seconds: 300,
  priority_start: 1,
  priority_step: 1,
  reconcile_interval_seconds: 60,
  cache_rate_priority_enabled: false,
  cache_rate_window_seconds: 3600,
  rate_priority_weight: 1,
  cache_rate_priority_weight: 1,
};

function fromSite(site: Site): SiteInput {
  return {
    name: site.name,
    base_url: site.base_url,
    api_key: "",
    enabled: site.enabled,
    inventory_interval_seconds: site.inventory_interval_seconds,
    priority_start: site.priority_start,
    priority_step: site.priority_step,
    reconcile_interval_seconds: site.reconcile_interval_seconds,
    cache_rate_priority_enabled: site.cache_rate_priority_enabled,
    cache_rate_window_seconds: site.cache_rate_window_seconds,
    rate_priority_weight: site.rate_priority_weight,
    cache_rate_priority_weight: site.cache_rate_priority_weight,
  };
}

export function SitesPage() {
  const [sites, setSites] = useState<Site[] | null>(null);
  const [error, setError] = useState("");
  const [editor, setEditor] = useState<{ site: Site | null; values: SiteInput } | null>(null);
  const [deleting, setDeleting] = useState<Site | null>(null);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");
  const { toast } = useToast();

  const load = useCallback(async () => {
    setError("");
    try {
      setSites(await api<Site[]>("/sites"));
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  async function saveSite(event: FormEvent) {
    event.preventDefault();
    if (!editor) return;
    const values = editor.values;
    if (!values.name.trim() || !values.base_url.trim() || (!editor.site && !values.api_key.trim())) {
      toast("请填写名称、地址和管理 API Key", "error");
      return;
    }
    setSaving(true);
    try {
      const saved = editor.site
        ? await api<Site>(`/sites/${editor.site.id}/`, { method: "PATCH", ...json(values) })
        : await api<Site>("/sites", { method: "POST", ...json(values) });
      setSites((current) => {
        if (!current) return [saved];
        return editor.site ? current.map((item) => item.id === saved.id ? saved : item) : [...current, saved];
      });
      setEditor(null);
      toast(editor.site ? "站点设置已保存" : "站点已添加，库存同步已开始", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  async function runAction(site: Site, action: "test" | "sync" | "reconcile" | "cache-sample") {
    setBusy(`${site.id}:${action}`);
    try {
      const result = await api<Site | { healthy: boolean; groups: number; version?: string }>(`/sites/${site.id}/${action}`, { method: "POST" });
      if (action === "test") {
        const test = result as { groups: number; version?: string };
        toast(`连接正常，读取到 ${test.groups} 个分组${test.version ? ` · ${test.version}` : ""}`, "success");
        await load();
      } else if (action === "sync") {
        const updated = result as Site;
        setSites((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? null);
        toast("账号与分组库存已同步", "success");
      } else if (action === "cache-sample") {
        const updated = result as Site;
        setSites((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? null);
        toast("缓存率采样完成", "success");
      } else {
        const reconciled = result as unknown as { evaluated: number; changed: number; failed: number };
        toast(`排序完成：检查 ${reconciled.evaluated} 个，更新 ${reconciled.changed} 个${reconciled.failed ? `，失败 ${reconciled.failed} 个` : ""}`, reconciled.failed ? "info" : "success");
        await load();
      }
    } catch (cause) {
      toast(errorMessage(cause), "error");
      await load();
    } finally {
      setBusy("");
    }
  }

  async function toggleSite(site: Site, enabled: boolean) {
    setBusy(`${site.id}:toggle`);
    try {
      const updated = await api<Site>(`/sites/${site.id}/`, { method: "PATCH", ...json({ ...fromSite(site), enabled }) });
      setSites((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? null);
      toast(enabled ? "站点自动化已启用" : "站点自动化已停用", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setBusy("");
    }
  }

  async function deleteSite() {
    if (!deleting) return;
    setSaving(true);
    try {
      await api<void>(`/sites/${deleting.id}/`, { method: "DELETE" });
      setSites((current) => current?.filter((site) => site.id !== deleting.id) ?? null);
      setDeleting(null);
      toast("站点及其本地库存已删除", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  if (!sites && !error) return <PageLoader />;
  if (!sites) return <ErrorState message={error} retry={() => void load()} />;

  return (
    <div className="page">
      <PageHeader
        eyebrow="Infrastructure"
        title="站点"
        description="管理当前用户拥有的 Sub2API 实例与库存同步策略"
        actions={<Button variant="primary" onClick={() => setEditor({ site: null, values: { ...newSite } })}><Plus size={16} />添加站点</Button>}
      />

      {error ? <div className="inline-alert"><CircleAlert size={16} /><span>{error}</span><Button size="sm" onClick={() => void load()}>重试</Button></div> : null}

      {sites.length === 0 ? (
        <section className="panel">
          <EmptyState
            title="还没有站点"
            description="添加 Sub2API 管理地址后即可同步账号与分组。"
            action={<Button variant="primary" onClick={() => setEditor({ site: null, values: { ...newSite } })}><Plus size={16} />添加站点</Button>}
            icon={<Server size={21} />}
          />
        </section>
      ) : (
        <div className="site-list">
          {sites.map((site) => {
            const state = connectionState(site.connection_state);
            return (
              <article className="site-row" key={site.id}>
                <div className="site-main">
                  <div className="site-icon"><Server size={19} /></div>
                  <div className="site-title">
                    <div><h2>{site.name}</h2><Badge tone={state.tone}>{state.label}</Badge>{!site.enabled ? <Badge>已停用</Badge> : null}</div>
                    <span title={site.base_url}>{site.base_url}</span>
                  </div>
                </div>
                <div className="site-stats">
                  <div><strong>{site.account_count}</strong><span>账号</span></div>
                  <div><strong>{site.enabled_automation_count}</strong><span>自动化</span></div>
                  <div><strong>{site.version_hint || "-"}</strong><span>版本</span></div>
                </div>
                <div className="site-timestamps">
                  <span>库存同步 <time>{formatDate(site.last_inventory_at)}</time></span>
                  <span>优先级排序 <time>{formatDate(site.last_reconcile_at)}</time></span>
                  {site.cache_rate_priority_enabled ? <span>缓存率采样 <time>{formatDate(site.last_cache_sample_at)}</time></span> : null}
                </div>
                <div className="site-toggle">
                  <Switch checked={site.enabled} onChange={(enabled) => void toggleSite(site, enabled)} label={`${site.enabled ? "停用" : "启用"}${site.name}`} disabled={busy.startsWith(site.id)} />
                </div>
                <div className="site-actions">
                  <IconButton label="测试连接" onClick={() => void runAction(site, "test")} disabled={Boolean(busy)}>
                    <Zap size={16} className={busy === `${site.id}:test` ? "spin" : undefined} />
                  </IconButton>
                  <IconButton label="同步库存" onClick={() => void runAction(site, "sync")} disabled={Boolean(busy)}>
                    <RefreshCw size={16} className={busy === `${site.id}:sync` ? "spin" : undefined} />
                  </IconButton>
                  <IconButton label="执行优先级排序" onClick={() => void runAction(site, "reconcile")} disabled={Boolean(busy)}>
                    <ArrowDownUp size={16} className={busy === `${site.id}:reconcile` ? "spin" : undefined} />
                  </IconButton>
                  <IconButton label="采样缓存率" onClick={() => void runAction(site, "cache-sample")} disabled={Boolean(busy)}>
                    <Percent size={16} className={busy === `${site.id}:cache-sample` ? "spin" : undefined} />
                  </IconButton>
                  <IconButton label="编辑站点" onClick={() => setEditor({ site, values: fromSite(site) })}><Pencil size={16} /></IconButton>
                  <IconButton label="删除站点" danger onClick={() => setDeleting(site)}><Trash2 size={16} /></IconButton>
                </div>
                {site.last_error ? <div className="site-error"><Unplug size={14} /><span title={site.last_error}>{site.last_error}</span></div> : null}
              </article>
            );
          })}
        </div>
      )}

      <Modal
        open={Boolean(editor)}
        title={editor?.site ? "编辑站点" : "添加站点"}
        description={editor?.site ? "留空 API Key 可保留原密钥" : "连接一个 Sub2API 管理端"}
        onClose={() => !saving && setEditor(null)}
        footer={
          <>
            <Button onClick={() => setEditor(null)} disabled={saving}>取消</Button>
            <Button variant="primary" type="submit" form="site-form" loading={saving}>{editor?.site ? "保存" : "添加站点"}</Button>
          </>
        }
      >
        {editor ? (
          <form id="site-form" className="form-stack" onSubmit={saveSite}>
            <div className="form-grid two">
              <Field label="站点名称" required>
                <Input value={editor.values.name} onChange={(event) => setEditor({ ...editor, values: { ...editor.values, name: event.target.value } })} maxLength={80} placeholder="生产站点" autoFocus />
              </Field>
              <Field label="状态">
                <div className="setting-inline"><span>{editor.values.enabled ? "启用自动任务" : "停止自动任务"}</span><Switch checked={editor.values.enabled} onChange={(enabled) => setEditor({ ...editor, values: { ...editor.values, enabled } })} label="启用站点" /></div>
              </Field>
            </div>
            <Field label="Sub2API 地址" hint="例如 https://sub.example.com，不要填写 API 路径" required>
              <Input type="url" value={editor.values.base_url} onChange={(event) => setEditor({ ...editor, values: { ...editor.values, base_url: event.target.value } })} placeholder="https://sub.example.com" />
            </Field>
            <Field label="管理 API Key" hint={editor.site ? "仅在需要轮换密钥时填写" : "密钥将使用主密钥加密后存储"} required={!editor.site}>
              <div className="input-prefix"><KeyRound size={15} /><Input type="password" value={editor.values.api_key} onChange={(event) => setEditor({ ...editor, values: { ...editor.values, api_key: event.target.value } })} placeholder={editor.site ? "保持不变" : "sk-..."} autoComplete="new-password" /></div>
            </Field>
            <div className="form-divider"><CloudCog size={15} /><span>调度参数</span></div>
            <div className="form-grid two">
              <Field label="库存同步间隔" hint="30 至 86400 秒">
                <NumberInput value={editor.values.inventory_interval_seconds} min={30} max={86400} onChange={(value) => setEditor({ ...editor, values: { ...editor.values, inventory_interval_seconds: value } })} />
              </Field>
              <Field label="优先级排序间隔" hint="10 至 86400 秒">
                <NumberInput value={editor.values.reconcile_interval_seconds} min={10} max={86400} onChange={(value) => setEditor({ ...editor, values: { ...editor.values, reconcile_interval_seconds: value } })} />
              </Field>
              <Field label="优先级起始值" hint="倍率最低的账号从此值开始">
                <NumberInput value={editor.values.priority_start} min={0} max={1_000_000} onChange={(value) => setEditor({ ...editor, values: { ...editor.values, priority_start: value } })} />
              </Field>
              <Field label="优先级步长" hint="数值越高，Sub2API 优先级越低">
                <NumberInput value={editor.values.priority_step} min={1} max={100_000} onChange={(value) => setEditor({ ...editor, values: { ...editor.values, priority_step: value } })} />
              </Field>
            </div>
            <div className="form-divider"><Percent size={15} /><span>缓存率排序</span></div>
            <div className="setting-inline"><span>按近期缓存率影响优先级</span><Switch checked={editor.values.cache_rate_priority_enabled} onChange={(checked) => setEditor({ ...editor, values: { ...editor.values, cache_rate_priority_enabled: checked } })} label="启用缓存率排序" /></div>
            <Field label="统计窗口" hint="300 至 86400 秒，可直接选择 30 分钟或 1 小时">
              <div className="segmented" role="group" aria-label="缓存率统计窗口">
                <button type="button" className={editor.values.cache_rate_window_seconds === 1800 ? "active" : ""} onClick={() => setEditor({ ...editor, values: { ...editor.values, cache_rate_window_seconds: 1800 } })}>30 分钟</button>
                <button type="button" className={editor.values.cache_rate_window_seconds === 3600 ? "active" : ""} onClick={() => setEditor({ ...editor, values: { ...editor.values, cache_rate_window_seconds: 3600 } })}>1 小时</button>
                <button type="button" className={editor.values.cache_rate_window_seconds !== 1800 && editor.values.cache_rate_window_seconds !== 3600 ? "active" : ""} onClick={() => setEditor({ ...editor, values: { ...editor.values, cache_rate_window_seconds: editor.values.cache_rate_window_seconds === 1800 || editor.values.cache_rate_window_seconds === 3600 ? 7200 : editor.values.cache_rate_window_seconds } })}>自定义</button>
              </div>
            </Field>
            {editor.values.cache_rate_window_seconds !== 1800 && editor.values.cache_rate_window_seconds !== 3600 ? (
              <Field label="自定义窗口" hint="秒">
                <NumberInput value={editor.values.cache_rate_window_seconds} min={300} max={86400} onChange={(value) => setEditor({ ...editor, values: { ...editor.values, cache_rate_window_seconds: value } })} />
              </Field>
            ) : null}
            <div className="form-grid two">
              <Field label="倍率权重" hint="0 至 100，越大越偏向低倍率">
                <NumberInput value={editor.values.rate_priority_weight} min={0} max={100} step="any" onChange={(value) => setEditor({ ...editor, values: { ...editor.values, rate_priority_weight: value } })} />
              </Field>
              <Field label="缓存率权重" hint="0 至 100，越大越偏向高缓存率">
                <NumberInput value={editor.values.cache_rate_priority_weight} min={0} max={100} step="any" onChange={(value) => setEditor({ ...editor, values: { ...editor.values, cache_rate_priority_weight: value } })} />
              </Field>
            </div>
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={Boolean(deleting)}
        title="删除站点"
        description={`确定删除“${deleting?.name ?? ""}”吗？本地账号库存和配置也会被删除，上游 Sub2API 数据不会受到影响。`}
        confirmLabel="删除站点"
        danger
        loading={saving}
        onConfirm={() => void deleteSite()}
        onClose={() => setDeleting(null)}
      />
    </div>
  );
}

function NumberInput({ value, onChange, min, max, step }: { value: number; onChange: (value: number) => void; min: number; max: number; step?: string }) {
  return <Input type="number" value={value} min={min} max={max} step={step} onChange={(event) => onChange(Number(event.target.value))} />;
}

function connectionState(state: string): { label: string; tone: "neutral" | "success" | "warning" | "danger" } {
  if (state === "healthy") return { label: "连接正常", tone: "success" };
  if (state === "auth_error") return { label: "认证失败", tone: "danger" };
  if (state === "unreachable") return { label: "无法连接", tone: "danger" };
  return { label: "等待检测", tone: "neutral" };
}
