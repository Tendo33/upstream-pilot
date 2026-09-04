import { ArrowRight, Eye, EyeOff, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { errorMessage } from "../api";
import type { User } from "../types";
import { Button, Field, IconButton, Input } from "../components/ui";

interface AuthPageProps {
  initialized: boolean;
  submit: (email: string, password: string) => Promise<User>;
}

export function AuthPage({ initialized, submit }: AuthPageProps) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [visible, setVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!email.trim() || !password) {
      setError("请填写邮箱和密码");
      return;
    }
    if (!initialized && password.length < 10) {
      setError("管理员密码至少需要 10 个字符");
      return;
    }
    setLoading(true);
    try {
      await submit(email.trim(), password);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="auth-page">
      <header className="auth-brand">
        <span className="brand-mark"><span /></span>
        <span className="brand-name">S2AM<span>-GO</span></span>
      </header>
      <section className="auth-panel">
        <div className="auth-icon"><ShieldCheck size={21} /></div>
        <h1>{initialized ? "登录控制台" : "初始化 S2AM-GO"}</h1>
        <p>{initialized ? "使用你的工作区账号继续" : "创建首个管理员账号"}</p>
        <form onSubmit={onSubmit} className="auth-form">
          <Field label="邮箱" required>
            <Input
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              autoComplete="email"
              placeholder="admin@example.com"
              autoFocus
              disabled={loading}
            />
          </Field>
          <Field label="密码" hint={!initialized ? "10 至 128 个字符" : undefined} required>
            <div className="input-with-action">
              <Input
                type={visible ? "text" : "password"}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete={initialized ? "current-password" : "new-password"}
                placeholder="输入密码"
                minLength={initialized ? undefined : 10}
                maxLength={128}
                disabled={loading}
              />
              <IconButton label={visible ? "隐藏密码" : "显示密码"} type="button" onClick={() => setVisible((current) => !current)}>
                {visible ? <EyeOff size={16} /> : <Eye size={16} />}
              </IconButton>
            </div>
          </Field>
          {error ? <div className="form-error" role="alert">{error}</div> : null}
          <Button className="auth-submit" variant="primary" type="submit" loading={loading}>
            {initialized ? "登录" : "创建管理员"}
            {!loading ? <ArrowRight size={16} /> : null}
          </Button>
        </form>
      </section>
      <footer className="auth-footer">
        <span>Listening on :33777</span>
        <span className="auth-footer-separator" />
        <span>PostgreSQL</span>
      </footer>
    </main>
  );
}
