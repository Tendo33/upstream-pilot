import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { PageHeader, PageLoader } from "../components/ui";
type Row={site_id:string;site_name:string;success:number;failure:number;unknown:number;conflict:number};
export function RiskPage(){const [rows,setRows]=useState<Row[]|null>(null),[error,setError]=useState("");useEffect(()=>{void api<Row[]>("/operations/risk/summary").then(setRows).catch(e=>setError(errorMessage(e)))},[]);return <div className="page"><PageHeader title="运营风控" description="基于最终请求结果的只读风险摘要。" actions={<Link to="/overview">返回总览</Link>}/>{error&&<p className="quality-error" role="alert">{error}</p>}{!rows?<PageLoader/>:<section className="panel"><p className="quality-note">统计窗口：最近 24 小时。失败、未确认和冲突仅作为运营线索，不自动封禁用户。</p><div className="quality-table-scroll"><table><thead><tr><th>站点</th><th>成功</th><th>失败</th><th>未确认</th><th>冲突</th></tr></thead><tbody>{rows.map(r=><tr key={r.site_id}><td>{r.site_name}</td><td>{r.success}</td><td>{r.failure}</td><td>{r.unknown}</td><td>{r.conflict}</td></tr>)}</tbody></table></div></section>}</div>}
