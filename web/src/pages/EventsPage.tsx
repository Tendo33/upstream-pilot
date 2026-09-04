import { Activity, ChevronDown, ChevronLeft, ChevronRight, ListFilter, RefreshCw, Rows3, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, errorMessage, json } from "../api";
import { formatDate, formatFullDate } from "../lib";
import type { AuditEventPage, AuditLogSettings } from "../types";
import { Badge, Button, ConfirmDialog, EmptyState, ErrorState, IconButton, Input, PageHeader, PageLoader, SelectMenu, cx, useToast } from "../components/ui";
import { eventActionName } from "./OverviewPage";

const retentionPresets = [7, 14, 30];

export function EventsPage() {
  const [eventPage, setEventPage] = useState<AuditEventPage | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [outcome, setOutcome] = useState("all");
  const [expanded, setExpanded] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [retention, setRetention] = useState<AuditLogSettings | null>(null);
  const [retentionDays, setRetentionDays] = useState(14);
  const [savingRetention, setSavingRetention] = useState(false);
  const [confirmingPurge, setConfirmingPurge] = useState(false);
  const { toast } = useToast();

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setEventPage(null);
    setError("");
    setRefreshing(true);
    try {
      const result = await api<AuditEventPage>(`/events?page=${page}&page_size=${pageSize}`);
      if (result.total_pages > 0 && result.page > result.total_pages) {
        setPage(result.total_pages);
        return;
      }
      setEventPage(result);
      setExpanded("");
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setRefreshing(false);
    }
  }, [page, pageSize]);

  useEffect(() => { void load(); }, [load]);

  const loadRetention = useCallback(async () => {
    try {
      const settings = await api<AuditLogSettings>("/settings/audit-log");
      setRetention(settings);
      setRetentionDays(settings.retention_days);
    } catch (cause) {
      toast(errorMessage(cause), "error");
    }
  }, [toast]);

  useEffect(() => { void loadRetention(); }, [loadRetention]);

  async function saveRetention() {
    if (retentionDays < 1 || retentionDays > 365) {
      toast("日志保留天数必须在 1 到 365 之间", "error");
      return;
    }
    setSavingRetention(true);
    try {
      const settings = await api<AuditLogSettings>("/settings/audit-log", {
        method: "PUT",
        ...json({ retention_days: retentionDays }),
      });
      setRetention(settings);
      setRetentionDays(settings.retention_days);
      setConfirmingPurge(false);
      toast(`已保留最近 ${settings.retention_days} 天，清理 ${settings.last_purge_removed_files} 个日志文件 / ${settings.last_purge_removed_records} 条记录`, "success");
      await load(true);
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSavingRetention(false);
    }
  }

  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    return (eventPage?.items ?? []).filter((event) => {
      if (outcome !== "all" && event.outcome !== outcome) return false;
      if (!query) return true;
      return [event.action, eventActionName(event.action), event.site_name, event.account_name, JSON.stringify(event.detail)]
        .some((value) => value.toLowerCase().includes(query));
    });
  }, [eventPage, outcome, search]);

  if (!eventPage && !error) return <PageLoader />;
  if (!eventPage) return <ErrorState message={error} retry={() => void load()} />;

  return (
    <div className="page">
      <PageHeader
        eyebrow="Audit"
        title="活动日志"
        description="站点、账号自动化与用户管理的审计记录"
        actions={<IconButton label="刷新日志" onClick={() => void load(true)} disabled={refreshing}><RefreshCw size={17} className={refreshing ? "spin" : undefined} /></IconButton>}
      />

      <div className="filter-bar events-filter">
        <div className="search-box"><Search size={16} /><Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索当前页的操作或对象" aria-label="搜索当前页活动日志" /></div>
        <SelectMenu
          className="select-control-filter"
          label="当前页执行结果"
          icon={<ListFilter size={15} />}
          value={outcome}
          onChange={setOutcome}
          options={[
            { value: "all", label: "全部结果" },
            { value: "success", label: "成功" },
            { value: "failed", label: "失败" },
            { value: "skipped", label: "跳过" },
          ]}
        />
        <SelectMenu
          className="select-control-filter"
          label="每页数量"
          icon={<Rows3 size={15} />}
          value={String(pageSize)}
          onChange={(value) => { setPageSize(Number(value)); setPage(1); }}
          options={[25, 50, 100, 200].map((size) => ({ value: String(size), label: `每页 ${size} 条` }))}
        />
        <span className="result-count">当前页 {visible.length} / {eventPage.items.length}{eventPage.has_next ? " · 还有更多" : ` · 共 ${eventPage.total} 条`}</span>
      </div>

      <div className="events-retention">
        <span>日志保留</span>
        <div className="segmented" role="group" aria-label="日志保留天数">
          {retentionPresets.map((days) => (
            <button type="button" key={days} className={retentionDays === days ? "active" : ""} onClick={() => setRetentionDays(days)}>{days} 天</button>
          ))}
          <button type="button" className={!retentionPresets.includes(retentionDays) ? "active" : ""} onClick={() => setRetentionDays(retentionPresets.includes(retentionDays) ? 90 : retentionDays)}>自定义</button>
        </div>
        {!retentionPresets.includes(retentionDays) ? (
          <label className="events-retention-custom">
            <Input type="number" min={1} max={365} value={retentionDays} onChange={(event) => setRetentionDays(Number(event.target.value))} aria-label="自定义保留天数" />
            <span>天</span>
          </label>
        ) : null}
        <Button size="sm" variant="primary" onClick={() => setConfirmingPurge(true)} disabled={savingRetention} loading={savingRetention}>保存并清理</Button>
        <span className="events-retention-meta">
          {retention?.configured
            ? `当前 ${retention.retention_days} 天${retention.last_purged_at ? ` · 上次清理 ${formatDate(retention.last_purged_at)}` : ""}`
            : "保存后按此天数删除更早的日志，并每天自动清理"}
        </span>
      </div>

      {visible.length === 0 ? (
        <section className="panel"><EmptyState title="暂无匹配活动" description={eventPage.total ? "调整搜索条件后重试。" : "执行操作后，审计记录会显示在这里。"} icon={<Activity size={21} />} /></section>
      ) : (
        <div className="events-table">
          <div className="events-head"><span>操作</span><span>对象</span><span>结果</span><span>时间</span><span /></div>
          {visible.map((event) => {
            const hasDetail = Object.keys(event.detail ?? {}).length > 0;
            const open = expanded === event.id;
            return (
              <div className={cx("event-entry", open && "event-entry-open")} key={event.id}>
                <div className="event-entry-main">
                  <div className="event-action"><span className={`event-indicator ${event.outcome}`} /><div><strong>{eventActionName(event.action)}</strong><small>{event.action}</small></div></div>
                  <div className="event-target"><strong>{event.account_name || event.site_name || "工作区"}</strong><span>{event.account_name && event.site_name ? event.site_name : "-"}</span></div>
                  <div><Badge tone={event.outcome === "success" ? "success" : event.outcome === "failed" ? "danger" : "warning"}>{event.outcome === "success" ? "成功" : event.outcome === "failed" ? "失败" : "跳过"}</Badge></div>
                  <time>{formatFullDate(event.created_at)}</time>
                  <IconButton label={open ? "收起详情" : "查看详情"} onClick={() => hasDetail && setExpanded(open ? "" : event.id)} disabled={!hasDetail}>{open ? <ChevronDown size={16} /> : <ChevronRight size={16} />}</IconButton>
                </div>
                {open ? <pre className="event-detail">{JSON.stringify(event.detail, null, 2)}</pre> : null}
              </div>
            );
          })}
        </div>
      )}

      <nav className="pagination-bar" aria-label="活动日志分页">
        <span>第 {eventPage.page} 页{eventPage.has_next ? "" : eventPage.total_pages ? ` / ${eventPage.total_pages}` : ""}</span>
        <div>
          <IconButton label="上一页" disabled={!eventPage.has_previous || refreshing} onClick={() => setPage((current) => Math.max(1, current - 1))}>
            <ChevronLeft size={16} />
          </IconButton>
          <IconButton label="下一页" disabled={!eventPage.has_next || refreshing} onClick={() => setPage((current) => current + 1)}>
            <ChevronRight size={16} />
          </IconButton>
        </div>
      </nav>

      <ConfirmDialog
        open={confirmingPurge}
        title="按保留天数清理日志"
        description={`将保留最近 ${retentionDays} 天的活动日志，更早的记录会被删除且不可恢复。`}
        confirmLabel="保存并清理"
        danger
        loading={savingRetention}
        onConfirm={() => void saveRetention()}
        onClose={() => !savingRetention && setConfirmingPurge(false)}
      />
    </div>
  );
}
