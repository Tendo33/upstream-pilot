package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFeishuSigningAndStrictReceipts(t *testing.T) {
	const secret = "synthetic-signing-secret"
	if got := feishuSignature(1700000000, secret); got != "6xIUSxIl44gnnWVqSOU/+VndIHzBElfLIeAHbjeSvtQ=" {
		t.Fatal("signature differs from independent HMAC vector")
	}
	for _, tc := range []struct {
		body string
		ok   bool
	}{{`{"code":0}`, true}, {`{"StatusCode":0}`, true}, {`{"code":19021,"msg":"private-token-do-not-log"}`, false}, {`{}`, false}, {`{"code":0,"StatusCode":1}`, false}, {`{"code":"0"}`, false}, {`<html>success</html>`, false}, {strings.Repeat("x", 65537), false}} {
		t.Run(tc.body[:min(len(tc.body), 35)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var p struct {
					Type    string `json:"msg_type"`
					Time    string `json:"timestamp"`
					Sign    string `json:"sign"`
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if json.Unmarshal(raw, &p) != nil {
					t.Error("bad payload")
				}
				timestamp, _ := strconv.ParseInt(p.Time, 10, 64)
				if p.Type != "text" || p.Sign != feishuSignature(timestamp, secret) || timestamp < time.Now().Unix()-10 || strings.Contains(string(raw), secret) || strings.Contains(p.Content.Text, "<at") {
					t.Error("bad signature or unsafe notification content")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			err := sendNotificationWebhook(context.Background(), server.Client(), server.URL, "feishu", secret, notificationMessage{ID: "test-id", Category: "quality", Severity: "warning", Message: `<at user_id="all">everyone</at>`, CreatedAt: time.Now()})
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "private-token") {
				t.Fatal("receiver response leaked")
			}
		})
	}
}
func TestNotificationURLProviderAndRedirectProtection(t *testing.T) {
	if _, err := normalizeNotificationURL("http://127.0.0.1:33888/notify", "auto", true); err != nil {
		t.Fatal("legacy private webhook lost", err)
	}
	for _, tc := range []struct {
		url, provider string
		ok            bool
	}{
		{"https://open.feishu.cn/open-apis/bot/v2/hook/test-token", "feishu", true},
		{"https://open.larksuite.com/open-apis/bot/v2/hook/test-token", "feishu", true},
		{"https://open.feishu.cn.evil.test/open-apis/bot/v2/hook/token", "feishu", false},
		{"https://open.feishu.cn/open-apis/bot/v2/hook/token?key=x", "feishu", false},
		{"https://open.feishu.cn/open-apis/bot/v2/hook/token", "webhook", false},
		{"https://user@receiver.test/hook", "webhook", false},
		{"http://receiver.test/hook", "webhook", false},
	} {
		_, err := normalizeNotificationURL(tc.url, tc.provider, false)
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
	var forwards atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwards.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	if err := sendNotificationWebhook(context.Background(), source.Client(), source.URL, "webhook", "", notificationMessage{}); err == nil || forwards.Load() != 0 {
		t.Fatal("redirect forwarded message")
	}
	text := limitMessageBytes(strings.Repeat("报警", 3000), 3800)
	if len(text) > 3800 || !utf8.ValidString(text) {
		t.Fatal("invalid byte truncation")
	}
}
