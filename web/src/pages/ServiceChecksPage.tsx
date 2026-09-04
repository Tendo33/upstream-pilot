import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage, json } from "../api";
import { Badge, Button, EmptyState, Field, Input, PageHeader, PageLoader, useToast } from "../components/ui";
import { formatDate } from "../lib";
type ProbeTarget = {id: string; name: string; site_name: string; kind: "group" | "account"; platform: string};

type ProfileConfig = {
  name: string; model: string; protocol: "chat" | "responses" | "messages";
  stream: boolean; tools: boolean; max_output_tokens: number; base_url: string;
  group_key_confirmed: boolean; direct_source_confirmed: boolean; adaptive:boolean; interval_seconds: number; timeout_seconds: number;
  objectives: {success_percent: number; first_content_ms: number; require_complete: boolean; minimum_samples: number; minimum_independent_backups: number; max_request_cost: number | null};
  budget: {daily_requests: number; daily_tokens: number; daily_cost: number | null; request_cost_reserve: number | null; currency: string; cost_basis: string};
};
type Profile = {account_id: string; id: string; group_id: string; group_name: string; site_id: string; generation: number; key_configured: boolean; enabled: boolean; config: ProfileConfig; last_probe_at: string | null; last_error: string};
type Result = {success: boolean; http_status: number; first_content_ms: number | null; duration_ms: number; complete: boolean; tool_valid: boolean; actual_model: string; failure: string; request_id: string; input_tokens: number | null; output_tokens: number | null};
type Run = {id: string; generation: number; status: string; reserved_tokens: number; reserved_cost: number | null; result: Result; started_at: string};
type Objective = {unconfirmed: number; profile_id: string; status: string; reason: string; samples: number; success_percent: number | null; first_content_p95_ms: number | null; complete_percent: number | null};

function initialConfig(): ProfileConfig {
  return {name: "", model: "", protocol: "responses", stream: true, tools: false, max_output_tokens: 512, base_url: "", group_key_confirmed: false, direct_source_confirmed: false, adaptive:true, interval_seconds: 3600, timeout_seconds: 45,
    objectives: {success_percent: 99, first_content_ms: 8000, require_complete: true, minimum_samples: 5, minimum_independent_backups: 1, max_request_cost: null},
    budget: {daily_requests: 24, daily_tokens: 120000, daily_cost: null, request_cost_reserve: null, currency: "", cost_basis: ""}};
}
const protocolNames = {chat: "Chat Completions", responses: "Responses", messages: "Messages"};
const ms = (value: number | null | undefined) => value == null ? "未知" : `${value} ms`;

export function ServiceChecksPage() {
  const [profiles, setProfiles] = useState<Profile[] | null>(null);
  const [groups, setGroups] = useState<ProbeTarget[]>([]);
  const [objectives, setObjectives] = useState<Objective[]>([]);
  const [editor, setEditor] = useState<{profile: Profile | null} | null>(null);
  const [history, setHistory] = useState<{profile: Profile; runs: Run[]} | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const {toast} = useToast();
  const request = useRef<AbortController | null>(null);
  const load = useCallback(async () => {
    request.current?.abort(); const controller = new AbortController(); request.current = controller;
    try {
      const [next, groupList, summary] = await Promise.all([
        api<Profile[]>("/service-profiles", {signal: controller.signal}),
        api<ProbeTarget[]>("/probe-targets", {signal: controller.signal}),
        api<Objective[]>("/service-objectives", {signal: controller.signal}),
      ]);
      if (!controller.signal.aborted) {setProfiles(next); setGroups(groupList); setObjectives(summary); setError("");}
    } catch (e) {if (!controller.signal.aborted) setError(errorMessage(e));}
  }, []);
  useEffect(() => {void load(); return () => request.current?.abort();}, [load]);
  async function probe(profile: Profile) {
    setBusy(profile.id); setError("");
    try {
      const result = await api<Result>(`/service-profiles/${profile.id}/probe`, {method: "POST"});
      toast(result.success ? "协议探测成功" : `协议探测失败：${result.failure}`, result.success ? "success" : "error");
      await load();
    } catch (e) {setError(errorMessage(e));} finally {setBusy("");}
  }
  async function showHistory(profile: Profile) {
    setBusy(profile.id);
    try {setHistory({profile, runs: await api<Run[]>(`/service-profiles/${profile.id}/runs`)}); setEditor(null);}
    catch (e) {setError(errorMessage(e));} finally {setBusy("");}
  }
  return <div className="page">
    <PageHeader title="服务探测档案" description="分组入口验证用户路径；账号直连档案验证供应商协议。两种视角分别记录。" actions={<div className="quality-actions"><Link to="/">返回质量页</Link><Button onClick={() => void load()}>刷新</Button><Button variant="primary" onClick={() => {setEditor({profile: null}); setHistory(null);}}>新建档案</Button></div>}/>
    <p className="quality-note">每个模型和协议分别建档，只发送合成请求，不执行工具。分组档案经过 Sub2API 入口；账号档案从 Pilot 直连供应商，不经过 Sub2API 的代理、模型映射或请求改写。真实用户最终可用率另行统计。</p>
    {error && <p className="quality-error" role="alert">{error}<Button onClick={() => void load()}>重试读取</Button></p>}
    {editor && <ProfileEditor key={editor.profile?.id ?? "new"} profile={editor.profile} groups={groups} close={() => setEditor(null)} saved={() => {setEditor(null); void load();}}/>}
    {!profiles && !error ? <PageLoader/> : profiles?.length === 0 && !editor ? <EmptyState title="还没有分组探测档案" description="先保存一个模型、协议和专用 Key，手动验证成功后再开启定时探测。"/> : profiles?.map(profile => {
      const summary = objectives.find(item => item.profile_id === profile.id);
      return <article className="service-profile-row" key={profile.id}>
        <div className="quality-section-heading"><div><h2>{profile.config.name}</h2><p className="quality-note">{profile.account_id ? "账号直连" : "分组入口"} · {profile.group_name} · {profile.config.model} · {protocolNames[profile.config.protocol]} · {profile.config.stream ? "流式" : "非流式"}{profile.config.tools ? " · 工具结构" : ""}</p></div><Badge tone={profile.enabled ? "success" : "neutral"}>{profile.enabled ? "定时探测开启" : "仅手动"}</Badge></div>
        <div className="service-profile-facts"><span>成功率 {summary?.success_percent == null ? "未知" : `${summary.success_percent.toFixed(1)}%`}</span><span>首字 P95 {ms(summary?.first_content_p95_ms)}</span><span>完整结束 {summary?.complete_percent == null ? "未知" : `${summary.complete_percent.toFixed(1)}%`}</span><span>{summary?.samples ?? 0} 个已完成样本 / 24h · {summary?.unconfirmed ?? 0} 个未确认</span></div>
        <p className="quality-reason">{summary?.reason ?? "等待探测结果"}</p>
        {profile.last_error && <p className="quality-error">最近探测：{profile.last_error}</p>}
        <p className="quality-note">上次探测 {formatDate(profile.last_probe_at ?? undefined)} · 每日最多 {profile.config.budget.daily_requests} 次，{profile.config.budget.daily_tokens.toLocaleString()} token 预留 · {profile.account_id ? "凭据由 Sub2API 按需读取" : profile.key_configured ? "Key 已加密保存" : "尚未配置 Key"}</p>
        <div className="quality-actions"><Button disabled={!!busy || (!profile.account_id && !profile.key_configured)} loading={busy === profile.id} onClick={() => void probe(profile)}>探测一次</Button><Button disabled={!!busy} onClick={() => {setEditor({profile}); setHistory(null);}}>编辑档案</Button><Button disabled={!!busy} onClick={() => void showHistory(profile)}>查看记录</Button></div>
      </article>;
    })}
    {history && <section className="quality-editor"><div className="quality-section-heading"><h2>{history.profile.config.name} · 探测记录</h2><Button onClick={() => setHistory(null)}>关闭记录</Button></div><p className="quality-note">请求发送后不会自动重试。预留额度包括中断、失败和费用未确认的尝试，并不表示实际账单金额。</p><div className="quality-table-scroll"><table><thead><tr><th>时间 / 代次</th><th>最终结果</th><th>首字 / 耗时</th><th>完整 / 工具</th><th>返回模型</th><th>token 预留</th></tr></thead><tbody>{history.runs.map(run => <tr key={run.id}><td>{formatDate(run.started_at)} / {run.generation}</td><td>{({passed: "成功", failed: "失败", reserved: "等待结果", abandoned: "中断未确认"} as Record<string, string>)[run.status] ?? run.status}<br/>{run.result.failure}</td><td>{ms(run.result.first_content_ms)} / {ms(run.result.duration_ms)}</td><td>{run.result.complete ? "完整" : "未确认"} / {run.result.tool_valid ? "结构有效" : "—"}</td><td>{run.result.actual_model || "未知"}</td><td>{run.reserved_tokens.toLocaleString()}</td></tr>)}</tbody></table></div>{history.runs.length === 0 && <p>暂无探测记录。</p>}</section>}
  </div>;
}

function ProfileEditor({profile, groups, close, saved}: {profile: Profile | null; groups: ProbeTarget[]; close: () => void; saved: () => void}) {
  const [config, setConfig] = useState<ProfileConfig>(() => profile ? structuredClone(profile.config) : initialConfig());
  const [kind, setKind] = useState<"group" | "account">(profile?.account_id ? "account" : "group");
  const [group, setGroup] = useState(profile?.account_id || profile?.group_id || "");
  const [enabled, setEnabled] = useState(profile?.enabled ?? false);
  const [key, setKey] = useState("");
  const [clearKey, setClearKey] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const first = useRef<HTMLFormElement>(null);
  useEffect(() => {const input = first.current?.querySelector("input"); input?.focus(); input?.scrollIntoView({block: "center"});}, []);
  function field<K extends keyof ProfileConfig>(name: K, value: ProfileConfig[K]) {setConfig(current => ({...current, [name]: value}));}
  function budget<K extends keyof ProfileConfig["budget"]>(name: K, value: ProfileConfig["budget"][K]) {setConfig(current => ({...current, budget: {...current.budget, [name]: value}}));}
  function objective<K extends keyof ProfileConfig["objectives"]>(name: K, value: ProfileConfig["objectives"][K]) {setConfig(current => ({...current, objectives: {...current.objectives, [name]: value}}));}
  async function submit(event: FormEvent) {
    event.preventDefault(); setSaving(true); setError("");
    try {await api("/service-profiles", {method: "POST", ...json({id: profile?.id ?? "", generation: profile?.generation ?? 0, group_id: kind === "group" ? group : "", account_id: kind === "account" ? group : "", enabled, key, clear_key: clearKey, config})}); setKey(""); saved();}
    catch (e) {setError(errorMessage(e));} finally {setSaving(false);}
  }
  return <form ref={first} className="quality-editor" onSubmit={event => void submit(event)}>
    <div className="quality-section-heading"><h2>{profile ? "编辑探测档案" : "新建探测档案"}</h2><Button onClick={close} disabled={saving}>取消</Button></div>
    <div className="quality-form-grid">
      <Field label="档案名称"><Input required value={config.name} maxLength={120} onChange={e => field("name", e.target.value)}/></Field>
      <Field label="探测视角"><select disabled={!!profile} value={kind} onChange={e => {setKind(e.target.value as "group" | "account"); setGroup(""); setKey(""); field("base_url", "");}}><option value="group">分组实际入口</option><option value="account">账号供应商直连</option></select></Field>
      <Field label={kind === "group" ? "所属分组" : "来源账号"}><select required disabled={!!profile} value={group} onChange={e => setGroup(e.target.value)}><option value="">选择探测目标</option>{groups.filter(g => g.kind === kind).map(g => <option key={g.id} value={g.id}>{g.site_name} / {g.name} · {g.platform}</option>)}</select></Field>
      <Field label="测试模型" hint={kind === "account" ? "填写供应商模型名称，直连请求不会应用 Sub2API 模型映射。" : "填写用户在这个分组调用的模型名称。"}><Input required value={config.model} maxLength={256} onChange={e => field("model", e.target.value)}/></Field>
      <Field label="请求协议"><select value={config.protocol} onChange={e => field("protocol", e.target.value as ProfileConfig["protocol"])}>{Object.entries(protocolNames).map(([value, name]) => <option value={value} key={value}>{name}</option>)}</select></Field>
      {kind === "group" && <Field label="实际入口地址" hint="留空使用所属站点地址。填写用户实际访问的 API 根地址。"><Input type="url" value={config.base_url} onChange={e => field("base_url", e.target.value)}/></Field>}
      {kind === "group" && <Field label="专用分组 Key" hint={profile?.key_configured ? "留空保留已保存的 Key。不要填写管理员 Key。" : "只用于合成探测；不要填写管理员 Key。"}><Input disabled={clearKey} type="password" autoComplete="new-password" value={key} onChange={e => setKey(e.target.value)}/></Field>}
      <Field label="探测间隔（秒）"><Input type="number" required min={30} max={86400} value={config.interval_seconds} onChange={e => field("interval_seconds", Number(e.target.value))}/></Field>
      <Field label="每次超时（秒）"><Input type="number" required min={3} max={300} value={config.timeout_seconds} onChange={e => field("timeout_seconds", Number(e.target.value))}/></Field>
      <Field label="最大输出 tokens" hint="推理模型可能需要更高额度才能正常结束。"><Input type="number" required min={16} max={8192} value={config.max_output_tokens} onChange={e => field("max_output_tokens", Number(e.target.value))}/></Field>
    </div>
    <div className="quality-checkboxes"><label><input type="checkbox" checked={config.adaptive} onChange={e=>field("adaptive",e.target.checked)}/>按状态调整频率（不突破每日预算）</label><label><input type="checkbox" checked={config.stream} onChange={e => field("stream", e.target.checked)}/>流式探测，测量首字</label><label><input type="checkbox" checked={config.tools} onChange={e => field("tools", e.target.checked)}/>验证工具调用结构（不执行工具）</label>{kind === "group" ? <label><input type="checkbox" checked={config.group_key_confirmed} onChange={e => field("group_key_confirmed", e.target.checked)}/>我已确认此 Key 专属于所选分组</label> : <label><input type="checkbox" checked={config.direct_source_confirmed} onChange={e => field("direct_source_confirmed", e.target.checked)}/>允许按需读取此账号上游凭据，并使用 Pilot 直连探测</label>}{kind === "group" && profile?.key_configured && <label><input type="checkbox" checked={clearKey} onChange={e => {setClearKey(e.target.checked); if (e.target.checked) {setKey(""); setEnabled(false);}}}/>清除已保存 Key 并暂停</label>}<label><input type="checkbox" disabled={clearKey} checked={enabled} onChange={e => setEnabled(e.target.checked)}/>开启定时探测</label></div>
    <h3>服务目标</h3>
    <div className="quality-form-grid">
      <Field label="目标成功率（%）"><Input type="number" required min={0.1} max={100} step="any" value={config.objectives.success_percent} onChange={e => objective("success_percent", Number(e.target.value))}/></Field>
      <Field label="首字 P95 上限（毫秒）"><Input type="number" required min={100} max={300000} value={config.objectives.first_content_ms} onChange={e => objective("first_content_ms", Number(e.target.value))}/></Field>
      <Field label="最少有效样本"><Input type="number" required min={2} max={300} value={config.objectives.minimum_samples} onChange={e => objective("minimum_samples", Number(e.target.value))}/></Field>
    </div>
    <h3>每日预算（UTC 日界）</h3>
    <div className="quality-form-grid">
      <Field label="最多请求次数"><Input type="number" required min={1} max={10000} value={config.budget.daily_requests} onChange={e => budget("daily_requests", Number(e.target.value))}/></Field>
      <Field label="token 预留预算" hint="每次预留 4096 输入余量加最大输出；未知用量不退回预算。"><Input type="number" required min={4096 + config.max_output_tokens} max={100000000} value={config.budget.daily_tokens} onChange={e => budget("daily_tokens", Number(e.target.value))}/></Field>
      <Field label="金额预算（可选）"><Input type="number" min={0.000001} step="any" value={config.budget.daily_cost ?? ""} onChange={e => budget("daily_cost", e.target.value === "" ? null : Number(e.target.value))}/></Field>
      <Field label="单次成本预留（可选）"><Input type="number" min={0.000001} step="any" value={config.budget.request_cost_reserve ?? ""} onChange={e => budget("request_cost_reserve", e.target.value === "" ? null : Number(e.target.value))}/></Field>
      <Field label="币种" hint="例如 USD、CNY"><Input maxLength={3} value={config.budget.currency} onChange={e => budget("currency", e.target.value.toUpperCase())}/></Field>
      <Field label="计价依据" hint="注明单次预留的价格来源或版本。"><Input maxLength={256} value={config.budget.cost_basis} onChange={e => budget("cost_basis", e.target.value)}/></Field>
    </div>
    <p className="quality-note">单次成本预留是你的预算假设，不是对上游的强制限价。价格未知时仅使用请求和 token 限制；开启金额预算后，未知价格或当日混合币种会阻止新请求。</p>
    {error && <p className="quality-error" role="alert">{error}</p>}
    <Button type="submit" variant="primary" loading={saving}>保存档案</Button>
  </form>;
}
