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
			val, _ := btn["value"].(map[string]any)
			action, _ := val["action"].(string)
			if action == "handled" {
				foundHandled = true
				if val["alert_id"] != "aid-xyz" {
					t.Fatalf("handled alert_id = %v", val["alert_id"])
				}
				if val["channel_id"] != "7" {
					t.Fatalf("handled channel_id = %v", val["channel_id"])
				}
				if val["event"] != "balance_low" {
					t.Fatalf("handled event = %v", val["event"])
				}
			}
			if action == "ignored" {
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

// TestBuildFeishuCardActionsOnlyForBalanceLow 验证只有余额告警卡片带「已处理 / 不处理」
// 操作按钮，其余事件卡片只展示正文、没有按钮区。
//
// 背景：rate_changed 不走 dispatchAlert，Extra 里没有 alert_id；如果给它挂按钮，
// 回调端点拿到空 alert_id 会直接返回「无效的告警」，按钮形同虚设。因此按事件类型
// 收敛按钮范围：仅 balance_low 有 action。
func TestBuildFeishuCardActionsOnlyForBalanceLow(t *testing.T) {
	cases := []struct {
		name     string
		event    storage.NotificationEvent
		wantBtns bool
	}{
		{"balance_low 带按钮", storage.EventBalanceLow, true},
		{"rate_changed 无按钮", storage.EventRateChanged, false},
		{"login_failed 无按钮", storage.EventLoginFailed, false},
		{"captcha_failed 无按钮", storage.EventCaptchaFailed, false},
		{"monitor_failed 无按钮", storage.EventMonitorFailed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{
				Event:     tc.event,
				ChannelID: 7,
				Subject:   "告警",
				Body:      "正文",
				Extra: map[string]any{
					"alert_id":   "aid-xyz",
					"channel_id": uint(7),
				},
			}
			card := buildFeishuCard(msg)
			elements, _ := card["elements"].([]any)
			hasAction := false
			for _, el := range elements {
				m, _ := el.(map[string]any)
				if m["tag"] == "action" {
					hasAction = true
					break
				}
			}
			if hasAction != tc.wantBtns {
				t.Fatalf("event=%s action=%v want=%v", tc.event, hasAction, tc.wantBtns)
			}
			// 正文 div 对所有事件都应存在。
			if len(elements) == 0 {
				t.Fatalf("event=%s card has no elements", tc.event)
			}
			first, _ := elements[0].(map[string]any)
			if first["tag"] != "div" {
				t.Fatalf("event=%s first element tag=%v want=div", tc.event, first["tag"])
			}
		})
	}
}
