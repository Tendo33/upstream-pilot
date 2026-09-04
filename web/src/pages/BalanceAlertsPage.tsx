import { BellRing, CheckCircle2, Clock3, FlaskConical, Save, Trash2, Webhook } from "lucide-react";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, json } from "../api";
import { Badge, Button, ErrorState, Field, Input, PageHeader, PageLoader, SelectMenu, Switch, useToast } from "../components/ui";
import { formatFullDate, relativeTime } from "../lib";
import type { BalanceAlertSettings } from "../types";

const cooldownOptions = [
  { value: "300", label: "5 分钟" },
  { value: "900", label: "15 分钟" },
  { value: "1800", label: "30 分钟" },
  { value: "3600", label: "1 小时" },
  { value: "21600", label: "6 小时" },
  { value: "43200", label: "12 小时" },
  { value: "86400", label: "1 天" },
  { value: "259200", label: "3 天" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" },
];

interface EditorState {
  enabled: boolean;
  threshold: string;
  cooldownSeconds: string;
  webhookURL: string;
  clearWebhook: boolean;
}

function editorFrom(settings: BalanceAlertSettings): EditorState {
  return {
    enabled: settings.enabled,
    threshold: String(settings.threshold),
    cooldownSeconds: String(settings.cooldown_seconds),
    webhookURL: "",
    clearWebhook: false,
  };
}

export function BalanceAlertsPage() {
  const [settings, setSettings] = useState<BalanceAlertSettings | null>(null);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const { toast } = useToast();

  const load = useCallback(async () => {
    setError("");
    try {
      const loaded = await api<BalanceAlertSettings>("/settings/balance-alert");
      setSettings(loaded);
      setEditor(editorFrom(loaded));
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!editor || !settings) return;
    const threshold = Number(editor.threshold);
    if (!Number.isFinite(threshold) || threshold < 0) {
      toast("请输入不小于 0 的有效余额阈值", "error");
      return;
    }
    if (editor.enabled && !settings.webhook_configured && !editor.webhookURL.trim()) {
      toast("启用预警前请填写企业微信 webhook", "error");
      return;
    }

    setSaving(true);
    try {
      const updated = await api<BalanceAlertSettings>("/settings/balance-alert", {
        method: "PUT",
        ...json({
          enabled: editor.enabled,
          threshold,
          cooldown_seconds: Number(editor.cooldownSeconds),
          webhook_url: editor.webhookURL.trim(),
          clear_webhook: editor.clearWebhook,
        }),
      });
      setSettings(updated);
      setEditor(editorFrom(updated));
      toast("余额预警设置已保存", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error");
    } finally {
      setSaving(false);
    }
  }

  async function sendTest() {
    setTesting(true);
    try {
      await api<{ delivered: boolean }>("/settings/balance-alert/test", { method: "POST" });
      toast("企业微信测试通知已发送", "success");
      await load();
    } catch (cause) {
      toast(errorMessage(cause), "error");
      await load();
    } finally {
      setTesting(false);
    }
  }

  if (!settings || !editor) {
    if (error) return <ErrorState message={error} retry={() => void load()} />;
    return <PageLoader />;
  }

  const webhookConfigured = settings.webhook_configured && !editor.clearWebhook;
  const cooldownActive = settings.cooldown_until && new Date(settings.cooldown_until).getTime() > Date.now();

  return (
    <div className="page balance-alerts-page">
      <PageHeader
        eyebrow="Notifications"
        title="余额预警"
        description="企业微信低余额通知"
      />

      <section className="panel balance-alert-panel">
        <header className="balance-alert-panel-header">
          <span className="balance-alert-panel-icon"><BellRing size={18} /></span>
          <div>
            <h2>企业微信群机器人</h2>
            <p>当前工作区的全部账号</p>
          </div>
          <div className="balance-alert-enable">
            <Badge tone={editor.enabled ? "success" : "neutral"}>{editor.enabled ? "已启用" : "已停用"}</Badge>
            <Switch checked={editor.enabled} onChange={(enabled) => setEditor({ ...editor, enabled })} label="启用余额预警" disabled={saving} />
          </div>
        </header>

        <form className="balance-alert-form" onSubmit={save}>
          <div className="form-grid two">
            <Field label="余额阈值" hint="按账号余额快照的原单位比较" required>
              <Input
                type="number"
                min="0"
                max="1000000000000"
                step="any"
                value={editor.threshold}
                onChange={(event) => setEditor({ ...editor, threshold: event.target.value })}
                disabled={saving}
              />
            </Field>
            <Field label="通知冷却" hint="冷却期内只发送一次汇总通知" required>
              <SelectMenu
                value={editor.cooldownSeconds}
                onChange={(cooldownSeconds) => setEditor({ ...editor, cooldownSeconds })}
                options={cooldownOptions}
                label="通知冷却"
                icon={<Clock3 size={14} />}
                disabled={saving}
              />
            </Field>
          </div>

          <Field
            label="企业微信 Webhook"
            hint={webhookConfigured ? "已加密保存；留空保持当前地址" : "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."}
            required={!webhookConfigured}
          >
            <div className="balance-alert-webhook-input">
              <span><Webhook size={15} /></span>
              <Input
                type="password"
                value={editor.webhookURL}
                onChange={(event) => setEditor({ ...editor, webhookURL: event.target.value, clearWebhook: false })}
                placeholder={webhookConfigured ? "已安全保存" : "粘贴企业微信群机器人 Webhook"}
                autoComplete="off"
                disabled={saving || editor.clearWebhook}
              />
              {settings.webhook_configured ? (
                <Button
                  className="balance-alert-clear"
                  variant={editor.clearWebhook ? "danger" : "ghost"}
                  size="sm"
                  onClick={() => setEditor({ ...editor, webhookURL: "", clearWebhook: !editor.clearWebhook, enabled: editor.clearWebhook ? editor.enabled : false })}
                  disabled={saving}
                >
                  <Trash2 size={14} />{editor.clearWebhook ? "待清除" : "清除"}
                </Button>
              ) : null}
            </div>
          </Field>

          <div className="balance-alert-actions">
            <Button onClick={() => void sendTest()} loading={testing} disabled={saving || !settings.webhook_configured || editor.clearWebhook || Boolean(editor.webhookURL.trim())}>
              <FlaskConical size={15} />发送测试通知
            </Button>
            <Button variant="primary" type="submit" loading={saving} disabled={testing}>
              <Save size={15} />保存设置
            </Button>
          </div>
        </form>

        <div className="balance-alert-status">
          <div>
            <span><CheckCircle2 size={15} />最近成功通知</span>
            <strong>{formatFullDate(settings.last_notified_at)}</strong>
          </div>
          <div>
            <span><Clock3 size={15} />冷却状态</span>
            <strong>{cooldownActive ? `结束于 ${formatFullDate(settings.cooldown_until)}` : "可发送"}</strong>
            {cooldownActive ? <small>{relativeTime(settings.cooldown_until)}</small> : null}
          </div>
          <div>
            <span><Webhook size={15} />Webhook</span>
            <strong>{settings.webhook_configured ? "已配置" : "未配置"}</strong>
            {settings.last_attempt_at ? <small>最近尝试 {formatFullDate(settings.last_attempt_at)}</small> : null}
          </div>
        </div>

        {settings.last_error ? <div className="balance-alert-error" role="alert">{settings.last_error}</div> : null}
      </section>
    </div>
  );
}
