package notify

import (
	"encoding/json"
	"testing"
)

func TestRedactConfig(t *testing.T) {
	t.Run("feishu app masks app_secret keeps mode and ids", func(t *testing.T) {
		raw := `{"mode":"app","app_id":"cli_xxx","app_secret":"topsecret","chat_id":"oc_yyy"}`
		got := RedactConfig("feishu", raw)
		if got["mode"] != "app" {
			t.Fatalf("mode should be visible, got %v", got["mode"])
		}
		if got["app_id"] != "cli_xxx" {
			t.Fatalf("app_id should be visible, got %v", got["app_id"])
		}
		if got["chat_id"] != "oc_yyy" {
			t.Fatalf("chat_id should be visible, got %v", got["chat_id"])
		}
		if got["app_secret"] != RedactedSentinel {
			t.Fatalf("app_secret should be redacted, got %v", got["app_secret"])
		}
	})

	t.Run("feishu webhook masks secret keeps webhook_url", func(t *testing.T) {
		raw := `{"mode":"webhook","webhook_url":"https://open.feishu.cn/hook/abc","secret":"sign"}`
		got := RedactConfig("feishu", raw)
		if got["webhook_url"] != "https://open.feishu.cn/hook/abc" {
			t.Fatalf("webhook_url should be visible, got %v", got["webhook_url"])
		}
		if got["secret"] != RedactedSentinel {
			t.Fatalf("secret should be redacted, got %v", got["secret"])
		}
	})

	t.Run("empty optional secret stays absent", func(t *testing.T) {
		raw := `{"mode":"webhook","webhook_url":"https://x/hook"}`
		got := RedactConfig("feishu", raw)
		if _, ok := got["secret"]; ok {
			t.Fatalf("absent secret should not appear, got %v", got["secret"])
		}
	})

	t.Run("empty raw returns nil", func(t *testing.T) {
		if got := RedactConfig("feishu", ""); got != nil {
			t.Fatalf("empty raw should return nil, got %v", got)
		}
	})

	t.Run("invalid json returns nil", func(t *testing.T) {
		if got := RedactConfig("feishu", "{not json"); got != nil {
			t.Fatalf("invalid json should return nil, got %v", got)
		}
	})

	t.Run("telegram masks bot_token keeps chat_id", func(t *testing.T) {
		raw := `{"bot_token":"123:ABC","chat_id":"-100"}`
		got := RedactConfig("telegram", raw)
		if got["chat_id"] != "-100" {
			t.Fatalf("chat_id should be visible, got %v", got["chat_id"])
		}
		if got["bot_token"] != RedactedSentinel {
			t.Fatalf("bot_token should be redacted, got %v", got["bot_token"])
		}
	})
}

func TestMergeConfig(t *testing.T) {
	t.Run("sentinel keeps existing secret", func(t *testing.T) {
		incoming := `{"mode":"app","app_id":"cli_xxx","app_secret":"` + RedactedSentinel + `","chat_id":"oc_new"}`
		existing := `{"mode":"app","app_id":"cli_xxx","app_secret":"real_secret","chat_id":"oc_old"}`
		merged, err := MergeConfig(incoming, existing)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(merged), &m); err != nil {
			t.Fatal(err)
		}
		if m["app_secret"] != "real_secret" {
			t.Fatalf("sentinel should keep existing secret, got %v", m["app_secret"])
		}
		if m["chat_id"] != "oc_new" {
			t.Fatalf("non-sentinel should override, got %v", m["chat_id"])
		}
	})

	t.Run("new secret value overrides", func(t *testing.T) {
		incoming := `{"mode":"app","app_id":"cli_xxx","app_secret":"brand_new","chat_id":"oc_x"}`
		existing := `{"mode":"app","app_id":"cli_xxx","app_secret":"real_secret","chat_id":"oc_old"}`
		merged, err := MergeConfig(incoming, existing)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(merged), &m); err != nil {
			t.Fatal(err)
		}
		if m["app_secret"] != "brand_new" {
			t.Fatalf("new value should override, got %v", m["app_secret"])
		}
	})

	t.Run("mode switch drops old keys", func(t *testing.T) {
		// app -> webhook: incoming 只有 webhook 字段，旧的 app_id/app_secret/chat_id 应被丢弃
		incoming := `{"mode":"webhook","webhook_url":"https://x/hook"}`
		existing := `{"mode":"app","app_id":"cli_xxx","app_secret":"real_secret","chat_id":"oc_old"}`
		merged, err := MergeConfig(incoming, existing)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(merged), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["app_secret"]; ok {
			t.Fatalf("app_secret should be dropped on mode switch, got %v", m["app_secret"])
		}
		if m["webhook_url"] != "https://x/hook" {
			t.Fatalf("webhook_url should be kept, got %v", m["webhook_url"])
		}
	})

	t.Run("sentinel with no existing field is dropped", func(t *testing.T) {
		incoming := `{"webhook_url":"https://x","secret":"` + RedactedSentinel + `"}`
		existing := `{"webhook_url":"https://x"}` // 无 secret
		merged, err := MergeConfig(incoming, existing)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(merged), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["secret"]; ok {
			t.Fatalf("sentinel with no existing value should be dropped, got %v", m["secret"])
		}
	})

	t.Run("empty existing still works", func(t *testing.T) {
		incoming := `{"mode":"app","app_id":"cli_x","app_secret":"` + RedactedSentinel + `","chat_id":"oc_x"}`
		merged, err := MergeConfig(incoming, "")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(merged), &m); err != nil {
			t.Fatal(err)
		}
		// existing 空 → 哨兵被删除，其余保留
		if _, ok := m["app_secret"]; ok {
			t.Fatalf("sentinel with empty existing should be dropped, got %v", m["app_secret"])
		}
		if m["app_id"] != "cli_x" {
			t.Fatalf("app_id should be kept, got %v", m["app_id"])
		}
	})
}
