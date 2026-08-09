package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

func TestNewFeishuAppRequiresCredentials(t *testing.T) {
	if _, err := newFeishuApp(`{"mode":"app"}`); err == nil {
		t.Fatal("expected error for missing app_id/secret/chat_id")
	}
}

func TestNewFeishuAppOK(t *testing.T) {
	n, err := newFeishuApp(`{"mode":"app","app_id":"x","app_secret":"y","chat_id":"oc_1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.cfg.ChatID != "oc_1" {
		t.Fatalf("chat_id = %q", n.cfg.ChatID)
	}
}

func TestFeishuFactoryRoutesByMode(t *testing.T) {
	webhookN, err := Build(&storage.NotificationChannel{Type: storage.NotifyFeishu}, `{"webhook_url":"https://h"}`)
	if err != nil {
		t.Fatalf("webhook build: %v", err)
	}
	if _, ok := webhookN.(*feishuWebhook); !ok {
		t.Fatalf("expected *feishuWebhook, got %T", webhookN)
	}

	appN, err := Build(&storage.NotificationChannel{Type: storage.NotifyFeishu}, `{"mode":"app","app_id":"x","app_secret":"y","chat_id":"oc_1"}`)
	if err != nil {
		t.Fatalf("app build: %v", err)
	}
	if _, ok := appN.(*feishuApp); !ok {
		t.Fatalf("expected *feishuApp, got %T", appN)
	}
}

func TestFeishuAppSendsCardWithButtonValues(t *testing.T) {
	var sentBody map[string]any
	msgRequestReached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 区分 token 请求和 message 请求，分别返回各自的响应体。
		switch {
		case strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-abc","expire":7200}`))
			return
		case strings.HasSuffix(r.URL.Path, "/messages"):
			msgRequestReached = true
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"message_id":"om_123"}}`))
			return
		}
		t.Logf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := newFeishuApp(`{"mode":"app","app_id":"x","app_secret":"y","chat_id":"oc_1"}`)
	if err != nil {
		t.Fatalf("newFeishuApp: %v", err)
	}
	// 把飞书 host 指向测试服务器：token 和 message 接口都走 srv.URL。
	n.host = srv.URL

	msg := Message{
		Event:     storage.EventBalanceLow,
		ChannelID: 7,
		Subject:   "余额告警",
		Body:      "当前余额 1.0",
		Extra: map[string]any{
			"alert_id":   "aid-xyz",
			"channel_id": uint(7),
		},
	}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !msgRequestReached {
		t.Fatal("message request never reached server")
	}

	content, _ := sentBody["content"].(string)
	if content == "" {
		t.Fatalf("no content sent: %v", sentBody)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("card parse: %v", err)
	}
	elements, _ := card["elements"].([]any)
	var foundHandled, foundIgnored bool
	for _, el := range elements {
		m, _ := el.(map[string]any)
		if m["tag"] != "action" {
			continue
		}
		actions, _ := m["actions"].([]any)
		for _, a := range actions {
			btn, _ := a.(map[string]any)
			if btn["tag"] != "button" {
				continue
			}
			// 按钮仅作视觉占位，不带 value（无回调闭环）。
			if _, hasValue := btn["value"]; hasValue {
				t.Fatalf("button should not carry value: %v", btn["value"])
			}
			t, _ := btn["text"].(map[string]any)
			content, _ := t["content"].(string)
			if content == "✅ 已处理" {
				foundHandled = true
			}
			if content == "🚫 不处理" {
				foundIgnored = true
			}
		}
	}
	if !foundHandled || !foundIgnored {
		t.Fatalf("missing buttons: handled=%v ignored=%v", foundHandled, foundIgnored)
	}
	// message_id 应回写到传递的 Extra map（同 map 引用）。
	if msg.Extra["feishu_message_id"] != "om_123" {
		t.Fatalf("message_id not written back: %v", msg.Extra["feishu_message_id"])
	}
}
