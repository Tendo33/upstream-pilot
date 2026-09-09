import {
  Activity,
  BellRing,
  CreditCard,
  Database,
  ExternalLink,
  Github,
  LayoutDashboard,
  Layers3,
  LogOut,
  Moon,
  Server,
  ShieldAlert,
  ChevronDown,
  Check,
  Ticket,
  Sun,
  UsersRound,
  Zap,
} from "lucide-react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { api } from "../api";
import type { User, VersionStatus } from "../types";
import { cx } from "./ui";

interface AppShellProps {
  user: User;
  dark: boolean;
  onToggleTheme: () => void;
  onLogout: () => void;
  children: ReactNode;
}

const navigationGroups = [
  { id: "resources", label: "资源管理", links: [
    { to: "/sites", label: "站点", description: "接入与同步上游", icon: Server },
    { to: "/accounts", label: "账号与控制", description: "账号配置、自动策略与恢复", icon: Database },
    { to: "/groups", label: "分组", description: "分组策略与售价倍率", icon: Layers3 },
    { to: "/suppliers", label: "供应商与成本", description: "来源、采购成本与余额续航", icon: CreditCard },
  ] },
  { id: "business", label: "运营管理", links: [
    { to: "/overview", label: "总览", description: "站点运营概况", icon: LayoutDashboard },
    { to: "/billing", label: "充值审计", description: "核对充值记录", icon: CreditCard },
    { to: "/redemptions", label: "兑换码", description: "查看使用状态与额度", icon: Ticket },
    { to: "/risk", label: "运营风控", description: "检查运营风险", icon: ShieldAlert },
  ] },
  { id: "monitoring", label: "监控", links: [
    { to: "/service-checks", label: "服务探测", description: "验证模型、流式与工具能力", icon: Zap },
    { to: "/operations", label: "运行状态", description: "采集、任务与进程健康", icon: Activity },
    { to: "/notifications", label: "消息中心", description: "告警、订阅与投递回执", icon: BellRing },
    { to: "/events", label: "活动日志", description: "追溯操作与执行记录", icon: Activity },
  ] },
];

export function AppShell({ user, dark, onToggleTheme, onLogout, children }: AppShellProps) {
  const [openGroup, setOpenGroup] = useState<string | null>(null);
  const [accountOpen, setAccountOpen] = useState(false);
  const [versionStatus, setVersionStatus] = useState<VersionStatus | null>(null);
  const { pathname } = useLocation();

  useEffect(() => { setOpenGroup(null); setAccountOpen(false); }, [pathname]);
  const roleLabel = user.role === "admin" ? "管理员" : "用户";
  const githubLabel = versionStatus?.update_available && versionStatus.latest_version
    ? `发现新版本 ${versionStatus.latest_version}`
    : "GitHub";

  useEffect(() => {
    let active = true;
    void api<VersionStatus>("/version")
      .then((status) => {
        if (active) setVersionStatus(status);
      })
      .catch(() => undefined);
    return () => { active = false; };
  }, []);

  return (
    <div className="app-shell">
      <header className="top-nav-shell">
        <div className="top-nav">
          <Brand />
          <span className="top-nav-divider" aria-hidden="true" />

          <nav className="nav-list" aria-label="主导航">
            <NavLink to="/" end className={({ isActive }) => cx("nav-link", isActive && "nav-link-active")}>
              <Activity size={17} strokeWidth={1.8} aria-hidden="true" /><span>质量</span>
            </NavLink>
            {navigationGroups.map((group) => {
              const active = group.links.some((link) => link.to === pathname);
              return <PopoverPrimitive.Root key={group.id} open={openGroup === group.id}
                onOpenChange={(open) => { setOpenGroup(open ? group.id : null); if (open) setAccountOpen(false); }}>
                <PopoverPrimitive.Trigger asChild>
                  <button type="button" className={cx("nav-link", active && "nav-link-active")} aria-label={group.label}>
                    <span>{group.label}</span><ChevronDown className="nav-chevron" size={13} aria-hidden="true" />
                  </button>
                </PopoverPrimitive.Trigger>
                <PopoverPrimitive.Portal>
                  <PopoverPrimitive.Content className="nav-group-menu" aria-label={group.label} align="start" sideOffset={10} collisionPadding={12}>
                    <nav aria-label={group.label}>
                      {group.links.map(({ to, label, description, icon: Icon }) => <NavLink key={to} to={to}
                        onClick={() => setOpenGroup(null)}
                        className={({ isActive }) => cx("nav-group-link", isActive && "nav-group-link-active")}>
                        <Icon size={17} strokeWidth={1.8} aria-hidden="true" />
                        <span><strong>{label}</strong><small>{description}</small></span>
                        {pathname === to && <Check size={14} aria-hidden="true" />}
                      </NavLink>)}
                    </nav>
                  </PopoverPrimitive.Content>
                </PopoverPrimitive.Portal>
              </PopoverPrimitive.Root>;
            })}
          </nav>

          <span className="top-nav-divider" aria-hidden="true" />
          <div className="top-nav-actions">
            <a
              className="top-nav-action top-nav-github"
              href="https://github.com/Tendo33/upstream-pilot"
              target="_blank"
              rel="noopener noreferrer"

              aria-label={githubLabel}
              title={githubLabel}

            >
              <span className="top-nav-action-icon">
                <Github size={17} aria-hidden="true" />
                {versionStatus?.update_available && <span className="top-nav-update-dot" aria-hidden="true" />}
              </span>

            </a>

            <button
              className="top-nav-action"
              type="button"

              aria-label={dark ? "切换至浅色" : "切换至暗色"}
              title={dark ? "切换至浅色" : "切换至暗色"}
              onClick={onToggleTheme}

            >
              {dark ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}

            </button>

            <PopoverPrimitive.Root
              open={accountOpen}
              onOpenChange={(open) => {
                setAccountOpen(open);
                if (open) setOpenGroup(null);
              }}
            >
              <PopoverPrimitive.Trigger asChild>
                <button
                  className="top-nav-account-trigger"
                  type="button"

                  aria-label={`账户：${user.email}，${roleLabel}`}
                  title={`${user.email} · ${roleLabel}`}

                >
                  <span className="avatar" aria-hidden="true">{user.email.slice(0, 1).toUpperCase()}</span>

                </button>
              </PopoverPrimitive.Trigger>
              <PopoverPrimitive.Portal>
                <PopoverPrimitive.Content
                  className="top-nav-account-menu"
                  align="end"
                  sideOffset={10}
                  collisionPadding={12}
                >
                  <div className="top-nav-account-profile">
                    <span className="avatar" aria-hidden="true">{user.email.slice(0, 1).toUpperCase()}</span>
                    <span>
                      <strong title={user.email}>{user.email}</strong>
                      <small>{roleLabel}</small>
                    </span>
                  </div>
                  {user.role === "admin" && <Link className="nav-account-users" to="/users" onClick={() => setAccountOpen(false)}>
                    <UsersRound size={16} aria-hidden="true" /><span>用户管理</span>
                  </Link>}
                  {versionStatus && (
                    <a
                      className="top-nav-version"
                      href={versionStatus.update_available && versionStatus.release_url
                        ? versionStatus.release_url
                        : `${versionStatus.repository_url}/releases`}
                      target="_blank"
                      rel="noopener noreferrer"
                      title={`提交 ${versionStatus.commit} · 构建时间 ${versionStatus.build_time}`}
                    >
                      <span>Upstream Pilot <strong>{versionStatus.current_version}</strong></span>
                      {versionStatus.update_available && versionStatus.latest_version && (
                        <span className="top-nav-version-update">
                          可更新至 {versionStatus.latest_version}
                          <ExternalLink size={12} aria-hidden="true" />
                        </span>
                      )}
                    </a>
                  )}
                  <button className="top-nav-logout" type="button" onClick={onLogout}>
                    <LogOut size={16} aria-hidden="true" />
                    <span>退出登录</span>
                  </button>
                  <PopoverPrimitive.Arrow className="top-nav-account-arrow" width={12} height={6} />
                </PopoverPrimitive.Content>
              </PopoverPrimitive.Portal>
            </PopoverPrimitive.Root>
          </div>
        </div>
      </header>

      <main className="main-content">{children}</main>
    </div>
  );
}

function Brand() {
  return (
    <Link className="brand top-nav-brand" to="/" aria-label="Upstream Pilot" title="Upstream Pilot">
      <svg className="pilot-mark" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="9" fill="currentColor"/><path d="M8 23V18L16 13M24 23V18L16 13M16 24V8M11 12L16 7L21 12" fill="none" stroke="var(--surface)" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"/></svg>
      <span className="brand-name">Upstream<span> Pilot</span></span>
    </Link>
  );
}
