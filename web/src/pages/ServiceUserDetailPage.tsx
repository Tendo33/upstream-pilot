import { ArrowLeft } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, errorMessage } from "../api";
import { formatFullDate } from "../lib";
import { Badge, ErrorState, PageHeader, PageLoader } from "../components/ui";

type Detail = { user: { id:number; username?:string; email:string; role:string; status:string; balance:number; concurrency:number; created_at:string; last_active_at?:string }; usage: { requests:number; input_tokens:number; output_tokens:number; cost:number; ips:number; tokens:number; last_seen?:string }; keys: { id:number; name:string; status:string; last_used_at?:string; quota:number; quota_used:number }[] };
export function ServiceUserDetailPage() {
  const { userID } = useParams(); const [data,setData]=useState<Detail|null>(null); const [error,setError]=useState("");
  useEffect(()=>{if(!userID)return; void api<Detail>(`/service-users/${userID}`).then(setData).catch(e=>setError(errorMessage(e)))},[userID]);
  if(error)return <div className="page"><ErrorState message={error} retry={()=>window.location.reload()}/></div>; if(!data)return <PageLoader/>;
  const u=data.user; return <div className="page"><PageHeader title={u.username||u.email} description={`服务用户 #${u.id} · ${u.email}`} actions={<Link className="button button-secondary" to="/users"><ArrowLeft size={16}/>返回用户</Link>}/>
    <div className="metric-grid"><article className="metric"><span>24 小时请求</span><strong>{data.usage.requests.toLocaleString()}</strong></article><article className="metric"><span>实际消费</span><strong>{Number(data.usage.cost||0).toFixed(4)}</strong></article><article className="metric"><span>关联 IP</span><strong>{data.usage.ips}</strong></article><article className="metric"><span>Token</span><strong>{data.usage.tokens}</strong></article></div>
    <section className="panel"><div className="panel-header"><h2>账户状态</h2></div><div className="detail-grid"><span>状态 <strong><Badge tone={u.status==="active"?"success":"neutral"}>{u.status}</Badge></strong></span><span>角色 <strong>{u.role}</strong></span><span>余额 <strong>{Number(u.balance||0).toFixed(4)}</strong></span><span>并发 <strong>{u.concurrency}</strong></span><span>最近活跃 <strong>{formatFullDate(u.last_active_at||u.created_at)}</strong></span><span>输入 / 输出 tokens <strong>{data.usage.input_tokens.toLocaleString()} / {data.usage.output_tokens.toLocaleString()}</strong></span></div></section>
    <section className="panel"><div className="panel-header"><h2>API Tokens</h2></div><div className="quality-table-scroll"><table><thead><tr><th>名称</th><th>状态</th><th>已用额度</th><th>额度</th><th>最近使用</th></tr></thead><tbody>{data.keys.map(k=><tr key={k.id}><td>{k.name||`Token #${k.id}`}</td><td><Badge tone={k.status==="active"?"success":"neutral"}>{k.status}</Badge></td><td>{Number(k.quota_used||0).toFixed(4)}</td><td>{Number(k.quota||0).toFixed(4)}</td><td>{formatFullDate(k.last_used_at)}</td></tr>)}</tbody></table></div></section>
  </div>;
}
