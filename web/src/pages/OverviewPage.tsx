import {
  Activity,
  ArrowUpRight,
  CheckCircle2,
  CirclePause,
  Database,
  RefreshCw,
  Server,
  ShieldAlert,
  Workflow,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import type { AuditEvent, AuditEventPage, Overview, Site } from "../types";
import { compactNumber, formatDate } from "../lib";
import { Badge, EmptyState, ErrorState, IconButton, PageHeader, PageLoader, useToast } from "../components/ui";

interface OverviewData {
  overview: Overview;
  events: AuditEvent[];
  sites: Site[];
}

const actionNames: Record<string, string> = {
  "site.create": "创建站点",
  "site.update": "更新站点",
  "site.test": "测试连接",
  "inventory.sync": "同步库存",
  "priority.reconcile": "重排优先级",
  "account.settings.update": "更新账号配置",
  "account.probe": "账号测活",
  "account.rate_sync": "同步账号倍率",
  "account.priority.reconcile": "调整账号优先级",
  "group.rate_rule.update": "更新分组倍率规则",
  "group.rate_rule.apply": "应用分组倍率规则",
  "account.settings": "更新账号配置",
  "probe.manual": "手动测活",
  "probe.scheduled": "定时测活",
  "rate_sync.manual": "手动同步倍率",
  "rate_sync.scheduled": "定时同步倍率",
  "user.create": "创建用户",
  "user.update": "更新用户",
  "user.delete": "删除用户",
  "audit_log.settings.update": "更新日志保留",
};

export function eventActionName(action: string): string {
  return actionNames[action] ?? action;
}

export function OverviewPage() {
  const [data, setData] = useState<OverviewData | null>(null);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [eventsLoading, setEventsLoading] = useState(true);
  const { toast } = useToast();

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setData(null);
    setError("");
    setRefreshing(quiet);
    try {
      const [overview, sites] = await Promise.all([
        api<Overview>("/overview"),
        api<Site[]>("/sites"),
      ]);
      setData((current) => current && quiet ? { ...current, overview, sites } : { overview, events: [], sites });
      setEventsLoading(true);
      try {
        const eventPage = await api<AuditEventPage>("/events?page=1&page_size=8");
        setData((current) => current ? { ...current, events: eventPage.items } : { overview, events: eventPage.items, sites });
      } catch {
        // Recent events are auxiliary; keep the rest of the overview visible.
      } finally {
        setEventsLoading(false);
      }
      if (quiet) toast("数据已刷新", "success");
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setRefreshing(false);
    }
  }, [toast]);

  useEffect(() => { void load(); }, [load]);

  if (!data && !error) return <PageLoader />;
  if (!data) return <ErrorState message={error} retry={() => void load()} />;

  const { overview, events, sites } = data;
  const healthTotal = overview.healthy + overview.failing + overview.paused;
  const healthPercent = healthTotal ? (overview.healthy / healthTotal) * 100 : 0;
  const failingPercent = healthTotal ? (overview.failing / healthTotal) * 100 : 0;
  const connected = sites.filter((site) => site.connection_state === "healthy").length;

  return (
    <div className="page">
      <PageHeader
        eyebrow="Workspace"
        title="运行总览"
        description="账号探测、倍率同步与优先级编排的实时状态"
        actions={
          <IconButton label="刷新总览" onClick={() => void load(true)} disabled={refreshing}>
            <RefreshCw size={17} className={refreshing ? "spin" : undefined} />
          </IconButton>
        }
      />

      <section className="metric-grid" aria-label="关键指标">
        <Metric label="站点" value={overview.sites} note={`${connected} 个连接正常`} icon={<Server size={18} />} />
        <Metric label="账号" value={overview.accounts} note={`${overview.automated} 个已启用自动化`} icon={<Database size={18} />} />
        <Metric label="运行健康" value={overview.healthy} note={`${overview.failing} 个探测异常`} icon={<CheckCircle2 size={18} />} tone={overview.failing ? "warning" : "success"} />
        <Metric label="近 24 小时失败" value={overview.recent_failures} note={`${overview.paused} 个已暂停调度`} icon={<ShieldAlert size={18} />} tone={overview.recent_failures ? "danger" : "neutral"} />
      </section>

      {overview.sites === 0 ? (
        <section className="overview-onboarding">
          <EmptyState
            title="添加第一个 Sub2API 站点"
            description="连接站点后即可同步账号库存并设置自动化策略。"
            action={<Link className="button button-primary button-md" to="/sites">添加站点 <ArrowUpRight size={15} /></Link>}
            icon={<Server size={21} />}
          />
        </section>
      ) : (
        <div className="overview-columns">
          <section className="panel health-panel">
            <div className="panel-heading">
              <div>
                <h2>探测状态</h2>
                <p>启用测活账号的当前状态</p>
              </div>
              <Link className="text-link" to="/accounts">查看账号 <ArrowUpRight size={14} /></Link>
            </div>
            <div className="health-score">
              <strong>{healthTotal ? Math.round(healthPercent) : 0}%</strong>
              <span>健康率</span>
            </div>
            <div className="health-bar" aria-label={`健康 ${overview.healthy}，异常 ${overview.failing}，暂停 ${overview.paused}`}>
              <span className="health-bar-success" style={{ width: `${healthPercent}%` }} />
              <span className="health-bar-danger" style={{ width: `${failingPercent}%` }} />
              <span className="health-bar-paused" />
            </div>
            <div className="health-legend">
              <div><span className="legend-dot success" /><strong>{overview.healthy}</strong><small>健康</small></div>
              <div><span className="legend-dot danger" /><strong>{overview.failing}</strong><small>异常</small></div>
              <div><span className="legend-dot warning" /><strong>{overview.paused}</strong><small>暂停</small></div>
            </div>
          </section>

          <section className="panel automation-panel">
            <div className="panel-heading">
              <div>
                <h2>自动化覆盖</h2>
                <p>至少启用一项账号级策略</p>
              </div>
              <Workflow size={18} />
            </div>
            <div className="automation-value">
              <strong>{overview.accounts ? Math.round((overview.automated / overview.accounts) * 100) : 0}%</strong>
              <span>{overview.automated} / {overview.accounts} 个账号</span>
            </div>
            <div className="progress-track"><span style={{ width: `${overview.accounts ? (overview.automated / overview.accounts) * 100 : 0}%` }} /></div>
            <div className="automation-note">
              <CirclePause size={16} />
              <span>四项策略完全独立，可按账号组合启用。</span>
            </div>
          </section>
        </div>
      )}

      <section className="panel recent-panel">
        <div className="panel-heading">
          <div>
            <h2>最近活动</h2>
            <p>工作区内最近发生的调度事件</p>
          </div>
          <Link className="text-link" to="/events">全部日志 <ArrowUpRight size={14} /></Link>
        </div>
        {events.length === 0 ? (
          eventsLoading ? (
            <p className="panel-loading">正在读取最近活动…</p>
          ) : (
            <EmptyState title="暂无活动" description="执行站点同步或账号操作后，事件会显示在这里。" icon={<Activity size={21} />} />
          )
        ) : (
          <div className="event-list compact-events">
            {events.map((event) => (
              <div className="event-row" key={event.id}>
                <span className={`event-indicator ${event.outcome}`} />
                <div className="event-copy">
                  <strong>{eventActionName(event.action)}</strong>
                  <span>{event.account_name || event.site_name || "工作区"}</span>
                </div>
                <Badge tone={event.outcome === "success" ? "success" : event.outcome === "failed" ? "danger" : "warning"}>
                  {event.outcome === "success" ? "成功" : event.outcome === "failed" ? "失败" : "跳过"}
                </Badge>
                <time title={new Date(event.created_at).toLocaleString("zh-CN")}>{formatDate(event.created_at)}</time>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function Metric({ label, value, note, icon, tone = "neutral" }: { label: string; value: number; note: string; icon: React.ReactNode; tone?: string }) {
  return (
    <article className={`metric metric-${tone}`}>
      <div className="metric-top"><span>{label}</span><span className="metric-icon">{icon}</span></div>
      <strong>{compactNumber(value)}</strong>
      <small>{note}</small>
    </article>
  );
}
