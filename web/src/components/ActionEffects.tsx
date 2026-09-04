import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, json } from "../api";
import { formatDate } from "../lib";
import { Badge, Button, ConfirmDialog } from "./ui";

type Action = {id: string; account_id: string; account_name: string; plan_id: string; source_generation: number; before_values: Record<string, unknown>; after_values: Record<string, unknown>; effect_status: string; effect_reason: string; window_seconds: number; created_at: string; has_managed_controls: boolean};
const fields: Record<string, string> = {priority: "优先级", schedulable: "调度开关", concurrency: "并发上限", load_factor: "负载系数"};
const effects: Record<string, string> = {unverified: "效果未验证", improved: "观测改善", unchanged: "无明显变化", regressed: "观测恶化"};
const value = (v: unknown) => typeof v === "boolean" ? v ? "开" : "关" : v == null ? "默认" : String(v);

export function ActionEffects() {
  const [actions, setActions] = useState<Action[]>([]), [error, setError] = useState("");
  const [restoring, setRestoring] = useState<Action | null>(null), [busy, setBusy] = useState(false);
  const load = useCallback(async () => {try {setActions(await api<Action[]>("/engine/actions")); setError("");} catch (e) {setError(errorMessage(e));}}, []);
  useEffect(() => {void load();}, [load]);
  async function restore() {
    if (!restoring) return; setBusy(true);
    try {await api(`/quality/${restoring.account_id}/release`, {method: "POST", ...json({restore: true})}); setRestoring(null); await load();}
    catch (e) {setError(errorMessage(e));} finally {setBusy(false);}
  }
  return <section className="quality-editor">
    <div className="quality-section-heading"><h2>动作与效果</h2><Button onClick={() => void load()}>刷新动作</Button></div>
    <p className="quality-note">参数读回成功后仍需观察服务结果。样本不足、档案变化或人工干预时保留“效果未验证”。恢复操作会回到该账号的人工基准并停止自动接管。</p>
    {error && <p role="alert" className="quality-error">{error}</p>}
    {actions.length === 0 ? <p className="quality-note">暂无确认完成的自动动作。</p> : actions.map(action => <article className="quality-notification" key={action.id}>
      <div><Badge tone={action.effect_status === "regressed" ? "danger" : action.effect_status === "improved" ? "success" : "neutral"}>{effects[action.effect_status] ?? action.effect_status}</Badge><strong>{action.account_name}</strong><span>{formatDate(action.created_at)}</span></div>
      <p>{Object.entries(action.after_values).map(([key, after]) => `${fields[key] ?? key} ${value(action.before_values[key])} → ${value(after)}`).join("；")}</p>
      <p className="quality-note">{action.effect_reason} · 观察窗口 {action.window_seconds} 秒 · 来源代次 {action.source_generation}</p>
      {action.has_managed_controls && <Button size="sm" disabled={busy} onClick={() => setRestoring(action)}>还原人工基准并停止</Button>}
    </article>)}
    <ConfirmDialog open={!!restoring} title="还原人工基准并停止" description={`将“${restoring?.account_name ?? ""}”仍由 Pilot 管理的优先级和容量字段恢复到人工基准，并停止自动接管。发现人工修改或站点地址变化时会停止恢复；调度启停需单独操作。`} confirmLabel="还原并停止" loading={busy} onConfirm={() => void restore()} onClose={() => !busy && setRestoring(null)}/>
  </section>;
}
