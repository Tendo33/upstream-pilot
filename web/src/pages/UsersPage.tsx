import { KeyRound, Pencil, Plus, Shield, Trash2, UserRound, UsersRound } from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, json } from "../api";
import { formatFullDate } from "../lib";
import type { Role, User } from "../types";
import {
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Modal,
  PageHeader,
  PageLoader,
  Switch,
  useToast,
} from "../components/ui";

interface EditorState {
  user: User | null;
  email: string;
  password: string;
  role: Role;
  enabled: boolean;
}

export function UsersPage({ currentUser }: { currentUser: User }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState("");
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");
  const { toast } = useToast();

  const load = useCallback(async () => {
    setError("");
    try {
      setUsers(await api<User[]>("/users"));
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!editor) return;
    if (!editor.user && (!editor.email.trim() || editor.password.length < 10)) {
      toast("请填写有效邮箱，密码至少需要 10 个字符", "error");
      return;
    }
    setSaving(true);
    try {
      const saved = editor.user
        ? await api<User>(`/users/${editor.user.id}`, { method: "PATCH", ...json({ role: editor.role, enabled: editor.enabled, password: editor.password }) })
        : await api<User>("/users", { method: "POST", ...json({ email: editor.email.trim(), password: editor.password, role: editor.role }) });
      setUsers((current) => editor.user ? current?.map((item) => item.id === saved.id ? saved : item) ?? [saved] : [...(current ?? []), saved]);
      setEditor(null);
      toast(editor.user ? "用户设置已更新" : "用户已创建", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  async function toggleUser(user: User, enabled: boolean) {
    setBusy(user.id);
    try {
      const updated = await api<User>(`/users/${user.id}`, { method: "PATCH", ...json({ enabled }) });
      setUsers((current) => current?.map((item) => item.id === updated.id ? updated : item) ?? null);
      toast(enabled ? "用户已启用" : "用户已停用并退出所有会话", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setBusy("");
    }
  }

  async function deleteUser() {
    if (!deleting) return;
    setSaving(true);
    try {
      await api<void>(`/users/${deleting.id}`, { method: "DELETE" });
      setUsers((current) => current?.filter((user) => user.id !== deleting.id) ?? null);
      setDeleting(null);
      toast("用户及其站点数据已删除", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  if (!users && !error) return <PageLoader />;
  if (!users) return <ErrorState message={error} retry={() => void load()} />;

  return (
    <div className="page">
      <PageHeader
        eyebrow="Administration"
        title="用户"
        description="管理独立工作区、角色与访问状态"
        actions={<Button variant="primary" onClick={() => setEditor({ user: null, email: "", password: "", role: "user", enabled: true })}><Plus size={16} />添加用户</Button>}
      />

      {users.length === 0 ? (
        <section className="panel"><EmptyState title="暂无用户" description="创建用户后即可分配独立的 Sub2API 工作区。" icon={<UsersRound size={21} />} /></section>
      ) : (
        <div className="users-table">
          <div className="users-head"><span>用户</span><span>角色</span><span>状态</span><span>最后登录</span><span>创建时间</span><span /></div>
          {users.map((user) => (
            <div className="user-row" key={user.id}>
              <div className="user-identity">
                <div className="avatar">{user.email.slice(0, 1).toUpperCase()}</div>
                <div><strong>{user.email}</strong><span>{user.id === currentUser.id ? "当前用户" : user.id.slice(0, 8)}</span></div>
              </div>
              <div><Badge tone={user.role === "admin" ? "info" : "neutral"}>{user.role === "admin" ? "管理员" : "用户"}</Badge></div>
              <div className="user-status"><Switch checked={user.enabled} onChange={(enabled) => void toggleUser(user, enabled)} label={`${user.enabled ? "停用" : "启用"}${user.email}`} disabled={user.id === currentUser.id || busy === user.id} /><span>{user.enabled ? "已启用" : "已停用"}</span></div>
              <time>{formatFullDate(user.last_login_at)}</time>
              <time>{formatFullDate(user.created_at)}</time>
              <div className="user-actions">
                <IconButton label="编辑用户" onClick={() => setEditor({ user, email: user.email, password: "", role: user.role, enabled: user.enabled })}><Pencil size={16} /></IconButton>
                <IconButton label="删除用户" danger disabled={user.id === currentUser.id} onClick={() => setDeleting(user)}><Trash2 size={16} /></IconButton>
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        open={Boolean(editor)}
        title={editor?.user ? "编辑用户" : "添加用户"}
        description={editor?.user ? editor.user.email : "创建独立的数据工作区"}
        onClose={() => !saving && setEditor(null)}
        width="sm"
        footer={<><Button onClick={() => setEditor(null)} disabled={saving}>取消</Button><Button variant="primary" type="submit" form="user-form" loading={saving}>{editor?.user ? "保存" : "创建用户"}</Button></>}
      >
        {editor ? (
          <form id="user-form" className="form-stack" onSubmit={save}>
            {!editor.user ? (
              <Field label="邮箱" required><Input type="email" value={editor.email} onChange={(event) => setEditor({ ...editor, email: event.target.value })} placeholder="user@example.com" autoComplete="email" autoFocus /></Field>
            ) : null}
            <Field label={editor.user ? "重置密码" : "初始密码"} hint={editor.user ? "留空保持当前密码；修改后用户需重新登录" : "10 至 128 个字符"} required={!editor.user}>
              <div className="input-prefix"><KeyRound size={15} /><Input type="password" value={editor.password} onChange={(event) => setEditor({ ...editor, password: event.target.value })} minLength={editor.user ? undefined : 10} maxLength={128} placeholder={editor.user ? "保持不变" : "输入初始密码"} autoComplete="new-password" /></div>
            </Field>
            <Field label="角色">
              <div className="role-options">
                <button type="button" className={editor.role === "user" ? "active" : ""} disabled={editor.user?.id === currentUser.id} onClick={() => setEditor({ ...editor, role: "user" })}><UserRound size={17} /><span><strong>用户</strong><small>仅访问自己的站点</small></span></button>
                <button type="button" className={editor.role === "admin" ? "active" : ""} disabled={editor.user?.id === currentUser.id} onClick={() => setEditor({ ...editor, role: "admin" })}><Shield size={17} /><span><strong>管理员</strong><small>可管理用户</small></span></button>
              </div>
            </Field>
            {editor.user ? <div className="setting-inline"><span>{editor.enabled ? "允许登录" : "禁止登录"}</span><Switch checked={editor.enabled} onChange={(enabled) => setEditor({ ...editor, enabled })} label="允许用户登录" disabled={editor.user.id === currentUser.id} /></div> : null}
          </form>
        ) : null}
      </Modal>

      <ConfirmDialog
        open={Boolean(deleting)}
        title="删除用户"
        description={`确定删除“${deleting?.email ?? ""}”吗？该用户拥有的站点、账号配置和审计日志将一并删除。`}
        confirmLabel="删除用户"
        danger
        loading={saving}
        onConfirm={() => void deleteUser()}
        onClose={() => setDeleting(null)}
      />
    </div>
  );
}
