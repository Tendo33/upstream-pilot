import { Search, UsersRound } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Link } from "react-router-dom";
import { formatFullDate } from "../lib";
import { Badge, EmptyState, ErrorState, Input, PageHeader, PageLoader } from "../components/ui";

type ServiceUser = { id: number; username?: string; email: string; role: string; status: string; balance: number; concurrency: number; last_active_at?: string; created_at: string };
type Response = { items: ServiceUser[]; page: number; page_size: number; total: number };

export function ServiceUsersPage() {
  const [rows, setRows] = useState<Response | null>(null); const [search, setSearch] = useState(""); const [page, setPage] = useState(1); const [error, setError] = useState("");
  const load = useCallback(async () => { try { setRows(await api<Response>(`/service-users?search=${encodeURIComponent(search)}&page=${page}`)); setError(""); } catch (cause) { setError(errorMessage(cause)); } }, [search, page]);
  useEffect(() => { const timer = setTimeout(() => void load(), 250); return () => clearTimeout(timer); }, [load]);
  if (!rows && !error) return <PageLoader />;
  return <div className="page"><PageHeader title="服务用户" description={`来自已配置的 Sub2API 只读库 · 共 ${rows?.total ?? 0} 人`} actions={<div className="input-prefix"><Search size={15} /><Input value={search} onChange={e => { setSearch(e.target.value); setPage(1); }} placeholder="搜索用户名或邮箱" /></div>} />
    {error ? <ErrorState message={error} retry={() => void load()} /> : rows?.items.length === 0 ? <section className="panel"><EmptyState title="暂无服务用户" description="连接 Sub2API 只读数据库后，这里会显示全量用户。" icon={<UsersRound size={21} />} /></section> : <section className="panel"><div className="quality-table-scroll"><table><thead><tr><th>用户</th><th>角色</th><th>状态</th><th>余额</th><th>并发</th><th>最近活跃</th></tr></thead><tbody>{rows?.items.map(user => <tr key={user.id}><td><Link to={`/users/${user.id}`}><strong>{user.username || user.email}</strong></Link><small>ID: {user.id}<br />{user.email}</small></td><td>{user.role}</td><td><Badge tone={user.status === "active" ? "success" : "neutral"}>{user.status}</Badge></td><td>{Number(user.balance || 0).toFixed(4)}</td><td>{user.concurrency}</td><td>{formatFullDate(user.last_active_at || user.created_at)}</td></tr>)}</tbody></table></div><div className="quality-actions"><button className="button button-secondary" disabled={page <= 1} onClick={() => setPage(v => v - 1)}>上一页</button><span>第 {page} 页 · 共 {Math.ceil((rows?.total ?? 0) / 50)} 页</span><button className="button button-secondary" disabled={page * 50 >= (rows?.total ?? 0)} onClick={() => setPage(v => v + 1)}>下一页</button></div></section>}
  </div>;
}
