package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

func init() {
	// 飞书渠道按加密配置里的 mode 字段路由：
	//   mode = "app"     → 应用机器人，发交互卡片（带已处理/不处理按钮）
	//   mode = ""/"webhook" → 群机器人 webhook，发文本（原行为）
	Register(storage.NotifyFeishu, func(raw string) (Notifier, error) {
		var probe struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal([]byte(raw), &probe)
		if strings.EqualFold(probe.Mode, "app") {
			return newFeishuApp(raw)
		}
		return newFeishuWebhook(raw)
	})
}

// feishuWebhookConfig 群机器人 webhook 配置。
type feishuWebhookConfig struct {
	WebhookURL string `json:"webhook_url"`
	Secret     string `json:"secret,omitempty"`
}

type feishuWebhook struct {
	cfg  feishuWebhookConfig
	http *resty.Client
}

func newFeishuWebhook(raw string) (*feishuWebhook, error) {
	var cfg feishuWebhookConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	if cfg.WebhookURL == "" {
		return nil, errors.New("feishu webhook_url is required")
	}
	return &feishuWebhook{cfg: cfg, http: resty.New()}, nil
}

func (f *feishuWebhook) Type() storage.NotificationChannelType { return storage.NotifyFeishu }

// Send 发群机器人文本消息（原行为，向后兼容）。
func (f *feishuWebhook) Send(ctx context.Context, msg Message) error {
	body := map[string]any{
		"msg_type": "text",
		"content": map[string]string{
			"text": msg.Subject + "\n" + msg.Body,
		},
	}
	if f.cfg.Secret != "" {
		ts := time.Now().Unix()
		stringToSign := strconv.FormatInt(ts, 10) + "\n" + f.cfg.Secret
		mac := hmac.New(sha256.New, []byte(stringToSign))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		body["timestamp"] = strconv.FormatInt(ts, 10)
		body["sign"] = sign
	}
	resp, err := f.http.R().
		SetContext(ctx).
		SetBody(body).
		Post(f.cfg.WebhookURL)
	if err != nil {
		return err
	}
	if resp.IsError() {
		return errors.New("feishu returned " + resp.Status())
	}
	return nil
}
