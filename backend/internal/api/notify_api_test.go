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

// TestNotifyChannelResponses_ListShape 锁定列表响应结构：每个元素必须是渠道对象本身
// （id/name/type/config_preview），不能再套一层 {"data":...} 外壳。
// 否则前端 useApi<NotificationChannel[]> 拿到的元素没有 id/name/type，
// 导致列表全部显示异常 + 删除/测试请求打到 /channels/undefined 报错。
func TestNotifyChannelResponses_ListShape(t *testing.T) {
	cipher, err := crypto.NewCipher("test-secret-key-for-api-test")
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cipher: cipher, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	enc, _ := cipher.Encrypt(`{"mode":"webhook","webhook_url":"https://x/hook"}`)
	list := []storage.NotificationChannel{
		{ID: 1, Name: "苏糖", Type: storage.NotifyFeishu, ConfigCipher: enc, Enabled: true},
		{ID: 2, Name: "运维群", Type: storage.NotifyTelegram, ConfigCipher: enc, Enabled: false},
	}

	out := notifyChannelResponses(d, list)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// 反序列化成通用结构，验证每个元素直接含 id/name/type，没有多余的 data 外壳
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 2 {
		t.Fatalf("want 2 elements, got %d", len(arr))
	}
	for i, el := range arr {
		if _, ok := el["data"]; ok {
			t.Fatalf("element %d must NOT be wrapped in {data:...}; got keys %v", i, keysOf(el))
		}
		for _, k := range []string{"id", "name", "type"} {
			if _, ok := el[k]; !ok {
				t.Fatalf("element %d missing key %q; got keys %v", i, k, keysOf(el))
			}
		}
	}
	// config_preview 在 webhook 模式下应存在且含 mode
	var first map[string]any
	if err := json.Unmarshal(arr[0]["config_preview"], &first); err != nil {
		t.Fatalf("config_preview should be a JSON object: %v", err)
	}
	if first["mode"] != "webhook" {
		t.Fatalf("config_preview.mode should be webhook, got %v", first["mode"])
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
