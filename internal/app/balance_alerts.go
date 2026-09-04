package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	defaultBalanceAlertThreshold = 10
	defaultBalanceAlertCooldown  = 6 * time.Hour
	minBalanceAlertCooldown      = 5 * time.Minute
	maxBalanceAlertCooldown      = 30 * 24 * time.Hour
	maxBalanceAlertThreshold     = 1_000_000_000_000
	maxWeComResponseBytes        = 64 << 10
	maxWeComMarkdownBytes        = 3800
	balanceAlertSendConcurrency  = 4
)

type balanceAlertSettings struct {
	Enabled           bool       `json:"enabled"`
	Threshold         float64    `json:"threshold"`
	CooldownSeconds   int        `json:"cooldown_seconds"`
	WebhookConfigured bool       `json:"webhook_configured"`
	CooldownUntil     *time.Time `json:"cooldown_until,omitempty"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	LastNotifiedAt    *time.Time `json:"last_notified_at,omitempty"`
	LastError         *string    `json:"last_error,omitempty"`
}

type balanceAlertSettingsInput struct {
	Enabled         bool    `json:"enabled"`
	Threshold       float64 `json:"threshold"`
	CooldownSeconds int     `json:"cooldown_seconds"`
	WebhookURL      string  `json:"webhook_url"`
	ClearWebhook    bool    `json:"clear_webhook"`
}

type claimedBalanceAlert struct {
	OwnerID           string
	Threshold         float64
	WebhookCiphertext string
}

type lowBalanceAccount struct {
	SiteName    string
	AccountName string
	Remaining   float64
	Unit        string
	CheckedAt   time.Time
}

type weComWebhookResponse struct {
	ErrorCode    int    `json:"errcode"`
	ErrorMessage string `json:"errmsg"`
}

func (a *App) getBalanceAlertSettingsHandler(w http.ResponseWriter, r *http.Request) error {
	settings, err := a.loadBalanceAlertSettings(r.Context(), identityFrom(r).ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, settings)
	return nil
}

func (a *App) loadBalanceAlertSettings(ctx context.Context, ownerID string) (balanceAlertSettings, error) {
	settings := balanceAlertSettings{
		Threshold:       defaultBalanceAlertThreshold,
		CooldownSeconds: int(defaultBalanceAlertCooldown / time.Second),
	}
	err := a.db.QueryRow(ctx, `
		SELECT enabled,threshold,cooldown_seconds,webhook_url_ciphertext IS NOT NULL,
		       cooldown_until,last_attempt_at,last_notified_at,last_error
		FROM balance_alert_settings WHERE owner_id=$1`, ownerID).Scan(
		&settings.Enabled, &settings.Threshold, &settings.CooldownSeconds, &settings.WebhookConfigured,
		&settings.CooldownUntil, &settings.LastAttemptAt, &settings.LastNotifiedAt, &settings.LastError,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	return settings, err
}

func normalizeBalanceAlertSettingsInput(input *balanceAlertSettingsInput) error {
	if math.IsNaN(input.Threshold) || math.IsInf(input.Threshold, 0) || input.Threshold < 0 || input.Threshold > maxBalanceAlertThreshold {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_BALANCE_THRESHOLD", Message: "余额阈值必须是 0 到 1000000000000 之间的有限数字"}
	}
	if input.CooldownSeconds < int(minBalanceAlertCooldown/time.Second) || input.CooldownSeconds > int(maxBalanceAlertCooldown/time.Second) {
		return &apiError{Status: http.StatusBadRequest, Code: "INVALID_ALERT_COOLDOWN", Message: "通知冷却必须为 5 分钟到 30 天"}
	}
	input.WebhookURL = strings.TrimSpace(input.WebhookURL)
	if input.WebhookURL != "" {
		normalized, err := normalizeWeComWebhookURL(input.WebhookURL)
		if err != nil {
			return &apiError{Status: http.StatusBadRequest, Code: "INVALID_WECOM_WEBHOOK", Message: "请填写有效的企业微信群机器人 webhook 地址"}
		}
		input.WebhookURL = normalized
	}
	if input.WebhookURL != "" && input.ClearWebhook {
		return &apiError{Status: http.StatusBadRequest, Code: "CONFLICTING_WEBHOOK_UPDATE", Message: "不能同时更新并清除 webhook"}
	}
	return nil
}

func normalizeWeComWebhookURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "qyapi.weixin.qq.com") {
		return "", errors.New("invalid Enterprise WeChat webhook URL")
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Path != "/cgi-bin/webhook/send" {
		return "", errors.New("invalid Enterprise WeChat webhook URL")
	}
	key := strings.TrimSpace(parsed.Query().Get("key"))
	if key == "" || len(key) > 256 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("missing Enterprise WeChat webhook key")
	}
	return parsed.String(), nil
}

func (a *App) updateBalanceAlertSettingsHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var input balanceAlertSettingsInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	if err := normalizeBalanceAlertSettingsInput(&input); err != nil {
		return err
	}

	sealed := ""
	var err error
	if input.WebhookURL != "" {
		sealed, err = a.cipher.Encrypt(input.WebhookURL, balanceAlertSecretPurpose(identity.ID))
		if err != nil {
			return err
		}
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO balance_alert_settings(owner_id) VALUES($1)
		ON CONFLICT(owner_id) DO NOTHING`, identity.ID); err != nil {
		return err
	}
	var webhookConfigured bool
	if err := tx.QueryRow(r.Context(), `
		SELECT webhook_url_ciphertext IS NOT NULL FROM balance_alert_settings
		WHERE owner_id=$1 FOR UPDATE`, identity.ID).Scan(&webhookConfigured); err != nil {
		return err
	}
	resultWebhookConfigured := !input.ClearWebhook && (sealed != "" || webhookConfigured)
	if input.Enabled && !resultWebhookConfigured {
		return &apiError{Status: http.StatusBadRequest, Code: "WEBHOOK_REQUIRED", Message: "启用余额预警前请先配置企业微信 webhook"}
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE balance_alert_settings SET
		 enabled=$2,threshold=$3,cooldown_seconds=$4,
		 webhook_url_ciphertext=CASE WHEN $6 THEN NULL WHEN $5<>'' THEN $5 ELSE webhook_url_ciphertext END,
		 cooldown_until=CASE WHEN $5<>'' OR $6 OR NOT $2 THEN NULL ELSE cooldown_until END,
		 last_error=CASE WHEN $5<>'' OR $6 OR NOT $2 THEN NULL ELSE last_error END,
		 updated_at=now()
		WHERE owner_id=$1`, identity.ID, input.Enabled, input.Threshold, input.CooldownSeconds, sealed, input.ClearWebhook); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "balance_alert.settings.update", "success", map[string]any{
		"enabled": input.Enabled, "threshold": input.Threshold, "cooldown_seconds": input.CooldownSeconds, "webhook_configured": resultWebhookConfigured,
	})
	settings, err := a.loadBalanceAlertSettings(r.Context(), identity.ID)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, settings)
	return nil
}

func (a *App) testBalanceAlertWebhookHandler(w http.ResponseWriter, r *http.Request) error {
	identity := identityFrom(r)
	var sealed string
	if err := a.db.QueryRow(r.Context(), `
		SELECT webhook_url_ciphertext FROM balance_alert_settings
		WHERE owner_id=$1 AND webhook_url_ciphertext IS NOT NULL`, identity.ID).Scan(&sealed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiError{Status: http.StatusBadRequest, Code: "WEBHOOK_REQUIRED", Message: "请先保存企业微信 webhook"}
		}
		return err
	}
	webhookURL, err := a.cipher.Decrypt(sealed, balanceAlertSecretPurpose(identity.ID))
	if err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	attemptedAt := time.Now().UTC()
	err = sendWeComWebhook(testCtx, a.httpClient, webhookURL, buildWeComTestMessage(attemptedAt))
	a.recordBalanceAlertDelivery(context.WithoutCancel(r.Context()), identity.ID, attemptedAt, false, err)
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	_ = a.audit(r.Context(), identity.ID, identity.ID, "", "", "balance_alert.webhook.test", outcome, map[string]any{})
	if err != nil {
		return &apiError{Status: http.StatusBadGateway, Code: "WEBHOOK_DELIVERY_FAILED", Message: "企业微信测试通知发送失败：" + err.Error()}
	}
	writeData(w, http.StatusOK, map[string]bool{"delivered": true})
	return nil
}

func balanceAlertSecretPurpose(ownerID string) string {
	return "balance-alert:" + ownerID
}

func (a *App) sendDueBalanceAlerts(ctx context.Context, now time.Time) error {
	claimed, err := a.claimDueBalanceAlerts(ctx, now)
	if err != nil || len(claimed) == 0 {
		return err
	}

	jobs := make(chan claimedBalanceAlert)
	errorsFound := make(chan error, len(claimed))
	var workers sync.WaitGroup
	workerCount := min(balanceAlertSendConcurrency, len(claimed))
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for alert := range jobs {
				if err := a.deliverClaimedBalanceAlert(ctx, alert, now); err != nil {
					errorsFound <- fmt.Errorf("deliver balance alert for owner %s: %w", alert.OwnerID, err)
				}
			}
		}()
	}
	for _, alert := range claimed {
		select {
		case jobs <- alert:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			close(errorsFound)
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	close(errorsFound)

	var joined error
	for deliveryErr := range errorsFound {
		joined = errors.Join(joined, deliveryErr)
	}
	return joined
}

func (a *App) claimDueBalanceAlerts(ctx context.Context, now time.Time) ([]claimedBalanceAlert, error) {
	rows, err := a.db.Query(ctx, `
		UPDATE balance_alert_settings settings SET
		 cooldown_until=$1 + make_interval(secs => settings.cooldown_seconds),
		 last_attempt_at=$1,
		 updated_at=now()
		WHERE settings.enabled
		  AND settings.webhook_url_ciphertext IS NOT NULL
		  AND (settings.cooldown_until IS NULL OR settings.cooldown_until <= $1)
		  AND EXISTS (
		    SELECT 1
		    FROM account_balance_snapshots balance
		    JOIN upstream_accounts account ON account.id=balance.account_id
		    JOIN sites site ON site.id=account.site_id
		    WHERE site.owner_id=settings.owner_id
		      AND account.deleted_at IS NULL
		      AND balance.status='ok'
		      AND balance.remaining IS NOT NULL
		      AND balance.remaining < settings.threshold
		  )
		RETURNING settings.owner_id::text,settings.threshold,settings.webhook_url_ciphertext`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claimed := make([]claimedBalanceAlert, 0)
	for rows.Next() {
		var alert claimedBalanceAlert
		if err := rows.Scan(&alert.OwnerID, &alert.Threshold, &alert.WebhookCiphertext); err != nil {
			return nil, err
		}
		claimed = append(claimed, alert)
	}
	return claimed, rows.Err()
}

func (a *App) deliverClaimedBalanceAlert(ctx context.Context, alert claimedBalanceAlert, attemptedAt time.Time) error {
	accounts, err := a.loadLowBalanceAccounts(ctx, alert.OwnerID, alert.Threshold)
	if err != nil {
		a.recordBalanceAlertDelivery(context.WithoutCancel(ctx), alert.OwnerID, attemptedAt, false, err)
		return err
	}
	if len(accounts) == 0 {
		return nil
	}
	webhookURL, err := a.cipher.Decrypt(alert.WebhookCiphertext, balanceAlertSecretPurpose(alert.OwnerID))
	if err != nil {
		a.recordBalanceAlertDelivery(context.WithoutCancel(ctx), alert.OwnerID, attemptedAt, false, err)
		return err
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = sendWeComWebhook(deliveryCtx, a.httpClient, webhookURL, buildWeComBalanceAlertMessage(alert.Threshold, accounts, attemptedAt, a.config.PublicURL))
	a.recordBalanceAlertDelivery(context.WithoutCancel(ctx), alert.OwnerID, attemptedAt, err == nil, err)
	if err != nil {
		_ = a.audit(context.WithoutCancel(ctx), alert.OwnerID, "", "", "", "balance_alert.webhook.send", "failed", map[string]any{"accounts": len(accounts), "error": err.Error()})
		return err
	}
	_ = a.audit(context.WithoutCancel(ctx), alert.OwnerID, "", "", "", "balance_alert.webhook.send", "success", map[string]any{"accounts": len(accounts), "threshold": alert.Threshold})
	return nil
}

func (a *App) loadLowBalanceAccounts(ctx context.Context, ownerID string, threshold float64) ([]lowBalanceAccount, error) {
	rows, err := a.db.Query(ctx, `
		SELECT site.name,account.name,balance.remaining,COALESCE(balance.unit,''),balance.checked_at
		FROM account_balance_snapshots balance
		JOIN upstream_accounts account ON account.id=balance.account_id
		JOIN sites site ON site.id=account.site_id
		WHERE site.owner_id=$1
		  AND account.deleted_at IS NULL
		  AND balance.status='ok'
		  AND balance.remaining IS NOT NULL
		  AND balance.remaining < $2
		ORDER BY balance.remaining,site.name,account.name`, ownerID, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]lowBalanceAccount, 0)
	for rows.Next() {
		var account lowBalanceAccount
		if err := rows.Scan(&account.SiteName, &account.AccountName, &account.Remaining, &account.Unit, &account.CheckedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (a *App) recordBalanceAlertDelivery(ctx context.Context, ownerID string, attemptedAt time.Time, delivered bool, deliveryErr error) {
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	message := ""
	if deliveryErr != nil {
		message = compactAlertText(deliveryErr.Error(), 500)
	}
	if _, err := a.db.Exec(updateCtx, `
		UPDATE balance_alert_settings SET
		 last_attempt_at=$2,
		 last_notified_at=CASE WHEN $3 THEN $2 ELSE last_notified_at END,
		 last_error=CASE WHEN $3 THEN NULL ELSE NULLIF($4,'') END,
		 updated_at=now()
		WHERE owner_id=$1`, ownerID, attemptedAt, delivered, message); err != nil && a.logger != nil {
		a.logger.Warn("balance alert delivery state update failed", "owner_id", ownerID, "error", err)
	}
}

func sendWeComWebhook(ctx context.Context, client *http.Client, webhookURL, content string) error {
	payload, err := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": content},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("无法创建企业微信 webhook 请求")
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("企业微信 webhook 请求失败")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxWeComResponseBytes))
	if err != nil {
		return errors.New("无法读取企业微信 webhook 响应")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("企业微信 webhook 返回 HTTP %d", response.StatusCode)
	}
	var result weComWebhookResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return errors.New("企业微信 webhook 返回无效 JSON")
	}
	if result.ErrorCode != 0 {
		message := compactAlertText(result.ErrorMessage, 160)
		if message == "" {
			message = "未知错误"
		}
		return fmt.Errorf("企业微信 webhook 返回错误 %d：%s", result.ErrorCode, message)
	}
	return nil
}

func buildWeComBalanceAlertMessage(threshold float64, accounts []lowBalanceAccount, sentAt time.Time, publicURL string) string {
	var builder strings.Builder
	builder.WriteString("## Upstream Pilot 余额预警\n")
	builder.WriteString("> 预警阈值：<font color=\"warning\">")
	builder.WriteString(formatBalanceAlertNumber(threshold))
	builder.WriteString("</font>\n")
	builder.WriteString("> 低余额账号：<font color=\"warning\">")
	builder.WriteString(strconv.Itoa(len(accounts)))
	builder.WriteString(" 个</font>\n\n")

	shown := 0
	for _, account := range accounts {
		line := "- **" + escapeWeComText(account.SiteName) + " / " + escapeWeComText(account.AccountName) + "**：<font color=\"warning\">" +
			formatBalanceAlertNumber(account.Remaining) + " " + escapeWeComText(account.Unit) + "</font>\n"
		if builder.Len()+len(line)+320 > maxWeComMarkdownBytes {
			break
		}
		builder.WriteString(line)
		shown++
	}
	if hidden := len(accounts) - shown; hidden > 0 {
		builder.WriteString("- 另有 ")
		builder.WriteString(strconv.Itoa(hidden))
		builder.WriteString(" 个账号，请打开控制台查看\n")
	}
	builder.WriteString("\n> 检测时间：")
	builder.WriteString(sentAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"))
	if normalizedPublicURL := strings.TrimRight(strings.TrimSpace(publicURL), "/"); normalizedPublicURL != "" && len(normalizedPublicURL) <= 512 {
		link := "\n> [打开账号页面](" + html.EscapeString(normalizedPublicURL+"/accounts") + ")"
		if builder.Len()+len(link) <= maxWeComMarkdownBytes {
			builder.WriteString(link)
		}
	}
	return builder.String()
}

func buildWeComTestMessage(sentAt time.Time) string {
	return "## Upstream Pilot 余额预警测试\n> 企业微信机器人连接正常\n> 发送时间：" +
		sentAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05")
}

func escapeWeComText(value string) string {
	value = compactAlertText(value, 80)
	value = strings.NewReplacer("\\", "\\\\", "*", "\\*", "`", "\\`", "[", "\\[", "]", "\\]").Replace(value)
	return html.EscapeString(value)
}

func compactAlertText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-1]) + "…"
}

func formatBalanceAlertNumber(value float64) string {
	abs := math.Abs(value)
	precision := 4
	if abs >= 1000 {
		precision = 0
	} else if abs >= 1 {
		precision = 2
	}
	formatted := strconv.FormatFloat(value, 'f', precision, 64)
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	return formatted
}
