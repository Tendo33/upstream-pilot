import { AlertCircle, LoaderCircle, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes, useNavigate } from "react-router-dom";
import { api, errorMessage, isAuthError, json } from "./api";
import { AppShell } from "./components/AppShell";
import { Button, ToastProvider } from "./components/ui";
import { AccountsPage } from "./pages/AccountsPage";
import { AuthPage } from "./pages/AuthPage";
import { BalanceAlertsPage } from "./pages/BalanceAlertsPage";
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
    const stored = localStorage.getItem("s2am-theme");
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
    window.addEventListener("s2am:session-expired", sessionExpired);
    return () => window.removeEventListener("s2am:session-expired", sessionExpired);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    localStorage.setItem("s2am-theme", dark ? "dark" : "light");
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
      <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
        <Route path="/" element={<OverviewPage />} />
        <Route path="/sites" element={<SitesPage />} />
        <Route path="/accounts" element={<AccountsPage />} />
        <Route path="/groups" element={<GroupsPage />} />
        <Route path="/events" element={<EventsPage />} />
        <Route path="/alerts" element={<BalanceAlertsPage />} />
        <Route path="/users" element={user.role === "admin" ? <UsersPage currentUser={user} /> : <Navigate to="/" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </AppShell>
  );
}

function BootLoader() {
  return (
    <main className="boot-screen">
      <div className="boot-brand"><span className="brand-mark"><span /></span><span className="brand-name">S2AM<span>-GO</span></span></div>
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
