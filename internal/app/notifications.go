package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var notificationCategories = []string{"quality", "price", "balance", "collector", "controller", "runway"}

type notificationRules struct {
	PriceRisePercent       float64 `json:"price_rise_percent"`
	PriceCooldownSeconds   int     `json:"price_cooldown_seconds"`
	BalanceEnabled         bool    `json:"balance_enabled"`
	BalanceThreshold       float64 `json:"balance_threshold"`
	BalanceCooldownSeconds int     `json:"balance_cooldown_seconds"`
}

func defaultNotificationRules() notificationRules {
	return notificationRules{5, 3600, false, 10, 21600}
}
func (a *App) loadNotificationRules(ctx context.Context, owner string) (notificationRules, error) {
	v := defaultNotificationRules()
	err := a.db.QueryRow(ctx, `SELECT price_rise_percent,price_cooldown_seconds,balance_enabled,balance_threshold,balance_cooldown_seconds FROM notification_rules WHERE owner_id=$1`, owner).Scan(&v.PriceRisePercent, &v.PriceCooldownSeconds, &v.BalanceEnabled, &v.BalanceThreshold, &v.BalanceCooldownSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return v, err
}
func (a *App) notificationRulesHandler(w http.ResponseWriter, r *http.Request) error {
	var v notificationRules
	if err := decodeJSON(r, &v); err != nil {
		return err
	}
	if !isFinite(v.PriceRisePercent) || v.PriceRisePercent < 0 || v.PriceRisePercent > 10000 || v.PriceCooldownSeconds < 60 || v.PriceCooldownSeconds > 86400 || !isFinite(v.BalanceThreshold) || v.BalanceThreshold < 0 || v.BalanceThreshold > 1e12 || v.BalanceCooldownSeconds < 300 || v.BalanceCooldownSeconds > 2592000 {
		return &apiError{Status: 400, Code: "INVALID_NOTIFICATION_RULE", Message: "请检查涨价比例、余额阈值与通知冷却时间"}
	}
	_, err := a.db.Exec(r.Context(), `INSERT INTO notification_rules(owner_id,price_rise_percent,price_cooldown_seconds,balance_enabled,balance_threshold,balance_cooldown_seconds) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(owner_id) DO UPDATE SET price_rise_percent=$2,price_cooldown_seconds=$3,balance_enabled=$4,balance_threshold=$5,balance_cooldown_seconds=$6,balance_snoozed_until=NULL,updated_at=now()`, identityFrom(r).ID, v.PriceRisePercent, v.PriceCooldownSeconds, v.BalanceEnabled, v.BalanceThreshold, v.BalanceCooldownSeconds)
	if err != nil {
		return err
	}
	writeData(w, 200, v)
	return nil
}
func (a *App) notificationsHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	rules, err := a.loadNotificationRules(r.Context(), owner)
	if err != nil {
		return err
	}
	var channels, events []byte
	if err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(c),'[]') FROM(SELECT id,name,provider,enabled,categories,revision,true AS webhook_configured,signing_secret_ciphertext IS NOT NULL AS signing_secret_configured,legacy_source FROM notification_channels WHERE owner_id=$1 ORDER BY created_at,id)c`, owner).Scan(&channels); err != nil {
		return err
	}
	if err = a.db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(e),'[]') FROM(SELECT n.id,n.kind,n.category,n.severity,n.message,n.context,n.created_at,COALESCE((SELECT jsonb_agg(d) FROM(SELECT d.id,d.channel_name,d.provider,d.status,d.attempts,d.last_error,d.delivered_at,d.next_attempt_at FROM notification_deliveries d JOIN notification_channels c ON c.id=d.channel_id WHERE d.event_id=n.id AND c.owner_id=$1 ORDER BY d.created_at)d),'[]') AS deliveries FROM quality_notifications n WHERE n.owner_id=$1 ORDER BY n.created_at DESC,n.id DESC LIMIT 100)e`, owner).Scan(&events); err != nil {
		return err
	}
	writeData(w, 200, map[string]any{"rules": rules, "channels": json.RawMessage(channels), "events": json.RawMessage(events)})
	return nil
}

type notificationChannelInput struct {
	Name               string   `json:"name"`
	Provider           string   `json:"provider"`
	Enabled            bool     `json:"enabled"`
	Categories         []string `json:"categories"`
	Revision           *int64   `json:"revision"`
	Webhook            string   `json:"webhook_url"`
	SigningSecret      string   `json:"signing_secret"`
	ClearSigningSecret bool     `json:"clear_signing_secret"`
}

func (a *App) saveNotificationChannelHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	id := uuid.NewString()
	creating := r.Method == http.MethodPost
	if !creating {
		var err error
		id, err = requiredID(r, "channelID")
		if err != nil {
			return err
		}
	}
	var input notificationChannelInput
	if err := decodeJSON(r, &input); err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Webhook = strings.TrimSpace(input.Webhook)
	input.SigningSecret = strings.TrimSpace(input.SigningSecret)
	if input.Name == "" || len(input.Name) > 100 || len(input.SigningSecret) > 512 || strings.ContainsAny(input.SigningSecret, "\r\n") || input.ClearSigningSecret && input.SigningSecret != "" {
		return &apiError{Status: 400, Code: "INVALID_CHANNEL", Message: "请检查渠道名称和签名设置"}
	}
	valid := map[string]bool{}
	for _, category := range notificationCategories {
		valid[category] = true
	}
	selected := map[string]bool{}
	for _, v := range input.Categories {
		if !valid[v] || selected[v] {
			return &apiError{Status: 400, Code: "INVALID_SUBSCRIPTION", Message: "通知类型无效或重复"}
		}
		selected[v] = true
	}
	if len(selected) == 0 {
		return &apiError{Status: 400, Code: "EMPTY_SUBSCRIPTION", Message: "至少选择一种通知类型"}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var user string
	if err = tx.QueryRow(r.Context(), `SELECT id::text FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&user); err != nil {
		return err
	}
	endpoint, sealed, secretCipher, purpose, oldProvider := "", "", "", "", ""
	if creating {
		var count int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM notification_channels WHERE owner_id=$1`, owner).Scan(&count); err != nil {
			return err
		}
		if count >= 8 {
			return &apiError{Status: 400, Code: "CHANNEL_LIMIT", Message: "最多配置 8 个通知渠道"}
		}
	} else {
		var revision int64
		if err = tx.QueryRow(r.Context(), `SELECT webhook_ciphertext,COALESCE(signing_secret_ciphertext,''),secret_purpose,revision,provider FROM notification_channels WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner).Scan(&sealed, &secretCipher, &purpose, &revision, &oldProvider); err != nil {
			return err
		}
		if input.Revision == nil || *input.Revision != revision {
			return &apiError{Status: 409, Code: "CHANNEL_CHANGED", Message: "渠道已被修改，请刷新后再保存"}
		}
		var active int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND status='sending' AND lease_until>now()`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return &apiError{Status: 409, Code: "CHANNEL_BUSY", Message: "该渠道正在发送消息，请稍后再保存"}
		}
		endpoint, err = a.cipher.Decrypt(sealed, purpose)
		if err != nil {
			return err
		}
	}
	if input.Provider == "auto" && (creating || oldProvider != "auto") {
		return &apiError{Status: 400, Code: "INVALID_PROVIDER", Message: "请选择飞书、企业微信或通用 Webhook"}
	}
	if input.Webhook != "" {
		endpoint = input.Webhook
	}
	endpoint, err = normalizeNotificationURL(endpoint, input.Provider, a.config.AllowPrivateUpstreams)
	if err != nil {
		return &apiError{Status: 400, Code: "INVALID_WEBHOOK", Message: err.Error()}
	}
	if input.SigningSecret != "" && input.Provider != "feishu" {
		return &apiError{Status: 400, Code: "INVALID_SIGNATURE", Message: "签名密钥仅用于飞书渠道"}
	}
	newPurpose := "notification-channel:" + owner + ":" + id
	sealed, err = a.cipher.Encrypt(endpoint, newPurpose)
	if err != nil {
		return err
	}
	if input.ClearSigningSecret || input.Provider != "feishu" || oldProvider != "feishu" {
		secretCipher = ""
	}
	if input.SigningSecret != "" {
		secretCipher, err = a.cipher.Encrypt(input.SigningSecret, newPurpose+":sign")
		if err != nil {
			return err
		}
	}
	// A new destination cannot silently inherit the previous robot's signing key.
	if input.Webhook != "" && !creating && secretCipher != "" && input.SigningSecret == "" && !input.ClearSigningSecret {
		return &apiError{Status: 400, Code: "SIGNATURE_REQUIRED", Message: "更换飞书地址时请重新填写签名密钥，或明确清除签名"}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO notification_channels(id,owner_id,name,provider,enabled,categories,webhook_ciphertext,signing_secret_ciphertext,secret_purpose) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9) ON CONFLICT(id) DO UPDATE SET name=$3,provider=$4,enabled=$5,categories=$6,webhook_ciphertext=$7,signing_secret_ciphertext=NULLIF($8,''),secret_purpose=$9,revision=notification_channels.revision+1,updated_at=now()`, id, owner, input.Name, input.Provider, input.Enabled, input.Categories, sealed, secretCipher, newPurpose)
	if err != nil {
		return err
	}
	// Changes cancel pending deliveries, preserving the old destination in history.
	if !creating {
		if _, err = tx.Exec(r.Context(), `UPDATE notification_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL,last_error='渠道已修改，旧投递已取消' WHERE channel_id=$1 AND status IN('pending','failed','sending')`, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	_ = a.audit(r.Context(), owner, owner, "", "", "notification.channel.save", "success", map[string]any{"channel_id": id, "provider": input.Provider, "enabled": input.Enabled})
	writeData(w, 200, map[string]any{"id": id, "saved": true})
	return nil
}

func (a *App) notificationTestHandler(w http.ResponseWriter, r *http.Request) error {
	owner := identityFrom(r).ID
	channel, err := requiredID(r, "channelID")
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var revision int64
	if err = tx.QueryRow(r.Context(), `SELECT revision FROM notification_channels WHERE id=$1 AND owner_id=$2 FOR SHARE`, channel, owner).Scan(&revision); err != nil {
		return err
	}
	event, delivery := uuid.NewString(), uuid.NewString()
	if _, err = tx.Exec(r.Context(), `INSERT INTO quality_notifications(id,owner_id,kind,message) VALUES($1,$2,'test','消息中心连接测试；自动调度状态不会改变')`, event, owner); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO notification_deliveries(id,event_id,channel_id,channel_revision) VALUES($1,$2,$3,$4)`, delivery, event, channel, revision); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.sendNotifications(r.Context(), delivery)
	var state, lastError string
	if err = a.db.QueryRow(r.Context(), `SELECT status,last_error FROM notification_deliveries WHERE id=$1`, delivery).Scan(&state, &lastError); err != nil {
		return err
	}
	writeData(w, 200, map[string]any{"delivery_id": delivery, "status": state, "message": lastError})
	return nil
}
func (a *App) retryNotificationHandler(w http.ResponseWriter, r *http.Request) error {
	id, err := requiredID(r, "deliveryID")
	if err != nil {
		return err
	}
	tag, err := a.db.Exec(r.Context(), `UPDATE notification_deliveries d SET status='pending',attempts=0,next_attempt_at=now(),lease_token=NULL,lease_until=NULL,last_error='' FROM notification_channels c,quality_notifications n WHERE d.id=$1 AND c.id=d.channel_id AND n.id=d.event_id AND c.owner_id=$2 AND n.owner_id=$2 AND d.status='failed' AND c.enabled AND c.revision=d.channel_revision AND n.created_at>now()-interval '1 hour' AND (n.source_generation IS NULL OR n.account_id IS NULL OR EXISTS(SELECT 1 FROM upstream_accounts a WHERE a.id=n.account_id AND a.source_generation=n.source_generation AND a.deleted_at IS NULL))`, id, identityFrom(r).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return &apiError{Status: 409, Code: "RETRY_UNAVAILABLE", Message: "仅可重试一小时内、渠道未变更且已启用的失败投递"}
	}
	writeData(w, 200, map[string]bool{"queued": true})
	return nil
}

func (a *App) runNotifications(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendNotifications(ctx, "")
		}
	}
}
func (a *App) sendNotifications(ctx context.Context, onlyID string) {
	_, err := a.db.Exec(ctx, `UPDATE notification_deliveries d SET status=CASE WHEN n.created_at<now()-interval '1 hour' THEN 'expired' ELSE 'cancelled' END,lease_token=NULL,lease_until=NULL,last_error=CASE WHEN n.created_at<now()-interval '1 hour' THEN '事件已超过一小时，停止自动投递' ELSE '通知渠道或账号来源已变更' END FROM notification_channels c,quality_notifications n WHERE d.channel_id=c.id AND d.event_id=n.id AND d.status IN('pending','sending') AND (d.lease_until IS NULL OR d.lease_until<now()) AND (n.created_at<now()-interval '1 hour' OR c.revision<>d.channel_revision OR (NOT c.enabled AND n.kind<>'test') OR (n.source_generation IS NOT NULL AND n.account_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM upstream_accounts a WHERE a.id=n.account_id AND a.source_generation=n.source_generation AND a.deleted_at IS NULL)))`)
	if err != nil {
		a.logger.Warn("notification expiry failed", "error", err)
		return
	}
	_, err = a.db.Exec(ctx, `UPDATE notification_deliveries SET status='failed',lease_token=NULL,lease_until=NULL,last_error='投递中断，已达到五次尝试上限' WHERE status='sending' AND attempts>=5 AND lease_until<now()`)
	if err != nil {
		a.logger.Warn("notification interrupted claim failed", "error", err)
		return
	}
	for i := 0; i < 10; i++ {
		var id, channel, endpointCipher, signCipher, purpose, provider, token string
		var attempts int
		var revision int64
		var m notificationMessage
		err = a.db.QueryRow(ctx, `WITH candidate AS(SELECT d.id FROM notification_deliveries d JOIN notification_channels c ON c.id=d.channel_id JOIN quality_notifications n ON n.id=d.event_id WHERE d.status IN('pending','sending') AND d.attempts<5 AND d.next_attempt_at<=now() AND (d.lease_until IS NULL OR d.lease_until<now()) AND c.revision=d.channel_revision AND (n.source_generation IS NULL OR n.account_id IS NULL OR EXISTS(SELECT 1 FROM upstream_accounts a WHERE a.id=n.account_id AND a.source_generation=n.source_generation AND a.deleted_at IS NULL)) AND (c.enabled OR n.kind='test') AND ($1='' OR d.id::text=$1) ORDER BY d.created_at,d.id FOR UPDATE OF d,c SKIP LOCKED LIMIT 1) UPDATE notification_deliveries d SET status='sending',attempts=d.attempts+1,lease_until=now()+interval '30 seconds',lease_token=gen_random_uuid() FROM candidate x,notification_channels c,quality_notifications n WHERE d.id=x.id AND c.id=d.channel_id AND n.id=d.event_id RETURNING d.id::text,c.id::text,d.channel_revision,d.lease_token::text,d.attempts,c.webhook_ciphertext,COALESCE(c.signing_secret_ciphertext,''),c.secret_purpose,c.provider,n.id::text,n.kind,n.category,n.severity,n.message,n.created_at,n.context`, onlyID).Scan(&id, &channel, &revision, &token, &attempts, &endpointCipher, &signCipher, &purpose, &provider, &m.ID, &m.Kind, &m.Category, &m.Severity, &m.Message, &m.CreatedAt, &m.Context)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			a.logger.Warn("notification claim failed", "error", err)
			return
		}
		endpoint, e := a.cipher.Decrypt(endpointCipher, purpose)
		secret := ""
		if e == nil && signCipher != "" {
			secret, e = a.cipher.Decrypt(signCipher, purpose+":sign")
		}
		if e == nil {
			delivery, cancel := context.WithTimeout(ctx, 10*time.Second)
			e = sendNotificationWebhook(delivery, a.httpClient, endpoint, provider, secret, m)
			cancel()
		}
		state, message := "delivered", ""
		if e != nil {
			state = "pending"
			if attempts >= 5 {
				state = "failed"
			}
			message = truncateError(e)
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, err = a.db.Exec(persist, `UPDATE notification_deliveries SET status=$3,delivered_at=CASE WHEN $3='delivered' THEN now() ELSE NULL END,last_error=$4,lease_until=NULL,lease_token=NULL,next_attempt_at=now()+$5*interval '1 second' WHERE id=$1 AND lease_token=$2 AND status='sending'`, id, token, state, message, 30*(1<<min(attempts, 5)))
		cancel()
		if err != nil {
			a.logger.Warn("notification receipt persistence failed", "error", err)
		}
		if onlyID != "" {
			return
		}
	}
}

func notificationSettingsMoved(http.ResponseWriter, *http.Request) error {
	return &apiError{Status: 410, Code: "MESSAGE_CENTER_MOVED", Message: "通知配置已统一到消息中心，请刷新页面"}
}
