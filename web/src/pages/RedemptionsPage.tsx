import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { Badge, Button, ErrorState, PageHeader, PageLoader } from "../components/ui";
import { formatDate } from "../lib";
type Code={id:number;code:string;type:string;status:string;value:number;used_by:number|null;expires_at:string|null;created_at:string|null};
type Data={items:Code[];total:number};
export function RedemptionsPage() {
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setData(null);
    setError("");
    void api<Data>(`/source/redeem-codes?page=${page}&page_size=50`, { signal: controller.signal })
      .then(next => { if (!controller.signal.aborted) setData(next); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); });
    return () => controller.abort();
  }, [page, retry]);
  return <div className="page"><PageHeader title="兑换码" description="只读查看已配置 Sub2API 库中的兑换码状态，兑换码已脱敏。" actions={<Link to="/overview">返回总览</Link>}/>{error ? <ErrorState message={error} retry={() => setRetry(value => value + 1)} /> : !data ? <PageLoader/>:<section className="panel"><div className="quality-table-scroll"><table><thead><tr><th>兑换码</th><th>类型</th><th>状态</th><th>额度</th><th>使用者</th><th>过期时间</th></tr></thead><tbody>{data.items.map(c=><tr key={c.id}><td>{c.code}</td><td>{c.type}</td><td><Badge tone={c.status==="unused"?"success":c.status==="expired"?"neutral":"warning"}>{c.status}</Badge></td><td>{c.value}</td><td>{c.used_by??"—"}</td><td>{formatDate(c.expires_at??undefined)}</td></tr>)}</tbody></table></div><div className="quality-actions"><Button disabled={page<=1} onClick={()=>setPage(page-1)}>上一页</Button><span>第 {page} 页 · 共 {data.total} 条</span><Button disabled={page * 50 >= data.total} onClick={()=>setPage(page+1)}>下一页</Button></div></section>}</div>}
