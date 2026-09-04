import { useEffect, useRef, useState } from "react";
import { api, errorMessage } from "../api";
import { formatDate } from "../lib";
import { Badge, Button, PageLoader } from "./ui";

type Report = {version: string; checked_at: string; features: Record<string, {state: string; detail: string}>};
const names: Record<string, string> = {inventory_read: "库存读取", control_write: "控制写入", traffic_read: "真实请求接口", traffic_ttft: "真实首字字段", traffic_completion: "流结束字段", native_eligibility: "原生候选约束", source_identity: "来源身份"};
const states: Record<string, string> = {available: "已验证", partial: "部分可用", unknown: "未验证", unsupported: "未提供", error: "采集失败"};

export function SiteCapabilities({id, name, close}: {id: string; name: string; close: () => void}) {
  const root = useRef<HTMLElement>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => root.current?.focus(), []);
  const [data, setData] = useState<Report | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController(); setError("");
    api<Report>(`/sites/${id}/capabilities`, {signal: controller.signal}).then(setData).catch(e => {if (!controller.signal.aborted) setError(errorMessage(e));});
    return () => controller.abort();
  }, [id, attempt]);
  return <section ref={root} tabIndex={-1} className="quality-editor" aria-label={`${name} 接口能力`}>
    <div className="quality-section-heading"><h2>{name} · 接口能力</h2><Button onClick={close}>关闭能力详情</Button></div>
    {error ? <p role="alert" className="quality-error">{error}<Button onClick={() => setAttempt(value => value + 1)}>重试读取</Button></p> : !data ? <PageLoader/> : <>
      <p className="quality-note">版本：{data.version || "未知"} · 库存核对 {data.checked_at?.startsWith("0001") ? "尚未完成" : formatDate(data.checked_at)}。版本号仅作参考，是否可用取决于实际接口与字段。</p>
      <div className="quality-table-scroll"><table><thead><tr><th>能力</th><th>状态</th><th>依据与边界</th></tr></thead><tbody>{Object.entries(data.features).map(([key, item]) => <tr key={key}><td>{names[key] ?? key}</td><td><Badge tone={item.state === "available" ? "success" : item.state === "error" ? "danger" : "neutral"}>{states[item.state] ?? item.state}</Badge></td><td>{item.detail}</td></tr>)}</tbody></table></div>
    </>}
  </section>;
}
