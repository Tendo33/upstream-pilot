import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage, json } from "../api";
import { Badge, Button, Field, Input, PageHeader, PageLoader } from "../components/ui";

type Operations = {provider: string; failure_domain: string; quota_pool: string; confirmed: boolean; notes: string; multiplier_basis: string; runway_warn_hours: number | null};
type Supplier = {id: string; name: string; source_generation: number; source_hint: string; current_source: boolean; independent_known: boolean; component: string; config: Operations};
type Card = {currency: string; currency_to_usd: number; token_unit: number; input: number; output: number; cache_read: number | null; cache_write: number | null; token_convention: string; apply_multiplier: boolean; basis: string; confirmed: boolean; valid_until: string};
type SavedCard = {account_id: string; model: string; current_source: boolean; config: Card};
type Runway = {status: string; reason: string; hours_low: number | null; hours_high: number | null; unit: string; samples: number; quota_reset_at: string | null};
type Economics = {cards: SavedCard[]; runways: Record<string, Runway>};

export function SuppliersPage() {
  const [rows, setRows] = useState<Supplier[] | null>(null), [economics, setEconomics] = useState<Economics>({cards: [], runways: {}}), [error, setError] = useState("");
  const [editing, setEditing] = useState<Supplier | null>(null), [pricing, setPricing] = useState<{supplier: Supplier; card?: SavedCard} | null>(null);
  const load = useCallback(async () => {try {const [suppliers, data] = await Promise.all([api<Supplier[]>("/suppliers"), api<Economics>("/economics")]); setRows(suppliers); setEconomics(data); setError("");} catch (e) {setError(errorMessage(e));}}, []);
  useEffect(() => {void load();}, [load]);
  return <div className="page">
    <PageHeader title="供应商与成本" description="确认共同故障关系，统一模型价格，关注余额续航。" actions={<div className="quality-actions"><Link to="/">返回质量页</Link><Button onClick={() => void load()}>刷新</Button></div>}/>
    <p className="quality-note">不同域名不代表供应商独立。同一供应商、失效域、额度池或来源凭据会合并计数；未确认及换源后的关联不算独立备用。模型采购价与用户售价分开维护。</p>
    {error && <p className="quality-error" role="alert">{error}</p>}
    {editing && <OperationsEditor key={editing.id} row={editing} close={() => setEditing(null)} saved={() => {setEditing(null); void load();}}/>}
    {pricing && <PriceEditor key={`${pricing.supplier.id}/${pricing.card?.model ?? "new"}`} {...pricing} close={() => setPricing(null)} saved={() => {setPricing(null); void load();}}/>}
    {!rows && !error ? <PageLoader/> : rows?.length === 0 ? <p>同步站点账号后，可在这里登记供应商和模型价格。</p> : rows?.map(row => {
      const runway = economics.runways[row.id]; const cards = economics.cards.filter(card => card.account_id === row.id);
      return <article key={row.id} className="service-profile-row">
        <div className="quality-section-heading"><h2>{row.name}</h2><Badge tone={row.independent_known ? "success" : "neutral"}>{row.independent_known ? "关联已确认" : "关联待确认"}</Badge></div>
        <p>{row.config.provider || "供应商未知"} · 失效域 {row.config.failure_domain || "未知"} · 额度池 {row.config.quota_pool || "未知"}</p>
        <p className="quality-note">来源线索：{row.source_hint || "未提供"}{!row.current_source && row.config.confirmed ? " · 来源已变化，请重新核对" : ""}</p>
        <p>余额续航：{runway?.status === "estimated" ? `${runway.hours_low?.toFixed(1)}–${runway.hours_high?.toFixed(1)} 小时（${runway.unit}，${runway.samples} 个样本）` : "未知"}</p>
        <p className="quality-note">{runway?.reason ?? "等待同来源余额消耗样本"}{runway?.quota_reset_at ? ` · 上报额度重置 ${new Date(runway.quota_reset_at).toLocaleString()}` : ""}</p>
        {cards.map(card => <div className="service-profile-facts" key={card.model}><strong>{card.model}</strong><span>{card.config.currency} / {card.config.token_unit.toLocaleString()} tokens</span><span>输入 {card.config.input} · 输出 {card.config.output}</span><span>{card.current_source && card.config.confirmed && Date.parse(card.config.valid_until) > Date.now() ? "价格配置有效" : "价格待确认或已过期"}</span><Button size="sm" onClick={() => {setPricing({supplier: row, card}); setEditing(null);}}>编辑价格</Button></div>)}
        <div className="quality-actions"><Button onClick={() => {setEditing(row); setPricing(null);}}>编辑关联</Button><Button onClick={() => {setPricing({supplier: row}); setEditing(null);}}>录入模型价格</Button></div>
      </article>;
    })}
  </div>;
}

function OperationsEditor({row, close, saved}: {row: Supplier; close: () => void; saved: () => void}) {
  const [config, setConfig] = useState<Operations>({...row.config, confirmed: row.current_source && row.config.confirmed}), [error, setError] = useState(""), [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {e.preventDefault(); setBusy(true); try {await api(`/suppliers/${row.id}`, {method: "PUT", ...json({source_generation: row.source_generation, config})}); saved();} catch (e) {setError(errorMessage(e));} finally {setBusy(false);}}
  return <form className="quality-editor" onSubmit={e => void submit(e)}><div className="quality-section-heading"><h2>{row.name} · 供应商关联</h2><Button disabled={busy} onClick={close}>取消</Button></div>
    <div className="quality-form-grid">{([['provider', '供应商标识'], ['failure_domain', '共同失效域'], ['quota_pool', '共享额度池']] as const).map(([key, label]) => <Field label={label} key={key} hint="共享同一关系的账号填写相同标识。"><Input maxLength={120} value={config[key] ?? ""} onChange={e => setConfig({...config, [key]: e.target.value})}/></Field>)}
      <Field label="倍率共同计价基础" hint="仅在确实采用同一模型价格基础时填写相同标识；否则留空。"><Input maxLength={256} value={config.multiplier_basis ?? ""} onChange={e => setConfig({...config, multiplier_basis: e.target.value})}/></Field>
      <Field label="续航预警阈值（小时）" hint="0 关闭；默认低于 6 小时提醒准备余额或备用。"><Input type="number" min={0} max={720} step="any" value={config.runway_warn_hours ?? 6} onChange={e => setConfig({...config, runway_warn_hours: Number(e.target.value)})}/></Field>
      <Field label="核对说明"><Input maxLength={2000} value={config.notes ?? ""} onChange={e => setConfig({...config, notes: e.target.value})}/></Field></div>
    <label className="quality-checkbox-line"><input type="checkbox" checked={!!config.confirmed} onChange={e => setConfig({...config, confirmed: e.target.checked})}/>我已核对供应商、失效域及额度池关系</label>
    {error && <p role="alert" className="quality-error">{error}</p>}<Button type="submit" variant="primary" loading={busy}>保存关联</Button>
  </form>;
}

function PriceEditor({supplier, card: savedCard, close, saved}: {supplier: Supplier; card?: SavedCard; close: () => void; saved: () => void}) {
  const [model, setModel] = useState(savedCard?.model ?? ""), [card, setCard] = useState<Card>(() => savedCard ? {...savedCard.config, confirmed: savedCard.current_source && savedCard.config.confirmed} : {currency: "USD", currency_to_usd: 1, token_unit: 1000000, input: 0, output: 0, cache_read: null, cache_write: null, token_convention: "disjoint", apply_multiplier: false, basis: "", confirmed: false, valid_until: new Date(Date.now() + 7 * 86400000).toISOString()});
  const [error, setError] = useState(""), [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {e.preventDefault(); setBusy(true); try {await api(`/economics/${supplier.id}`, {method: "PUT", ...json({model, source_generation: supplier.source_generation, card})}); saved();} catch (e) {setError(errorMessage(e));} finally {setBusy(false);}}
  return <form className="quality-editor" onSubmit={e => void submit(e)}><div className="quality-section-heading"><h2>{supplier.name} · 模型采购价格</h2><Button disabled={busy} onClick={close}>取消</Button></div><p className="quality-note">模型名称对应此账号在分组中的对外模型名。按已确认价格、站点用量结构及账号充值比例计算采购预估，不读取用户账单作为采购成本。</p>
    <div className="quality-form-grid">
      <Field label="对外模型名"><Input required maxLength={256} disabled={!!savedCard} value={model} onChange={e => setModel(e.target.value)}/></Field>
      <Field label="报价币种"><Input required maxLength={3} value={card.currency} onChange={e => setCard({...card, currency: e.target.value.toUpperCase()})}/></Field>
      <Field label="1 单位报价币种对应 USD"><Input required type="number" min={0.000000001} step="any" value={card.currency_to_usd} onChange={e => setCard({...card, currency_to_usd: Number(e.target.value)})}/></Field>
      <Field label="价格 token 单位"><select value={card.token_unit} onChange={e => setCard({...card, token_unit: Number(e.target.value)})}><option value={1000000}>每百万 tokens</option><option value={1000}>每千 tokens</option></select></Field>
      {([['input', '输入价格'], ['output', '输出价格'], ['cache_read', '缓存读取价格'], ['cache_write', '缓存写入价格']] as const).map(([key, label]) => <Field key={key} label={label} hint={key.startsWith('cache') ? "未知留空，免费填写 0。" : undefined}><Input type="number" min={0} step="any" required={!key.startsWith('cache')} value={card[key] ?? ""} onChange={e => setCard({...card, [key]: e.target.value === "" ? null : Number(e.target.value)})}/></Field>)}
      <Field label="本站 usage token 口径"><select value={card.token_convention} onChange={e => setCard({...card, token_convention: e.target.value})}><option value="disjoint">输入与缓存是互斥计数</option><option value="input_includes_cache">输入包含缓存，需要扣除</option></select></Field>
      <Field label="价格依据 / 版本"><Input required maxLength={256} value={card.basis} onChange={e => setCard({...card, basis: e.target.value})}/></Field>
      <Field label="有效截止日（本地时间）"><Input type="date" required value={card.valid_until.slice(0, 10)} onChange={e => {if (e.target.value) setCard({...card, valid_until: new Date(`${e.target.value}T23:59:59`).toISOString()});}}/></Field>
    </div>
    <div className="quality-checkboxes"><label><input type="checkbox" checked={card.apply_multiplier} onChange={e => setCard({...card, apply_multiplier: e.target.checked})}/>报价还需乘以采集到的采购倍率（已含倍率时不要勾选）</label><label><input type="checkbox" checked={card.confirmed} onChange={e => setCard({...card, confirmed: e.target.checked})}/>我已核对价格、计数口径和换算；账号充值比例仍会单独折算</label></div>
    {error && <p role="alert" className="quality-error">{error}</p>}<Button type="submit" variant="primary" loading={busy}>保存模型价格</Button>
  </form>;
}
