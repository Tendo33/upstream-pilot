package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *App) qualityAlertsHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	if r.Method == http.MethodPut {
		var input struct {
			Enabled bool   `json:"enabled"`
			Webhook string `json:"webhook_url"`
			Clear   bool   `json:"clear_webhook"`
		}
		if err := decodeJSON(r, &input); err != nil {
			return err
		}
		sealed := ""
		input.Webhook = strings.TrimSpace(input.Webhook)
		if input.Webhook != "" {
			u, err := url.Parse(input.Webhook)
			if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(a.config.AllowPrivateUpstreams && u.Scheme == "http")) {
				return &apiError{Status: 400, Code: "INVALID_WEBHOOK", Message: "请填写有效的 HTTPS Webhook 地址"}
			}
			if input.Clear {
				return &apiError{Status: 400, Code: "INVALID_WEBHOOK", Message: "不能同时设置和清除 Webhook"}
			}
			sealed, err = a.cipher.Encrypt(input.Webhook, "quality-alert:"+owner)
			if err != nil {
				return err
			}
		}
		var existing bool
		if err := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM quality_alert_settings WHERE owner_id=$1 AND webhook_ciphertext IS NOT NULL)`, owner).Scan(&existing); err != nil {
			return err
		}
		if input.Enabled && (input.Clear || (!existing && sealed == "")) {
			return &apiError{Status: 400, Code: "WEBHOOK_REQUIRED", Message: "启用通知前请配置 Webhook"}
		}
		_, err := a.db.Exec(r.Context(), `INSERT INTO quality_alert_settings(owner_id,enabled,webhook_ciphertext) VALUES($1,$2,NULLIF($3,'')) ON CONFLICT(owner_id) DO UPDATE SET enabled=$2,webhook_ciphertext=CASE WHEN $4 THEN NULL WHEN $3<>'' THEN $3 ELSE quality_alert_settings.webhook_ciphertext END,updated_at=now()`, owner, input.Enabled, sealed, input.Clear)
		if err != nil {
			return err
		}
	}
	var enabled, configured bool
	err := a.db.QueryRow(r.Context(), `SELECT enabled,webhook_ciphertext IS NOT NULL FROM quality_alert_settings WHERE owner_id=$1`, owner).Scan(&enabled, &configured)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var history []byte
	if err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(v),'[]') FROM(SELECT id,kind,message,attempts,delivered_at,last_error,created_at FROM quality_notifications WHERE owner_id=$1 ORDER BY created_at DESC LIMIT 100)v`, owner).Scan(&history); err != nil {
		return err
	}
	writeData(w, 200, map[string]any{"enabled": enabled, "webhook_configured": configured, "events": json.RawMessage(history)})
	return nil
}

func (a *App) qualityAlertTestHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	var sealed string
	if err := a.db.QueryRow(r.Context(), `SELECT webhook_ciphertext FROM quality_alert_settings WHERE owner_id=$1 AND webhook_ciphertext IS NOT NULL`, owner).Scan(&sealed); err != nil {
		return &apiError{Status: 400, Code: "WEBHOOK_REQUIRED", Message: "请先保存 Webhook"}
	}
	endpoint, err := a.cipher.Decrypt(sealed, "quality-alert:"+owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err = a.deliverQualityNotification(ctx, endpoint, uuid.NewString(), "test", "Sub2API 上游质量通知测试"); err != nil {
		return &apiError{Status: 422, Code: "WEBHOOK_FAILED", Message: err.Error()}
	}
	writeData(w, 200, map[string]bool{"sent": true})
	return nil
}

func (a *App) deliverQualityNotification(ctx context.Context, endpoint, id, kind, message string) error {
	parsed, _ := url.Parse(endpoint)
	if parsed != nil && parsed.Hostname() == "qyapi.weixin.qq.com" {
		return sendWeComWebhook(ctx, a.httpClient, endpoint, message)
	}
	data, _ := json.Marshal(map[string]any{"event_id": id, "kind": kind, "message": message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("无法创建通知请求")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return errors.New("通知发送失败，请检查地址和网络")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("通知接收端未返回成功状态")
	}
	return nil
}

func (a *App) runQualityNotifications(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendQualityNotifications(ctx)
		}
	}
}

func (a *App) sendQualityNotifications(ctx context.Context) {
	for i := 0; i < 10; i++ {
		var id, owner, kind, message, sealed string
		var attempts int
		err := a.db.QueryRow(ctx, `WITH candidate AS(SELECT n.id FROM quality_notifications n JOIN quality_alert_settings s ON s.owner_id=n.owner_id WHERE s.enabled AND s.webhook_ciphertext IS NOT NULL AND n.delivered_at IS NULL AND n.attempts<5 AND n.next_attempt_at<=now() AND (n.lease_until IS NULL OR n.lease_until<now()) ORDER BY n.created_at FOR UPDATE OF n SKIP LOCKED LIMIT 1) UPDATE quality_notifications n SET lease_until=now()+interval '30 seconds',attempts=attempts+1 FROM candidate c,quality_alert_settings s WHERE n.id=c.id AND s.owner_id=n.owner_id RETURNING n.id::text,n.owner_id::text,n.kind,n.message,n.attempts,s.webhook_ciphertext`).Scan(&id, &owner, &kind, &message, &attempts, &sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			a.logger.Warn("notification claim failed", "error", err)
			return
		}
		endpoint, err := a.cipher.Decrypt(sealed, "quality-alert:"+owner)
		if err == nil {
			delivery, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = a.deliverQualityNotification(delivery, endpoint, id, kind, message)
			cancel()
		}
		persistence, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err == nil {
			_, err = a.db.Exec(persistence, `UPDATE quality_notifications SET delivered_at=now(),lease_until=NULL,last_error='' WHERE id=$1`, id)
		} else {
			_, err = a.db.Exec(persistence, `UPDATE quality_notifications SET lease_until=NULL,last_error='通知发送失败，最多重试五次；请检查接收地址',next_attempt_at=now()+$2*interval '1 second' WHERE id=$1`, id, 30*(1<<min(attempts, 5)))
		}
		cancel()
		if err != nil {
			a.logger.Warn("notification delivery state failed", "error", err)
		}
	}
}
