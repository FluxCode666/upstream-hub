package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

const (
	feishuHost              = "https://open.feishu.cn"
	feishuTokenRefreshLead  = 5 * time.Minute
	feishuExtraKeyMessageID = "feishu_message_id"
)

// feishuAppConfig 飞书自建应用配置。mode=app 时使用。
//
// AppID/AppSecret 在飞书开放平台应用"凭证与基础信息"获取；
// ChatID 是目标群的 chat_id（oc_ 开头），需先把机器人拉入群，再调
// GET /open-apis/im/v1/chats（权限 im:chat:readonly）取得。
type feishuAppConfig struct {
	Mode      string `json:"mode"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	ChatID    string `json:"chat_id"`
}

type feishuApp struct {
	cfg   feishuAppConfig
	http  *resty.Client
	token *feishuToken
	host  string // 可注入，默认 feishuHost；测试时指向 httptest.Server
}

// feishuToken 缓存 tenant_access_token。加锁刷新，避免并发请求重复换 token。
type feishuToken struct {
	mu       sync.Mutex
	value    string
	expiresAt time.Time
}

func (t *feishuToken) expired() bool {
	return time.Now().After(t.expiresAt.Add(-feishuTokenRefreshLead))
}

func newFeishuApp(raw string) (*feishuApp, error) {
	var cfg feishuAppConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, errors.New("feishu app mode requires app_id and app_secret")
	}
	if cfg.ChatID == "" {
		return nil, errors.New("feishu app mode requires chat_id")
	}
	return &feishuApp{
		cfg:   cfg,
		http:  resty.New(),
		token: &feishuToken{},
		host:  feishuHost,
	}, nil
}

func (f *feishuApp) Type() storage.NotificationChannelType { return storage.NotifyFeishu }

// tenantToken 获取（必要时刷新）tenant_access_token。
func (f *feishuApp) tenantToken(ctx context.Context) (string, error) {
	f.token.mu.Lock()
	defer f.token.mu.Unlock()
	if f.token.value != "" && !f.token.expired() {
		return f.token.value, nil
	}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	r, err := f.http.R().
		SetContext(ctx).
		SetBody(map[string]string{
			"app_id":     f.cfg.AppID,
			"app_secret": f.cfg.AppSecret,
		}).
		SetResult(&resp).
		Post(f.host + "/open-apis/auth/v3/tenant_access_token/internal")
	if err != nil {
		return "", err
	}
	if r.IsError() || resp.Code != 0 {
		return "", fmt.Errorf("feishu tenant_token: code=%d msg=%s", resp.Code, resp.Msg)
	}
	f.token.value = resp.TenantAccessToken
	f.token.expiresAt = time.Now().Add(time.Duration(resp.Expire) * time.Second)
	return f.token.value, nil
}

// Send 发交互卡片。成功后把飞书 message_id 写回 msg.Extra（供调用方落库 AlertState）。
func (f *feishuApp) Send(ctx context.Context, msg Message) error {
	token, err := f.tenantToken(ctx)
	if err != nil {
		return err
	}
	card := buildFeishuCard(msg)
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	r, err := f.http.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+token).
		SetQueryParam("receive_id_type", "chat_id").
		SetBody(map[string]string{
			"receive_id": f.cfg.ChatID,
			"msg_type":   "interactive",
			"content":    string(cardJSON),
		}).
		SetResult(&resp).
		Post(f.host + "/open-apis/im/v1/messages")
	if err != nil {
		return err
	}
	if r.IsError() || resp.Code != 0 {
		return fmt.Errorf("feishu send message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if msg.Extra != nil && resp.Data.MessageID != "" {
		msg.Extra[feishuExtraKeyMessageID] = resp.Data.MessageID
	}
	return nil
}

// buildFeishuCard 构造告警交互卡片。
// 按钮的 value 带 alert_id / action / channel_id / event，飞书点按后回调端点据此处理。
func buildFeishuCard(msg Message) map[string]any {
	alertID, _ := msg.Extra["alert_id"].(string)
	channelID, _ := msg.Extra["channel_id"].(uint)
	event := string(msg.Event)

	btn := func(text string, action string, btnType string) map[string]any {
		return map[string]any{
			"tag": "button",
			"text": map[string]string{
				"tag":     "plain_text",
				"content": text,
			},
			"type": btnType,
			"value": map[string]string{
				"action":     action,
				"alert_id":   alertID,
				"channel_id": strconv.FormatUint(uint64(channelID), 10),
				"event":      event,
			},
		}
	}
	return map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": msg.Subject,
			},
		},
		"elements": []any{
			map[string]any{
				"tag":  "div",
				"text": map[string]string{"tag": "lark_md", "content": escapeFeishuMd(msg.Body)},
			},
			map[string]any{
				"tag": "action",
				"actions": []any{
					btn("✅ 已处理", "handled", "primary"),
					btn("🚫 不处理", "ignored", "danger"),
				},
			},
		},
	}
}

// escapeFeishuMd 把正文的 < > & 转义，避免 lark_md 注入。
func escapeFeishuMd(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// NewFeishuAppFromConfig 供回调端点"就地更新卡片"复用：用解密后的 app 配置构造 client。
// 与 newFeishuApp 的区别是返回导出类型，供 api 包使用。
func NewFeishuAppFromConfig(raw string) (*feishuApp, error) {
	return newFeishuApp(raw)
}

// UpdateCard 就地把卡片改成"已处理/不处理"终态文案。
func (f *feishuApp) UpdateCard(ctx context.Context, messageID, statusText string) error {
	token, err := f.tenantToken(ctx)
	if err != nil {
		return err
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title": map[string]string{"tag": "plain_text", "content": statusText},
		},
		"elements": []any{
			map[string]any{
				"tag":  "div",
				"text": map[string]string{"tag": "lark_md", "content": statusText},
			},
		},
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	r, err := f.http.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+token).
		SetBody(map[string]string{
			"msg_type": "interactive",
			"content":  string(cardJSON),
		}).
		SetResult(&resp).
		Patch(f.host + "/open-apis/im/v1/messages/" + messageID)
	if err != nil {
		return err
	}
	if r.IsError() || resp.Code != 0 {
		return fmt.Errorf("feishu update card: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
