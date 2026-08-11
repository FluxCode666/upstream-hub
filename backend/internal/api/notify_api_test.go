package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

func TestNotifyChannelResponse_RedactsAndEchoesMode(t *testing.T) {
	cipher, err := crypto.NewCipher("test-secret-key-for-api-test")
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cipher: cipher, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// 飞书 app 配置：app_secret 应脱敏，mode/app_id/chat_id 应可见
	cfg := `{"mode":"app","app_id":"cli_abc","app_secret":"topsecret","chat_id":"oc_xyz"}`
	enc, err := cipher.Encrypt(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ch := &storage.NotificationChannel{
		ID: 1, Name: "苏糖", Type: storage.NotifyFeishu, ConfigCipher: enc, Enabled: true,
	}

	resp := notifyChannelResponse(d, ch)
	data, _ := resp["data"].(notifyChannelDTO)
	if data.ConfigPreview == nil {
		t.Fatal("config_preview should be present")
	}
	if data.ConfigPreview["mode"] != "app" {
		t.Fatalf("mode should echo app, got %v", data.ConfigPreview["mode"])
	}
	if data.ConfigPreview["app_secret"] != "__REDACTED__" {
		t.Fatalf("app_secret should be redacted, got %v", data.ConfigPreview["app_secret"])
	}
	if data.ConfigPreview["app_id"] != "cli_abc" {
		t.Fatalf("app_id should be visible, got %v", data.ConfigPreview["app_id"])
	}
	// ConfigCipher must never leak
	b, _ := json.Marshal(resp)
	if strings.Contains(string(b), "topsecret") {
		t.Fatalf("plaintext secret leaked in response: %s", string(b))
	}
}

func TestNotifyChannelResponse_DecryptFailOmitsPreview(t *testing.T) {
	cipher, err := crypto.NewCipher("test-secret-key-for-api-test")
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cipher: cipher, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// 用错误的密文 → 解密失败 → config_preview 应缺失
	ch := &storage.NotificationChannel{
		ID: 2, Name: "x", Type: storage.NotifyFeishu, ConfigCipher: "not-valid-ciphertext", Enabled: true,
	}
	resp := notifyChannelResponse(d, ch)
	data, _ := resp["data"].(notifyChannelDTO)
	if data.ConfigPreview != nil {
		t.Fatalf("config_preview should be nil on decrypt failure, got %v", data.ConfigPreview)
	}
}
