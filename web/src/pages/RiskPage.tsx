import { Activity, Globe, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { formatFullDate } from "../lib";
import { Badge, Button, EmptyState, ErrorState, PageHeader, PageLoader } from "../components/ui";

type UserRisk = { user_id: number; name: string; requests: number; ips: number; tokens: number; cost: number; last_seen: string };
type Ranking = { as_of: string; windows: { hours: number; items: UserRisk[] }[] };
type IPRow = { ip: string; requests: number; users: number; tokens: number; cost: number; last_seen: string };
type IPData = { as_of: string; items: IPRow[] };
type SharedIP = { ip: string; requests: number; users: number; tokens: number; user_ids: number[]; last_seen: string };
type TokenRow = { token_id: number; requests: number; users: number; ips: number; cost: number; last_seen: string };
type TokenData = { as_of: string; items: TokenRow[] };
type RotationRow = { user_id: number; token_count: number; requests: number; ip_count: number; last_seen: string; risk: string };
type Review = { subject_type: string; subject_id: string; status: string; note: string };
type ModerationRow = { id: number; created_at: string; request_id: string; user_id?: number; user_email: string; api_key_id?: number; model: string; action: string; flagged: boolean; highest_category: string; highest_score: number; violation_count: number; auto_banned: boolean; error: string };
type AuditRow = { id: number; created_at: string; actor_user_id?: number; actor_email: string; actor_role: string; action: string; method: string; path: string; request_id: string; client_ip: string; status_code: number; latency_ms: number };

export function RiskPage() {
  const [tab, setTab] = useState<"rankings" | "ips" | "tokens" | "moderation" | "audit">("rankings");
  const [metric, setMetric] = useState("requests");
  const [refresh, setRefresh] = useState(30);
  const [revision, setRevision] = useState(0);
  const [ranking, setRanking] = useState<Ranking | null>(null);
  const [ips, setIPs] = useState<IPData | null>(null);
  const [sharedIPs, setSharedIPs] = useState<SharedIP[]>([]);
  const [tokens, setTokens] = useState<TokenData | null>(null);
  const [rotation, setRotation] = useState<RotationRow[]>([]);
  const [reviews, setReviews] = useState<Review[]>([]);
  const [moderation, setModeration] = useState<ModerationRow[] | null>(null);
  const [audit, setAudit] = useState<AuditRow[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      setLoading(true);
      try {
        const existing = await api<Review[]>("/operations/risk/reviews", { signal: controller.signal });
        if (!controller.signal.aborted) setReviews(existing);
        if (tab === "rankings") {
          const next = await api<Ranking>(`/operations/risk/rankings?metric=${metric}`, { signal: controller.signal });
          if (!controller.signal.aborted) setRanking(next);
        } else if (tab === "ips") {
          const [next, shared] = await Promise.all([api<IPData>("/operations/risk/ips?hours=24", { signal: controller.signal }), api<SharedIP[]>("/operations/risk/shared-ips", { signal: controller.signal })]);
          if (!controller.signal.aborted) setIPs(next);
          if (!controller.signal.aborted) setSharedIPs(shared);
        } else if (tab === "moderation") {
          const next = await api<ModerationRow[]>("/source/moderation-logs", { signal: controller.signal });
          if (!controller.signal.aborted) setModeration(next);
        } else if (tab === "audit") {
          const next = await api<AuditRow[]>("/source/audit-logs", { signal: controller.signal });
          if (!controller.signal.aborted) setAudit(next);
        } else {
          const [next, rotate] = await Promise.all([api<TokenData>("/operations/risk/tokens", { signal: controller.signal }), api<RotationRow[]>("/operations/risk/token-rotation", { signal: controller.signal })]);
          if (!controller.signal.aborted) setTokens(next);
          if (!controller.signal.aborted) setRotation(rotate);
        }
        if (!controller.signal.aborted) setError("");
      } catch (cause) {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false);
          if (refresh > 0) timer = setTimeout(() => void load(), refresh * 1000);
        }
      }
    }
    void load();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [tab, metric, refresh, revision]);

  async function review(subjectType: string, subjectID: string, status: string) {
    try {
      await api("/operations/risk/reviews", { method: "PUT", body: JSON.stringify({ subject_type: subjectType, subject_id: subjectID, status }) });
      setError("");
      setRevision(v => v + 1);
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }
  const reviewStatus = (type: string, id: string) => reviews.find(item => item.subject_type === type && item.subject_id === id)?.status;
  const reviewButton = (type: string, id: string) => <Button size="sm" onClick={() => void review(type, id, reviewStatus(type, id) === "confirmed" ? "open" : "confirmed")}>{reviewStatus(type, id) === "confirmed" ? "已确认" : "确认风险"}</Button>;

  const data = tab === "rankings" ? ranking : tab === "ips" ? ips : tab === "tokens" ? tokens : tab === "moderation" ? moderation : audit;
  return <div className="page">
    <PageHeader title="风控中心" description="查看服务用户的请求活动、消费与 IP 分布。" actions={<div className="quality-actions">
      <label>刷新 <select value={refresh} onChange={e => setRefresh(Number(e.target.value))}><option value={0}>手动</option><option value={30}>30 秒</option><option value={60}>60 秒</option></select></label>
      <Button disabled={loading} onClick={() => setRevision(v => v + 1)}><RefreshCw size={16} />刷新</Button>
    </div>} />
    <div className="quality-actions" aria-label="风控视图">
      <Button variant={tab === "rankings" ? "primary" : "secondary"} onClick={() => setTab("rankings")}><Activity size={16} />实时排行</Button>
      <Button variant={tab === "ips" ? "primary" : "secondary"} onClick={() => setTab("ips")}><Globe size={16} />IP 监控</Button>
      <Button variant={tab === "tokens" ? "primary" : "secondary"} onClick={() => setTab("tokens")}>Token 风险</Button>
      <Button variant={tab === "moderation" ? "primary" : "secondary"} onClick={() => setTab("moderation")}>内容审核</Button>
      <Button variant={tab === "audit" ? "primary" : "secondary"} onClick={() => setTab("audit")}>活动审计</Button>
      <Link className="button button-secondary" to="/events">控制台日志</Link>
      {tab === "rankings" && <label>排序 <select value={metric} onChange={e => { setRanking(null); setMetric(e.target.value); }}><option value="requests">请求次数</option><option value="cost">实际消费</option></select></label>}
    </div>
    <p className="quality-note">统计已记录的用量日志，不包含未入账的失败请求。IP 数量与消费排行是调查线索，不代表违规。{data && "as_of" in data && ` 更新于 ${formatFullDate(data.as_of)}`}</p>
    {error && <ErrorState message={error} retry={() => setRevision(v => v + 1)} />}
    {!data && !error && <PageLoader />}
    {tab === "rankings" && ranking && <div className="risk-ranking-grid">{ranking.windows.map(window => <section className="panel" key={window.hours}>
      <div className="panel-header"><h2>{window.hours} 小时内 · Top 10</h2></div>
      {window.items.length === 0 ? <EmptyState title="暂无用量记录" description="此时间窗口内没有已记录的请求。" /> : <ol className="risk-ranking-list">{window.items.map((row, i) => <li key={row.user_id}>
        <span className="risk-rank">{i + 1}</span><div className="risk-user"><strong>{row.name}</strong><small>ID: {row.user_id} · IP: {row.ips} · Token: {row.tokens}</small></div><div className="risk-value"><strong>{metric === "cost" ? row.cost.toFixed(4) : row.requests.toLocaleString()}</strong><small>{metric === "cost" ? "实际消费" : "请求次数"}</small>{reviewButton("user", String(row.user_id))}</div>
      </li>)}</ol>}
    </section>)}</div>}
    {tab === "ips" && ips && <><section className="panel"><div className="panel-header"><h2>最近 24 小时 · IP 请求排行</h2></div><div className="quality-table-scroll"><table><thead><tr><th>IP</th><th>请求</th><th>关联用户</th><th>Token</th><th>实际消费</th><th>最近活动</th></tr></thead><tbody>{ips.items.map(row => <tr key={row.ip}><td>{row.ip}</td><td>{row.requests}</td><td>{row.users}</td><td>{row.tokens}</td><td>{row.cost.toFixed(4)}</td><td>{formatFullDate(row.last_seen)}</td></tr>)}</tbody></table></div>{ips.items.length === 0 && <EmptyState title="暂无 IP 记录" description="用量日志需要包含 IP 地址才能显示监控结果。" />}</section><section className="panel"><div className="panel-header"><h2>共享 IP · 关联多个用户</h2></div><div className="quality-table-scroll"><table><thead><tr><th>IP</th><th>用户数</th><th>用户 ID</th><th>请求</th><th>Token</th><th>复核</th></tr></thead><tbody>{sharedIPs.map(row => <tr key={row.ip}><td>{row.ip}</td><td>{row.users}</td><td>{row.user_ids.join(", ")}</td><td>{row.requests}</td><td>{row.tokens}</td><td>{reviewButton("ip", row.ip)}</td></tr>)}</tbody></table></div>{sharedIPs.length===0 && <p className="quality-note">最近 24 小时没有发现同 IP 关联多个用户。</p>}</section></>}
    {tab === "tokens" && tokens && <><section className="panel"><div className="panel-header"><h2>最近 24 小时 · Token 活动</h2></div><p className="quality-note">Token 轮换风险线索：同一 Token 关联多个用户或多个 IP 时需要人工复核。</p><div className="quality-table-scroll"><table><thead><tr><th>Token ID</th><th>请求</th><th>用户数</th><th>IP 数</th><th>实际消费</th><th>最近活动</th><th>复核</th></tr></thead><tbody>{tokens.items.map(row => <tr key={row.token_id}><td>{row.token_id}</td><td>{row.requests}</td><td>{row.users}</td><td>{row.ips}</td><td>{row.cost.toFixed(4)}</td><td>{formatFullDate(row.last_seen)}</td><td>{reviewButton("token", String(row.token_id))}</td></tr>)}</tbody></table></div>{tokens.items.length === 0 && <EmptyState title="暂无 Token 用量" description="用量日志中没有可关联的 Token。" />}</section><section className="panel"><div className="panel-header"><h2>用户 Token 轮换线索</h2></div><div className="quality-table-scroll"><table><thead><tr><th>用户 ID</th><th>Token 数</th><th>请求</th><th>IP 数</th><th>风险</th><th>最近活动</th><th>复核</th></tr></thead><tbody>{rotation.map(row => <tr key={row.user_id}><td>{row.user_id}</td><td>{row.token_count}</td><td>{row.requests}</td><td>{row.ip_count}</td><td><Badge tone={row.risk === "high" ? "danger" : "warning"}>{row.risk === "high" ? "高风险" : "需要复核"}</Badge></td><td>{formatFullDate(row.last_seen)}</td><td>{reviewButton("user", String(row.user_id))}</td></tr>)}</tbody></table></div>{rotation.length===0 && <p className="quality-note">暂无达到轮换复核阈值的用户。</p>}</section></>}
    {tab === "moderation" && moderation && <section className="panel"><div className="panel-header"><h2>内容审核记录</h2></div><div className="quality-table-scroll"><table><thead><tr><th>时间</th><th>用户</th><th>模型</th><th>动作</th><th>分类</th><th>分数</th><th>违规次数</th><th>自动封禁</th><th>复核</th></tr></thead><tbody>{moderation.map(row => <tr key={row.id}><td>{formatFullDate(row.created_at)}</td><td>{row.user_email || row.user_id || "—"}</td><td>{row.model}</td><td>{row.action}</td><td>{row.highest_category || "—"}</td><td>{Number(row.highest_score || 0).toFixed(4)}</td><td>{row.violation_count}</td><td>{row.auto_banned ? <Badge tone="danger">是</Badge> : "否"}</td><td>{reviewButton("moderation", String(row.id))}</td></tr>)}</tbody></table></div>{moderation.length===0 && <p className="quality-note">暂无内容审核记录。</p>}</section>}
    {tab === "audit" && audit && <section className="panel"><div className="panel-header"><h2>Sub2API 活动审计</h2></div><div className="quality-table-scroll"><table><thead><tr><th>时间</th><th>操作者</th><th>动作</th><th>方法</th><th>路径</th><th>状态</th><th>IP</th></tr></thead><tbody>{audit.map(row => <tr key={row.id}><td>{formatFullDate(row.created_at)}</td><td>{row.actor_email || row.actor_user_id || "—"}<small>{row.actor_role}</small></td><td>{row.action}</td><td>{row.method}</td><td>{row.path}</td><td>{row.status_code}</td><td>{row.client_ip || "—"}</td></tr>)}</tbody></table></div>{audit.length===0 && <p className="quality-note">暂无活动审计记录。</p>}</section>}
  </div>;
}
