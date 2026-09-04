import {
  Activity,
  BellRing,
  Database,
  ExternalLink,
  Github,
  LayoutDashboard,
  Layers3,
  LogOut,
  Moon,
  Server,
  Sun,
  UsersRound,
} from "lucide-react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Link, NavLink } from "react-router-dom";
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

const commonLinks = [
  { to: "/", label: "质量", icon: Activity, end: true },
  { to: "/overview", label: "总览", icon: LayoutDashboard },
  { to: "/sites", label: "站点", icon: Server },
  { to: "/accounts", label: "账号", icon: Database },
  { to: "/groups", label: "分组", icon: Layers3 },
  { to: "/alerts", label: "预警", icon: BellRing },
  { to: "/events", label: "活动日志", icon: Activity },
];

type DockItem = `nav:${string}` | "github" | "theme" | "account";

function dockLabelStyle(label: string): CSSProperties {
  return { "--dock-label-width": `${Array.from(label).length}em` } as CSSProperties;
}

export function AppShell({ user, dark, onToggleTheme, onLogout, children }: AppShellProps) {
  const [expandedItem, setExpandedItem] = useState<DockItem | null>(null);
  const [accountOpen, setAccountOpen] = useState(false);
  const [versionStatus, setVersionStatus] = useState<VersionStatus | null>(null);
  const collapseTimer = useRef<number | null>(null);
  const links = user.role === "admin"
    ? [...commonLinks, { to: "/users", label: "用户", icon: UsersRound }]
    : commonLinks;
  const roleLabel = user.role === "admin" ? "管理员" : "用户";
  const githubLabel = versionStatus?.update_available && versionStatus.latest_version
    ? `发现新版本 ${versionStatus.latest_version}`
    : "GitHub";

  const clearCollapseTimer = useCallback(() => {
    if (collapseTimer.current !== null) {
      window.clearTimeout(collapseTimer.current);
      collapseTimer.current = null;
    }
  }, []);

  const expandDockItem = useCallback((item: DockItem) => {
    clearCollapseTimer();
    setExpandedItem(item);
  }, [clearCollapseTimer]);

  const scheduleDockCollapse = useCallback((item: DockItem) => {
    clearCollapseTimer();
    collapseTimer.current = window.setTimeout(() => {
      setExpandedItem((current) => current === item ? null : current);
      collapseTimer.current = null;
    }, 90);
  }, [clearCollapseTimer]);

  useEffect(() => clearCollapseTimer, [clearCollapseTimer]);

  useEffect(() => {
    let active = true;
    void api<VersionStatus>("/version")
      .then((status) => {
        if (active) setVersionStatus(status);
      })
      .catch(() => undefined);
    return () => { active = false; };
  }, []);

  const dockInteractions = (item: DockItem) => ({
    onPointerEnter: () => expandDockItem(item),
    onPointerLeave: () => scheduleDockCollapse(item),
    onFocus: () => expandDockItem(item),
    onBlur: () => scheduleDockCollapse(item),
  });

  return (
    <div className="app-shell">
      <header className="top-nav-shell">
        <div className="top-nav">
          <Brand />
          <span className="top-nav-divider" aria-hidden="true" />

          <nav className="nav-list" aria-label="主导航">
            {links.map(({ to, label, icon: Icon, ...link }) => {
              const item = `nav:${to}` as const;
              return (
                <NavLink
                  to={to}
                  end={"end" in link ? link.end : false}
                  className={({ isActive }) => cx("nav-link", isActive && "nav-link-active", expandedItem === item && "dock-item-expanded")}
                  style={dockLabelStyle(label)}
                  aria-label={label}
                  title={label}
                  key={to}
                  {...dockInteractions(item)}
                >
                  <Icon size={17} strokeWidth={1.8} aria-hidden="true" />
                  <span className="nav-link-label">{label}</span>
                </NavLink>
              );
            })}
          </nav>

          <span className="top-nav-divider" aria-hidden="true" />
          <div className="top-nav-actions">
            <a
              className={cx("top-nav-action", "top-nav-github", expandedItem === "github" && "dock-item-expanded")}
              href="https://github.com/langrenjh-alt/S2AM-GO"
              target="_blank"
              rel="noopener noreferrer"
              style={dockLabelStyle("GitHub")}
              aria-label={githubLabel}
              title={githubLabel}
              {...dockInteractions("github")}
            >
              <span className="top-nav-action-icon">
                <Github size={17} aria-hidden="true" />
                {versionStatus?.update_available && <span className="top-nav-update-dot" aria-hidden="true" />}
              </span>
              <span className="top-nav-action-label">GitHub</span>
            </a>

            <button
              className={cx("top-nav-action", expandedItem === "theme" && "dock-item-expanded")}
              type="button"
              style={dockLabelStyle(dark ? "浅色" : "暗色")}
              aria-label={dark ? "切换至浅色" : "切换至暗色"}
              title={dark ? "切换至浅色" : "切换至暗色"}
              onClick={onToggleTheme}
              {...dockInteractions("theme")}
            >
              {dark ? <Sun size={17} aria-hidden="true" /> : <Moon size={17} aria-hidden="true" />}
              <span className="top-nav-action-label">{dark ? "浅色" : "暗色"}</span>
            </button>

            <PopoverPrimitive.Root
              open={accountOpen}
              onOpenChange={(open) => {
                setAccountOpen(open);
                if (open) expandDockItem("account");
                else scheduleDockCollapse("account");
              }}
            >
              <PopoverPrimitive.Trigger asChild>
                <button
                  className={cx("top-nav-account-trigger", (expandedItem === "account" || accountOpen) && "dock-item-expanded")}
                  type="button"
                  style={dockLabelStyle("账户")}
                  aria-label={`账户：${user.email}，${roleLabel}`}
                  title={`${user.email} · ${roleLabel}`}
                  {...dockInteractions("account")}
                >
                  <span className="avatar" aria-hidden="true">{user.email.slice(0, 1).toUpperCase()}</span>
                  <span className="top-nav-action-label">账户</span>
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
                      <span>Upstream Manager <strong>{versionStatus.current_version}</strong></span>
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
    <Link className="brand top-nav-brand" to="/" aria-label="Upstream Manager" title="Upstream Manager">
      <span className="brand-mark"><span /></span>
      <span className="brand-name">Upstream<span> Manager</span></span>
    </Link>
  );
}
