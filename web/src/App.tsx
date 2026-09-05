import { AlertCircle, LoaderCircle, RefreshCw } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useNavigate } from "react-router-dom";
import { api, errorMessage, isAuthError, json } from "./api";
import { AppShell } from "./components/AppShell";
import { Button, PageLoader, ToastProvider } from "./components/ui";
import { RouteBoundary } from "./components/RouteBoundary";
const OperationsPage = lazy(() => import("./pages/OperationsPage").then(module => ({default: module.OperationsPage})));
const SuppliersPage = lazy(() => import("./pages/SuppliersPage").then(module => ({default: module.SuppliersPage})));
const ServiceChecksPage = lazy(() => import("./pages/ServiceChecksPage").then(module => ({default: module.ServiceChecksPage})));
import { QualityPage } from "./pages/QualityPage";
const MessageCenterPage = lazy(() => import("./pages/MessageCenterPage").then(module => ({default: module.MessageCenterPage})));
import { AccountsPage } from "./pages/AccountsPage";
import { AuthPage } from "./pages/AuthPage";
import { EventsPage } from "./pages/EventsPage";
import { GroupsPage } from "./pages/GroupsPage";
import { OverviewPage } from "./pages/OverviewPage";
import { SitesPage } from "./pages/SitesPage";
import { UsersPage } from "./pages/UsersPage";
import type { SetupStatus, User } from "./types";

type BootState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; initialized: boolean; user: User | null };

export default function App() {
  const [boot, setBoot] = useState<BootState>({ status: "loading" });
  const [dark, setDark] = useState(() => {
    const stored = localStorage.getItem("pilot-theme");
    return stored ? stored === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  });

  const bootstrap = useCallback(async () => {
    setBoot({ status: "loading" });
    try {
      const setup = await api<SetupStatus>("/setup/status");
      if (!setup.initialized) {
        setBoot({ status: "ready", initialized: false, user: null });
        return;
      }
      try {
        const user = await api<User>("/auth/me");
        setBoot({ status: "ready", initialized: true, user });
      } catch (cause) {
        if (isAuthError(cause)) setBoot({ status: "ready", initialized: true, user: null });
        else throw cause;
      }
    } catch (cause) {
      setBoot({ status: "error", message: errorMessage(cause) });
    }
  }, []);

  useEffect(() => { void bootstrap(); }, [bootstrap]);
  useEffect(() => {
    const sessionExpired = () => setBoot((current) => current.status === "ready" && current.initialized
      ? { status: "ready", initialized: true, user: null }
      : current);
    window.addEventListener("pilot:session-expired", sessionExpired);
    return () => window.removeEventListener("pilot:session-expired", sessionExpired);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    localStorage.setItem("pilot-theme", dark ? "dark" : "light");
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", dark ? "#000000" : "#ffffff");
  }, [dark]);

  if (boot.status === "loading") return <BootLoader />;
  if (boot.status === "error") return <BootError message={boot.message} retry={() => void bootstrap()} />;

  async function authenticate(email: string, password: string): Promise<User> {
    const path = boot.status === "ready" && boot.initialized ? "/auth/login" : "/setup";
    const user = await api<User>(path, { method: "POST", ...json({ email, password }) });
    setBoot({ status: "ready", initialized: true, user });
    return user;
  }

  if (!boot.user) return <AuthPage initialized={boot.initialized} submit={authenticate} />;

  return (
    <ToastProvider>
      <BrowserRouter>
        <AuthenticatedApp user={boot.user} dark={dark} setDark={setDark} onSessionEnded={() => setBoot({ status: "ready", initialized: true, user: null })} />
      </BrowserRouter>
    </ToastProvider>
  );
}

function AuthenticatedApp({ user, dark, setDark, onSessionEnded }: { user: User; dark: boolean; setDark: (value: boolean) => void; onSessionEnded: () => void }) {
  const navigate = useNavigate();
  async function logout() {
    try {
      await api<{ logged_out: boolean }>("/auth/logout", { method: "POST" });
    } catch {
      // Local session state must still be cleared when the server is unreachable.
    } finally {
      onSessionEnded();
      navigate("/");
    }
  }

  return (
    <AppShell user={user} dark={dark} onToggleTheme={() => setDark(!dark)} onLogout={() => void logout()}>
      <Routes>
        <Route path="/" element={<QualityPage />} />
        <Route path="/overview" element={<OverviewPage />} />
        <Route path="/operations" element={<RouteBoundary><Suspense fallback={<PageLoader/>}><OperationsPage/></Suspense></RouteBoundary>}/>
        <Route path="/suppliers" element={<RouteBoundary><Suspense fallback={<PageLoader/>}><SuppliersPage/></Suspense></RouteBoundary>}/>
        <Route path="/service-checks" element={<RouteBoundary><Suspense fallback={<PageLoader/>}><ServiceChecksPage /></Suspense></RouteBoundary>} />
        <Route path="/notifications" element={<RouteBoundary><Suspense fallback={<PageLoader/>}><MessageCenterPage/></Suspense></RouteBoundary>} />
        <Route path="/quality-alerts" element={<Navigate to="/notifications" replace />} />
        <Route path="/sites" element={<SitesPage />} />
        <Route path="/accounts" element={<AccountsPage />} />
        <Route path="/groups" element={<GroupsPage />} />
        <Route path="/events" element={<EventsPage />} />
        <Route path="/alerts" element={<Navigate to="/notifications" replace />} />
        <Route path="/users" element={user.role === "admin" ? <UsersPage currentUser={user} /> : <Navigate to="/" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </AppShell>
  );
}

function BootLoader() {
  return (
    <main className="boot-screen">
      <div className="boot-brand"><span className="brand-mark"><span /></span><span className="brand-name">Upstream<span> Pilot</span></span></div>
      <LoaderCircle className="spin" size={19} aria-label="正在连接服务器" />
    </main>
  );
}

function BootError({ message, retry }: { message: string; retry: () => void }) {
  return (
    <main className="boot-screen">
      <div className="boot-error-icon"><AlertCircle size={21} /></div>
      <h1>无法打开控制台</h1>
      <p>{message}</p>
      <Button onClick={retry}><RefreshCw size={15} />重新连接</Button>
    </main>
  );
}
