import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorState, PageHeader, PageLoader } from "../components/ui";
import { formatDate } from "../lib";
type Order = { id:number; user_id:number; normalized_status:string; amount:number; currency:string|null; created_at:string|null; completed_at:string|null };
type Data = { items:Order[]; total:number; summary:{count_by_status:Record<string,number>; amount_by_status:Record<string,number>} };
const labels:Record<string,string>={pending:"待支付",paid:"已支付",recharging:"充值中",completed:"已完成",expired:"已过期",failed:"失败",cancelled:"已取消",unknown:"未知"};
export function BillingAuditPage() {
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setData(null);
    setError("");
    void api<Data>(`/source/billing/orders?page=${page}&page_size=50`, { signal: controller.signal })
      .then(next => { if (!controller.signal.aborted) setData(next); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); });
    return () => controller.abort();
  }, [page, retry]);
  return <div className="page"><PageHeader title="充值审计" description="只读查看已配置 Sub2API 库中的订单、支付状态和入账状态。" actions={<Link to="/overview">返回总览</Link>}/>{error ? <ErrorState message={error} retry={() => setRetry(value => value + 1)} /> : !data ? <PageLoader/>:<><section className="metric-grid">{Object.entries(data.summary.count_by_status).map(([k,v])=><article className="metric" key={k}><span>{labels[k]??k}</span><strong>{v}</strong><small>{(data.summary.amount_by_status[k]??0).toFixed(2)}</small></article>)}</section><section className="panel"><div className="quality-table-scroll"><table><thead><tr><th>订单</th><th>用户</th><th>状态</th><th>金额</th><th>创建时间</th><th>完成时间</th></tr></thead><tbody>{data.items.map(o=><tr key={o.id}><td>#{o.id}</td><td>{o.user_id}</td><td><Badge tone={o.normalized_status==="completed"?"success":o.normalized_status==="failed"?"danger":"warning"}>{labels[o.normalized_status]??"未知"}</Badge></td><td>{o.amount.toFixed(2)} {o.currency ?? "币种未记录"}</td><td>{formatDate(o.created_at??undefined)}</td><td>{formatDate(o.completed_at??undefined)}</td></tr>)}</tbody></table></div><div className="quality-actions"><Button disabled={page<=1} onClick={()=>setPage(page-1)}>上一页</Button><span>第 {page} 页 · 共 {data.total} 条</span><Button disabled={page * 50 >= data.total} onClick={()=>setPage(page+1)}>下一页</Button></div></section></>}</div>}
