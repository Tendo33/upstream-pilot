import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { ActionEffects } from "../components/ActionEffects";
import { api, errorMessage, json } from "../api";
import { Badge, Button, EmptyState, Field, Input, PageHeader, PageLoader, useToast } from "../components/ui";
import { formatDate } from "../lib";

type Provider = "feishu" | "wecom" | "webhook" | "auto";
type Channel = {id: string; name: string; provider: Provider; enabled: boolean; categories: string[]; revision: number; webhook_configured: boolean; signing_secret_configured: boolean; legacy_source: string | null};
type Rules = {price_rise_percent: number; price_cooldown_seconds: number; balance_enabled: boolean; balance_threshold: number; balance_cooldown_seconds: number};
type Delivery = {id: string; channel_name: string; provider: Provider; status: string; attempts: number; last_error: string; delivered_at: string | null};
type Message = {id: string; category: string; severity: string; kind: string; message: string; created_at: string; context: {groups?: string[]; site_name?: string; legacy?: boolean}; deliveries: Delivery[]};
type Data = {channels: Channel[]; rules: Rules; events: Message[]};
type Incident = {account_id: string; account_name: string; channel: string; active: boolean; current_source: boolean; episode: number; message: string; opened_at: string | null; resolved_at: string | null};
const categories: Record<string, string> = {quality: "上游质量", price: "采购价格", balance: "余额预警", collector: "采集故障", controller: "控制状态", runway: "余额续航"};
const providers = {feishu: "飞书 / Lark", wecom: "企业微信", webhook: "通用 Webhook", auto: "原渠道自动识别"};
const deliveryStates: Record<string, string> = {pending: "待投递", sending: "发送中", delivered: "已送达", failed: "投递失败", cancelled: "已取消", expired: "已过期"};
const severityNames: Record<string, string> = {warning: "提醒", critical: "紧急", recovery: "恢复", info: "信息"};
const incidentNames: Record<string, string> = {controller: "控制写回", pending_control: "待确认控制", collector_probe: "主动探测", collector_traffic: "真实请求", collector_rate: "采购倍率", collector_balance: "余额采集", balance_runway: "余额续航"};

function deliverySummary(items: Delivery[]) {
 const delivered = items.filter(d => d.status === "delivered").length;
 const counts = `${delivered}/${items.length} 个渠道已送达`;
 if (delivered === items.length) return counts;
 if (items.some(d => d.status === "failed")) return `投递失败 · ${counts}`;
 if (items.some(d => d.status === "sending")) return `发送中 · ${counts}`;
 if (items.some(d => d.status === "pending")) return `${items.some(d => d.last_error) ? "等待重试" : "待投递"} · ${counts}`;
 if (items.every(d => d.status === "expired")) return "已过期 · 停止自动投递";
 return `投递已停止 · ${counts}`;
}

export function MessageCenterPage() {
 const [data, setData] = useState<Data | null>(null), [error, setError] = useState("");
 const [editor, setEditor] = useState<{channel: Channel | null} | null>(null), [editingRules, setEditingRules] = useState(false);
 const [filter, setFilter] = useState("all"), [busy, setBusy] = useState("");
 const request = useRef<AbortController | null>(null); const {toast} = useToast();
 const load = useCallback(async () => {
  request.current?.abort(); const controller = new AbortController(); request.current = controller;
  try {const result = await api<Data>("/notifications", {signal: controller.signal}); if (!controller.signal.aborted) {setData(result); setError("");}}
  catch (e) {if (!controller.signal.aborted) setError(errorMessage(e));}
 }, []);
 useEffect(() => {void load(); return () => request.current?.abort();}, [load]);
 async function test(channel: Channel) {
  setBusy(channel.id);
  try {const result = await api<{status: string; message: string}>(`/notifications/channels/${channel.id}/test`, {method: "POST"}); toast(result.status === "delivered" ? "机器人已确认接收测试消息" : result.message || "测试消息已入队，请查看投递结果", result.status === "delivered" ? "success" : result.message ? "error" : "info"); await load();}
  catch (e) {setError(errorMessage(e));} finally {setBusy("");}
 }
 async function retry(id: string) {
  setBusy(id); try {await api(`/notifications/deliveries/${id}/retry`, {method: "POST"}); toast("该渠道的消息已重新入队", "success"); await load();}
  catch (e) {setError(errorMessage(e));} finally {setBusy("");}
 }
 return <div className="page message-center">
  <PageHeader title="消息中心" description="在一个地方接收上游风险、价格变化和恢复通知。观察模式也可以报警。" actions={<div className="quality-actions"><Button onClick={() => void load()}>刷新</Button><Button variant="primary" disabled={!data || data.channels.length >= 8} onClick={() => {setEditor({channel: null}); setEditingRules(false);}}>添加渠道</Button></div>}/>
  {error && <p className="quality-error" role="alert">{error}</p>}
  {!data && !error ? <PageLoader/> : data && <>
   <section className="message-section" aria-labelledby="message-channels"><h2 id="message-channels">接收渠道</h2>
    {data.channels.length === 0 && !editor ? <EmptyState title="还没有通知渠道" description="添加飞书、企业微信或通用 Webhook；没有接收渠道时，事件仍会记录在这里。"/> : data.channels.map(channel => <div className="message-channel" key={channel.id}>
     <div><strong>{channel.name}</strong><p className="quality-note">{providers[channel.provider]}{channel.signing_secret_configured ? " · 已配置签名" : ""} · {channel.categories.map(c => categories[c] ?? c).join("、")}</p></div>
     <div className="quality-actions"><Badge tone={channel.enabled ? "success" : "neutral"}>{channel.enabled ? "接收新事件" : "已停用"}</Badge><Button disabled={!!busy} onClick={() => {setEditor({channel}); setEditingRules(false);}}>编辑</Button><Button disabled={!!busy || !channel.webhook_configured} loading={busy === channel.id} onClick={() => void test(channel)}>测试连接</Button></div>
    </div>)}
    <p className="quality-note">测试连接会向选中的渠道发送一条消息。停用渠道不影响事件记录；启用后只接收新事件。</p>
   </section>
   {editor && <ChannelEditor key={editor.channel?.id ?? "new"} channel={editor.channel} close={() => setEditor(null)} saved={() => {setEditor(null); void load();}}/>}
   <section className="message-section"><div className="quality-section-heading"><h2>通知规则</h2><Button onClick={() => {setEditingRules(v => !v); setEditor(null);}}>{editingRules ? "关闭规则" : "编辑规则"}</Button></div>
    <p>采购倍率上涨 {data.rules.price_rise_percent}% 起通知，同一账号每 {Math.round(data.rules.price_cooldown_seconds / 60)} 分钟最多一次。{data.rules.balance_enabled ? `余额低于 ${data.rules.balance_threshold} 时预警，持续低余额每 ${Math.round(data.rules.balance_cooldown_seconds / 60)} 分钟提醒一次。` : "全局低余额预警尚未启用。"}</p>
    <p className="quality-note">持续故障由质量策略判断，同一状态不反复通知。余额恢复只使用同来源、同单位的新鲜读数；采集未知不代表恢复。</p>
    {editingRules && <RulesEditor rules={data.rules} saved={() => {setEditingRules(false); void load();}}/>}
   </section>
   <section className="message-section"><div className="quality-section-heading"><h2>最近消息</h2><label className="message-filter">通知类型 <select value={filter} onChange={e => setFilter(e.target.value)}><option value="all">全部</option>{Object.entries(categories).map(([key, name]) => <option value={key} key={key}>{name}</option>)}<option value="test">连接测试</option></select></label></div>
    <p className="quality-note">显示最近 100 条。投递失败最多尝试 5 次；超过一小时的消息停止自动发送，避免恢复连接后刷屏。</p>
    {data.events.filter(e => filter === "all" || e.category === filter).map(event => <article className="message-event" key={event.id}>
     <div className="message-event-heading"><strong>{categories[event.category] ?? "连接测试"}</strong><Badge tone={event.severity === "recovery" ? "success" : event.severity === "critical" ? "danger" : "neutral"}>{severityNames[event.severity] ?? event.severity}</Badge><time>{formatDate(event.created_at)}</time>{event.context.legacy && <span className="quality-note">迁移记录</span>}</div>
     <p className="message-body">{event.message}</p>
     {(event.context.site_name || event.context.groups?.length) && <p className="quality-note">{event.context.site_name}{event.context.groups?.length ? ` · 影响分组：${event.context.groups.join("、")}` : ""}</p>}
     {event.deliveries.length === 0 ? <span className="quality-note">仅记录 · 发生时没有启用的订阅渠道</span> : <details><summary>{deliverySummary(event.deliveries)} · 查看详情</summary><div className="message-deliveries">{event.deliveries.map(d => <div className="message-delivery" key={d.id}><div><strong>{d.channel_name}</strong><span>{d.status === "pending" && d.last_error ? "等待重试" : deliveryStates[d.status] ?? d.status} · {d.attempts}/5 次</span>{d.last_error && <p className="quality-error">{d.last_error}</p>}</div>{d.status === "failed" && <Button disabled={!!busy} onClick={() => void retry(d.id)}>重试投递</Button>}</div>)}</div></details>}
    </article>)}
    {data.events.filter(e => filter === "all" || e.category === filter).length === 0 && <p>当前没有此类消息。请先确认对应采集和规则已启用。</p>}
   </section>
   <IncidentHistory/>
   <details className="message-section"><summary>查看自动动作效果</summary><ActionEffects/></details>
  </>}
 </div>;
}

function ChannelEditor({channel, close, saved}: {channel: Channel | null; close: () => void; saved: () => void}) {
 const [name, setName] = useState(channel?.name ?? "飞书告警"), [provider, setProvider] = useState<Provider>(channel?.provider ?? "feishu");
 const [enabled, setEnabled] = useState(channel?.enabled ?? false), [selected, setSelected] = useState(channel?.categories ?? Object.keys(categories));
 const [url, setUrl] = useState(""), [secret, setSecret] = useState(""), [clearSign, setClearSign] = useState(false), [saving, setSaving] = useState(false), [error, setError] = useState("");
 const ref = useRef<HTMLFormElement>(null);
 useEffect(() => {ref.current?.scrollIntoView({block: "nearest"}); ref.current?.querySelector("input")?.focus();}, []);
 async function save(e: FormEvent) {
  e.preventDefault(); setSaving(true); setError("");
  try {await api(`/notifications/channels${channel ? `/${channel.id}` : ""}`, {method: channel ? "PUT" : "POST", ...json({name, provider, enabled, categories: selected, revision: channel?.revision, webhook_url: url, signing_secret: secret, clear_signing_secret: clearSign})}); saved();}
  catch (e) {setError(errorMessage(e));} finally {setSaving(false);}
 }
 return <form className="quality-editor" ref={ref} onSubmit={e => void save(e)}><div className="quality-section-heading"><h2>{channel ? "编辑通知渠道" : "添加通知渠道"}</h2><Button onClick={close}>取消</Button></div>
  <div className="quality-form-grid"><Field label="渠道名称"><Input value={name} maxLength={100} required onChange={e => setName(e.target.value)}/></Field><Field label="接收方式"><select value={provider} onChange={e => {setProvider(e.target.value as Provider); setSecret("");}}>{Object.entries(providers).filter(([key]) => key !== "auto" || channel?.provider === "auto").map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></Field></div>
  <Field label="Webhook 地址" hint={channel ? "已加密保存。留空保留；更换地址会取消尚未发送的旧消息。" : "从群自定义机器人设置中复制地址。"}><Input type="url" required={!channel} autoComplete="off" value={url} onChange={e => setUrl(e.target.value)} placeholder={provider === "feishu" ? "https://open.feishu.cn/open-apis/bot/v2/hook/…" : "https://…"}/></Field>
  {provider === "feishu" && <><Field label="签名密钥（可选）" hint={channel?.signing_secret_configured ? "已保存。留空保留；更换机器人地址时请重新填写或明确清除。" : "机器人开启签名校验时填写。密钥加密保存，不会回显。"}><Input type="password" autoComplete="new-password" maxLength={512} value={secret} disabled={clearSign} onChange={e => setSecret(e.target.value)}/></Field>{channel?.signing_secret_configured && <label className="quality-checkbox-line"><input type="checkbox" checked={clearSign} onChange={e => {setClearSign(e.target.checked); setSecret("");}}/>清除已保存的签名密钥</label>}</>}
  <fieldset className="message-subscriptions"><legend>接收哪些消息</legend>{Object.entries(categories).map(([key, label]) => <label key={key}><input type="checkbox" checked={selected.includes(key)} onChange={e => setSelected(current => e.target.checked ? [...current, key] : current.filter(v => v !== key))}/>{label}</label>)}</fieldset>
  <label className="quality-checkbox-line"><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)}/>启用此渠道，接收之后发生的新事件</label>
  {error && <p role="alert" className="quality-error">{error}</p>}<Button type="submit" variant="primary" loading={saving}>保存渠道</Button>
 </form>;
}
function RulesEditor({rules, saved}: {rules: Rules; saved: () => void}) {
 const [v, setV] = useState({...rules}), [saving, setSaving] = useState(false), [error, setError] = useState("");
 async function save(e: FormEvent) {e.preventDefault(); setSaving(true); try {await api("/notifications/rules", {method: "PUT", ...json(v)}); saved();} catch (e) {setError(errorMessage(e));} finally {setSaving(false);}}
 return <form onSubmit={e => void save(e)}><div className="quality-form-grid">
  <Field label="采购倍率上涨阈值（%）" hint="从上次已通知倍率或后续低点累计计算；0 表示任何上涨。"><Input type="number" required min={0} max={10000} step="any" value={v.price_rise_percent} onChange={e => setV({...v, price_rise_percent: Number(e.target.value)})}/></Field>
  <Field label="涨价通知冷却（分钟）"><Input type="number" required min={1} max={1440} value={v.price_cooldown_seconds / 60} onChange={e => setV({...v, price_cooldown_seconds: Number(e.target.value) * 60})}/></Field>
  <Field label="低余额阈值" hint="按各上游返回的余额单位判断，不进行跨币种比较。"><Input type="number" required min={0} max={1e12} step="any" value={v.balance_threshold} onChange={e => setV({...v, balance_threshold: Number(e.target.value)})}/></Field>
  <Field label="持续低余额提醒间隔（分钟）"><Input type="number" required min={5} max={43200} value={v.balance_cooldown_seconds / 60} onChange={e => setV({...v, balance_cooldown_seconds: Number(e.target.value) * 60})}/></Field>
 </div><label className="quality-checkbox-line"><input type="checkbox" checked={v.balance_enabled} onChange={e => setV({...v, balance_enabled: e.target.checked})}/>启用全局低余额事件与恢复通知</label>{error && <p role="alert" className="quality-error">{error}</p>}<Button type="submit" variant="primary" loading={saving}>保存规则</Button></form>;
}
function IncidentHistory() {
 const [items, setItems] = useState<Incident[] | null>(null), [error, setError] = useState("");
 async function load() {try {setItems(await api<Incident[]>("/quality/incidents")); setError("");} catch (e) {setError(errorMessage(e));}}
 return <details className="message-section" onToggle={e => {if (e.currentTarget.open && items === null) void load();}}><summary>查看采集与控制事件</summary>{error && <p className="quality-error" role="alert">{error}<Button onClick={() => void load()}>重试读取</Button></p>}{items?.length === 0 && <p>暂无采集或控制故障记录。</p>}{items?.map(item => <article className="message-event" key={`${item.account_id}:${item.channel}`}><div className="message-event-heading"><strong>{item.account_name}</strong><Badge tone={!item.current_source ? "neutral" : item.active ? "warning" : "success"}>{!item.current_source ? "历史来源" : item.active ? "处理中" : "已恢复"}</Badge><span>{incidentNames[item.channel] ?? item.channel}</span></div><p>{item.active ? item.message : "此事件已恢复"}</p><p className="quality-note">发生 {formatDate(item.opened_at ?? undefined)}{item.resolved_at ? ` · 恢复 ${formatDate(item.resolved_at)}` : ""} · 第 {item.episode} 次</p></article>)}</details>;
}
