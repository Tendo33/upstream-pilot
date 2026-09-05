package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeWeComWebhookURL(t *testing.T) {
	valid := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-key"
	if normalized, err := normalizeWeComWebhookURL("  " + valid + "  "); err != nil || normalized != valid {
		t.Fatalf("normalizeWeComWebhookURL() = %q, %v", normalized, err)
	}

	invalid := []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-key",
		"https://qyapi.weixin.qq.com.evil.example/cgi-bin/webhook/send?key=test-key",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://qyapi.weixin.qq.com/cgi-bin/other?key=test-key",
		"https://user@qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-key",
	}
	for _, value := range invalid {
		if _, err := normalizeWeComWebhookURL(value); err == nil {
			t.Errorf("normalizeWeComWebhookURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSendWeComWebhookUsesMarkdownPayload(t *testing.T) {
	var method, contentType, messageType, content string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			MessageType string `json:"msgtype"`
			Markdown    struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		_ = json.Unmarshal(body, &payload)
		messageType = payload.MessageType
		content = payload.Markdown.Content
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	if err := sendWeComWebhook(context.Background(), server.Client(), server.URL, "## 余额预警"); err != nil {
		t.Fatalf("sendWeComWebhook() error = %v", err)
	}
	if method != http.MethodPost || contentType != "application/json; charset=utf-8" || messageType != "markdown" || content != "## 余额预警" {
		t.Fatalf("unexpected request: method=%q content-type=%q msgtype=%q content=%q", method, contentType, messageType, content)
	}
}

func TestSendWeComWebhookRejectsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook url"}`))
	}))
	defer server.Close()

	err := sendWeComWebhook(context.Background(), server.Client(), server.URL, "test")
	if err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("sendWeComWebhook() error = %v", err)
	}
}

func TestFormatBalanceAlertNumberPreservesIntegerZeros(t *testing.T) {
	values := map[float64]string{0: "0", 0.125: "0.125", 10: "10", 1000: "1000"}
	for value, expected := range values {
		if actual := formatBalanceAlertNumber(value); actual != expected {
			t.Errorf("formatBalanceAlertNumber(%v) = %q, want %q", value, actual, expected)
		}
	}
}
