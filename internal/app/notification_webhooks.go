package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type notificationMessage struct {
	ID        string          `json:"event_id"`
	Kind      string          `json:"kind"`
	Category  string          `json:"category"`
	Severity  string          `json:"severity"`
	Message   string          `json:"message"`
	CreatedAt time.Time       `json:"created_at"`
	Context   json.RawMessage `json:"context"`
}

func normalizeNotificationURL(raw, provider string, allowPrivate bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && provider == "auto" {
		provider = notificationProvider(u.Hostname())
	}
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 4096 || (u.Scheme != "https" && !(allowPrivate && provider == "webhook" && u.Scheme == "http")) {
		return "", errors.New("请填写有效的 HTTPS Webhook 地址")
	}
	host := strings.ToLower(u.Hostname())
	if provider == "auto" {
		provider = notificationProvider(host)
	}
	switch provider {
	case "wecom":
		return normalizeWeComWebhookURL(raw)
	case "feishu":
		token := strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")
		if (host != "open.feishu.cn" && host != "open.larksuite.com") || u.Scheme != "https" || u.Port() != "" || u.RawQuery != "" || token == u.Path || token == "" || len(token) > 128 || strings.ContainsAny(token, "/ \r\n") {
			return "", errors.New("请填写飞书或 Lark 自定义机器人的 Webhook 地址")
		}
	case "webhook":
		if notificationProvider(host) != "webhook" {
			return "", errors.New("请为飞书或企业微信地址选择对应渠道类型")
		}
	default:
		return "", errors.New("通知渠道类型无效")
	}
	return u.String(), nil
}
func notificationProvider(host string) string {
	switch strings.ToLower(host) {
	case "open.feishu.cn", "open.larksuite.com":
		return "feishu"
	case "qyapi.weixin.qq.com":
		return "wecom"
	default:
		return "webhook"
	}
}
func feishuSignature(timestamp int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(strconv.FormatInt(timestamp, 10)+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func notificationText(m notificationMessage) string {
	title := map[string]string{"quality": "上游质量", "price": "采购价格", "balance": "余额预警", "collector": "采集故障", "controller": "控制状态", "runway": "余额续航", "test": "连接测试"}[m.Category]
	if title == "" {
		title = "服务通知"
	}
	state := map[string]string{"warning": "提醒", "critical": "紧急", "recovery": "恢复", "info": "信息"}[m.Severity]
	var c struct {
		Groups []string `json:"groups"`
	}
	_ = json.Unmarshal(m.Context, &c)
	text := "Upstream Pilot · " + title + " · " + state + "\n时间：" + m.CreatedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05") + "\n事件：" + m.ID + "\n" + m.Message
	if len(c.Groups) > 0 {
		text += "\n影响分组：" + strings.Join(c.Groups, "、")
	}
	// Upstream account/error text must never become an @all mention or bot markup.
	return strings.NewReplacer("<", "＜", ">", "＞").Replace(text)
}
func limitMessageBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	text = text[:max-3]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}
func sendNotificationWebhook(ctx context.Context, client *http.Client, endpoint, provider, secret string, m notificationMessage) error {
	legacy := provider == "auto"
	if provider == "auto" {
		u, e := url.Parse(endpoint)
		if e != nil {
			return errors.New("通知地址无效")
		}
		provider = notificationProvider(u.Hostname())
	}
	text := notificationText(m)
	if provider == "wecom" {
		return sendWeComWebhook(ctx, client, endpoint, limitMessageBytes(text, 3800))
	}
	var payload any = m
	if legacy && provider == "webhook" {
		payload = map[string]any{"event_id": m.ID, "kind": m.Kind, "message": m.Message}
	}
	if provider == "feishu" {
		data := map[string]any{"msg_type": "text", "content": map[string]string{"text": limitMessageBytes(text, 12000)}}
		if secret != "" {
			timestamp := time.Now().Unix()
			data["timestamp"] = strconv.FormatInt(timestamp, 10)
			data["sign"] = feishuSignature(timestamp, secret)
		}
		payload = data
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("无法创建通知请求")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// Never forward webhook credentials or message contents across redirects.
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return errors.New("通知发送失败，请检查网络和接收地址")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return errors.New("通知回执读取失败或过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知接收端返回 HTTP %d", resp.StatusCode)
	}
	if provider == "feishu" {
		var result struct {
			Code       *int `json:"code"`
			LegacyCode *int `json:"StatusCode"`
		}
		if json.Unmarshal(body, &result) != nil || result.Code == nil && result.LegacyCode == nil {
			return errors.New("飞书未返回有效的业务回执")
		}
		for _, code := range []*int{result.Code, result.LegacyCode} {
			if code != nil && *code != 0 {
				return fmt.Errorf("飞书拒绝消息（错误码 %d），请检查签名、关键词和机器人配置", *code)
			}
		}
	}
	return nil
}

func normalizeWeComWebhookURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "qyapi.weixin.qq.com") || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Path != "/cgi-bin/webhook/send" {
		return "", errors.New("企业微信机器人地址无效")
	}
	key := strings.TrimSpace(u.Query().Get("key"))
	if key == "" || len(key) > 256 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("企业微信机器人密钥缺失")
	}
	return u.String(), nil
}
func sendWeComWebhook(ctx context.Context, client *http.Client, endpoint, content string) error {
	data, _ := json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": content}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("无法创建企业微信请求")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return errors.New("企业微信通知发送失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return errors.New("企业微信回执无效")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("企业微信返回 HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code *int `json:"errcode"`
	}
	if json.Unmarshal(body, &result) != nil || result.Code == nil {
		return errors.New("企业微信未返回有效的业务回执")
	}
	if *result.Code != 0 {
		return fmt.Errorf("企业微信拒绝消息（错误码 %d）", *result.Code)
	}
	return nil
}
