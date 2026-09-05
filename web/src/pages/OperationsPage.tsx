import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { Badge, Button, PageHeader, PageLoader } from "../components/ui";
import { formatDate } from "../lib";

type Feed = {status: string; message: string; rows: number; truncated: boolean};
type Coverage = {status?: string; message?: string; checked_at?: string; truncated?: boolean; feeds?: Record<string, Feed>};
type Billing = {counts: Record<string, number>; accounts: {account_id: number; name: string; status: string; last_attempt_at: string | null}[]};
type Site = {id: string; name: string; connection_state: string; last_inventory_at: string | null; last_reconcile_at: string | null; inventory_lag_seconds: number; reconcile_lag_seconds: number; usage_collection: Coverage; traffic_collection: Coverage; native_billing: Billing};
type Task = {kind: string; resource_type: string; started_at: string; finished_at: string | null; last_success_at: string | null; duration_ms: number | null; last_error: string; running: boolean};
type Outcome = {group_id: string; name: string; model: string; observed_requests: number; confirmed_success: number; confirmed_failure: number; unconfirmed: number; latest_at: string; coverage: Coverage};
type Data = {sites: Site[]; tasks: Task[]; workers: number; pool: {total: number; idle: number; acquired: number; acquire_count: number; empty_acquires: number}; database: {count: number; errors: number}; process?: {heap_mb: number; goroutines: number}; checked_at: string};
const states: Record<string, string> = {ok: "已读取", partial: "采集不完整", unsupported: "接口不支持", error: "采集失败", unknown: "未知", fresh: "声明有效", stale: "已过期", disabled: "未启用", failed: "采集失败"};
const feedNames: Record<string, string> = {requests: "请求明细", upstream_errors: "上游尝试错误", request_errors: "最终请求错误"};
const stateName = (s?: string) => s ? states[s] ?? s : "未运行";

export function OperationsPage() {
 const [data, setData] = useState<Data | null>(null), [outcomes, setOutcomes] = useState<Outcome[]>([]), [error, setError] = useState("");
 const request = useRef<AbortController | null>(null);
 const load = useCallback(async () => {
  request.current?.abort(); const controller = new AbortController(); request.current = controller;
  try {
   const [next, results] = await Promise.all([api<Data>("/operations", {signal: controller.signal}), api<Outcome[]>("/request-outcomes", {signal: controller.signal})]);
   if (!controller.signal.aborted) {setData(next); setOutcomes(results); setError("");}
  } catch (e) {if (!controller.signal.aborted) setError(errorMessage(e));}
 }, []);
 useEffect(() => {void load(); const timer = setInterval(() => void load(), 15000); return () => {clearInterval(timer); request.current?.abort();};}, [load]);
 return <div className="page">
  <PageHeader title="运行状态" description="查看分组最终结果、采集盲区、任务积压和进程负载。" actions={<div className="quality-actions"><Link to="/">返回质量页</Link><Button onClick={() => void load()}>刷新</Button></div>}/>
  {error && <p className="quality-error" role="alert">{error}</p>}
  {!data && !error ? <PageLoader/> : data && <>
   <section className="quality-editor"><h2>分组最终请求结果</h2>
    <p className="quality-note">最近 15 分钟，按分组和请求标识去重。上游尝试失败单独计入供应商质量；流结束字段缺失、矛盾结果或采集缺口不能算作完整成功率。</p>
    {outcomes.length === 0 ? <p>尚无可关联的分组请求记录，等待站点采集。</p> : <div className="quality-table-scroll"><table><thead><tr><th>分组 / 模型</th><th>已确认成功</th><th>已确认失败</th><th>未确认</th><th>采集状态</th></tr></thead><tbody>{outcomes.map(row => <tr key={`${row.group_id}:${row.model}`}><td>{row.name}<small>{row.model || "模型未知"}</small></td><td>{row.confirmed_success}</td><td>{row.confirmed_failure}</td><td>{row.unconfirmed}</td><td>{stateName(row.coverage.status)}{row.coverage.truncated ? " · 样本截断" : ""}<small>{formatDate(row.latest_at)}</small></td></tr>)}</tbody></table></div>}
   </section>
   <section className="quality-editor"><h2>站点采集</h2>{data.sites.map(site => <article className="quality-notification" key={site.id}>
    <div><strong>{site.name}</strong><Badge tone={site.connection_state === "healthy" ? "success" : "danger"}>{site.connection_state === "healthy" ? "连接正常" : site.connection_state}</Badge><span>库存积压 {site.inventory_lag_seconds}s</span><span>策略积压 {site.reconcile_lag_seconds}s</span></div>
    <p>请求采集：{stateName(site.traffic_collection?.status)} · 用量：{stateName(site.usage_collection?.status)}</p>
    {site.traffic_collection?.message && <p className="quality-note">{site.traffic_collection.message}</p>}
    {Object.entries(site.traffic_collection?.feeds ?? {}).map(([name, feed]) => <p key={name}>{feedNames[name] ?? name}：{stateName(feed.status)} · {feed.rows} 条{feed.truncated ? " · 样本截断" : ""}{feed.message ? ` · ${feed.message}` : ""}</p>)}
    <details><summary>上游采购声明覆盖 · {site.native_billing?.accounts.length ?? 0} 个账号</summary>
     <p className="quality-note">这是 Sub2API 已有声明的采集状态，不会触发探测，也不代表余额已核实或采购价格可以直接比较。</p>
     <p>{Object.entries(site.native_billing?.counts ?? {}).map(([state, count]) => `${stateName(state)} ${count}`).join(" · ") || "尚未同步声明状态"}</p>
     {site.native_billing?.accounts.map(account => <p key={account.account_id}>{account.name} · {stateName(account.status)} · 上次尝试 {formatDate(account.last_attempt_at ?? undefined)}</p>)}
    </details>
    <small className="quality-incident-time">库存 {formatDate(site.last_inventory_at ?? undefined)} · 策略 {formatDate(site.last_reconcile_at ?? undefined)}</small>
   </article>)}</section>
   <section className="quality-editor"><h2>调度器自身健康</h2><div className="service-profile-facts"><span>{data.workers} 个 worker</span><span>数据库连接 {data.pool.acquired}/{data.pool.total}</span><span>等待连接 {data.pool.empty_acquires}</span><span>数据库操作 {data.database.count} · 错误 {data.database.errors}</span>{data.process && <><span>堆内存 {data.process.heap_mb.toFixed(1)} MB</span><span>{data.process.goroutines} 个 goroutine</span></>}</div></section>
   <section className="quality-editor"><h2>最近任务</h2>{data.tasks.length === 0 ? <p>暂无任务记录。</p> : data.tasks.map((task, i) => <article className="quality-notification" key={`${task.resource_type}:${task.started_at}:${i}`}><div><Badge tone={task.last_error ? "danger" : task.running ? "warning" : "success"}>{task.last_error ? "失败" : task.running ? "运行中" : "完成"}</Badge><strong>{task.kind}</strong><span>{task.resource_type}</span><span>{formatDate(task.started_at)}</span></div><p>{task.last_error || `耗时 ${task.duration_ms ?? "—"} ms`}</p><small className="quality-incident-time">该资源上次成功 {formatDate(task.last_success_at ?? undefined)}</small></article>)}</section>
  </>}
 </div>;
}
