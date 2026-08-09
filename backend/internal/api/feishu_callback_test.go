package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// feishuEncrypt 是飞书加密方向的逆运算，用于在测试里造加密 body。
// 算法与 feishuDecrypt 互逆：随机 16 字节 IV + sha256(key) AES-256-CBC 加密 + base64。
func feishuEncrypt(plain, key string) (string, error) {
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:sha256.Size])
	if err != nil {
		return "", err
	}
	// PKCS7 填充到 block 整数倍。
	pt := []byte(plain)
	pad := aes.BlockSize - len(pt)%aes.BlockSize
	for i := 0; i < pad; i++ {
		pt = append(pt, byte(pad))
	}
	iv := make([]byte, aes.BlockSize)
	for i := range iv { // 测试用确定性 IV，不要求密码学随机性。
		iv[i] = byte(i)
	}
	// 飞书密文 = IV || 密文，CBC 加密。
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, pt)
	return base64.StdEncoding.EncodeToString(append(iv, ct...)), nil
}

// TestFeishuDecrypt_RoundTrip 加密一段明文再解密，应完全还原。
// 顺带覆盖飞书真实场景的 JSON 形态（url_verification 探测）。
func TestFeishuDecrypt_RoundTrip(t *testing.T) {
	key := "mt-5S2NOabtrOnUcVep0Nn3ClyQyNdXFWjzl28gmBUk"
	cases := []string{
		`{"type":"url_verification","challenge":"abc123"}`,
		`{"type":"card.action.trigger","action":{"value":{"alert_id":"a1"}}}`,
		`{"中文key":"值 with spaces"}`,
	}
	for _, plain := range cases {
		enc, err := feishuEncrypt(plain, key)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		got, err := feishuDecrypt(enc, key)
		if err != nil {
			t.Fatalf("decrypt %q: %v", plain, err)
		}
		if string(got) != plain {
			t.Fatalf("round-trip mismatch\nwant %q\ngot  %q", plain, got)
		}
	}
}

// TestFeishuDecrypt_BadInput 坏输入应返回错误而非 panic。
func TestFeishuDecrypt_BadInput(t *testing.T) {
	key := "anykey"
	if _, err := feishuDecrypt("!!!not-base64!!!", key); err == nil {
		t.Fatal("expected error for bad base64")
	}
	if _, err := feishuDecrypt(base64.StdEncoding.EncodeToString([]byte("short")), key); err == nil {
		t.Fatal("expected error for too-short cipher")
	}
	// 错误密钥：能解出乱码，但截取 { } 后应不含期望明文。
	enc, _ := feishuEncrypt(`{"type":"url_verification"}`, key)
	if _, err := feishuDecrypt(enc, "wrong-key"); err == nil {
		// 错密钥 CBC 解密通常不报错（只产生乱码），这里允许 nil error，
		// 但结果不应等于明文。
		got, _ := feishuDecrypt(enc, "wrong-key")
		if string(got) == `{"type":"url_verification"}` {
			t.Fatal("wrong key unexpectedly decrypted to correct plaintext")
		}
	}
}

// newCallbackEngine 起一个最小 gin，只挂飞书回调路由，url_verification 分支不触达 DB。
func newCallbackEngine(t *testing.T, encryptKey string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	d := &Deps{FeishuEncryptKey: encryptKey, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	registerFeishuCallback(r, "/feishu/card-callback", d)
	return r
}

// TestHandleFeishuCallback_EncryptedProbe 加密模式的 url_verification 探测，应回 challenge。
func TestHandleFeishuCallback_EncryptedProbe(t *testing.T) {
	key := "mt-5S2NOabtrOnUcVep0Nn3ClyQyNdXFWjzl28gmBUk"
	plain := `{"type":"url_verification","challenge":"enc_challenge_42"}`
	enc, err := feishuEncrypt(plain, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"encrypt": enc})

	r := newCallbackEngine(t, key)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/feishu/card-callback", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v (body=%s)", err, w.Body.String())
	}
	if resp["challenge"] != "enc_challenge_42" {
		t.Fatalf("challenge = %q, want enc_challenge_42 (body=%s)", resp["challenge"], w.Body.String())
	}
}

// TestHandleFeishuCallback_PlainProbe 非加密模式（明文 + 签名）探测，防回归。
func TestHandleFeishuCallback_PlainProbe(t *testing.T) {
	key := "mt-5S2NOabtrOnUcVep0Nn3ClyQyNdXFWjzl28gmBUk"
	plain := `{"type":"url_verification","challenge":"plain_challenge_7"}`
	ts := time.Now().Unix()
	nonce := "plainnonce"
	sig := signatureHex(ts, nonce, key, plain)

	r := newCallbackEngine(t, key)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/feishu/card-callback", strings.NewReader(plain))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lark-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", sig)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v (body=%s)", err, w.Body.String())
	}
	if resp["challenge"] != "plain_challenge_7" {
		t.Fatalf("challenge = %q, want plain_challenge_7 (body=%s)", resp["challenge"], w.Body.String())
	}
}

// TestHandleFeishuCallback_PlainNoSig 非加密明文但缺签名头：应被拒（防伪造）。
func TestHandleFeishuCallback_PlainNoSig(t *testing.T) {
	r := newCallbackEngine(t, "anykey")
	w := httptest.NewRecorder()
	// 明文 url_verification 但不带签名头 —— 应判定签名缺失而拒绝。
	req := httptest.NewRequest(http.MethodPost, "/feishu/card-callback",
		strings.NewReader(`{"type":"url_verification","challenge":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["challenge"] != nil {
		t.Fatalf("plaintext probe without signature should NOT echo challenge, got %s", w.Body.String())
	}
}

// TestFeishuCardEvent_Schema2Parse 飞书 2.0 schema 把 action/operator 包在 event 字段下，
// 必须能正确解析出 action.value 里的 alert_id 等。用线上抓到的真实结构。
func TestFeishuCardEvent_Schema2Parse(t *testing.T) {
	raw := `{"schema":"2.0","header":{"event_id":"x","event_type":"card.action.trigger"},"event":{"operator":{"open_id":"ou_op1","union_id":"on_u1"},"action":{"value":{"action":"handled","alert_id":"ba0fa24b","channel_id":"7","event":"balance_low"},"tag":"button"}}}`
	var ev feishuCardEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	a := ev.action()
	if a.Value["alert_id"] != "ba0fa24b" {
		t.Fatalf("alert_id = %q, want ba0fa24b (value=%v)", a.Value["alert_id"], a.Value)
	}
	if a.Value["action"] != "handled" {
		t.Fatalf("action = %q, want handled", a.Value["action"])
	}
	if a.Value["channel_id"] != "7" {
		t.Fatalf("channel_id = %q, want 7", a.Value["channel_id"])
	}
	if ev.operator().OpenID != "ou_op1" {
		t.Fatalf("operator open_id = %q, want ou_op1", ev.operator().OpenID)
	}
	// 顶层 Action/Operator 应为空（2.0 下不该误取到）。
	if ev.Action.Value != nil {
		t.Fatalf("top-level action should be nil in 2.0 schema, got %v", ev.Action.Value)
	}
}

// TestFeishuCardEvent_LegacyTopLevelParse 旧版 schema（action/operator 在顶层）也要能解析。
func TestFeishuCardEvent_LegacyTopLevelParse(t *testing.T) {
	raw := `{"action":{"value":{"alert_id":"a1","action":"ignored"},"open_id":"ou_top"},"operator":{"open_id":"ou_op2"}}`
	var ev feishuCardEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	a := ev.action()
	if a.Value["alert_id"] != "a1" {
		t.Fatalf("alert_id = %q, want a1", a.Value["alert_id"])
	}
	if ev.operator().OpenID != "ou_op2" {
		t.Fatalf("operator open_id = %q, want ou_op2", ev.operator().OpenID)
	}
}

// TestBuildFeishuResolvedCard 终态卡片结构与发送卡片一致（config/header/elements），
// 不含按钮 action，elements 含终态文案。供回调响应 card 字段就地替换。
func TestBuildFeishuResolvedCard(t *testing.T) {
	cases := []struct {
		status storage.AlertStatus
		event  string
		wantBadge string
	}{
		{storage.AlertStatusHandled, "balance_low", "✅"},
		{storage.AlertStatusIgnored, "rate_limit", "🚫"},
	}
	for _, tc := range cases {
		card := buildFeishuResolvedCard(tc.event, tc.status)
		// 结构关键字段
		if card["config"] == nil || card["header"] == nil || card["elements"] == nil {
			t.Fatalf("card missing config/header/elements: %v", card)
		}
		elems, ok := card["elements"].([]any)
		if !ok || len(elems) == 0 {
			t.Fatalf("elements not non-empty slice: %T", card["elements"])
		}
		// 第一个 element 应为 div + 含 badge 文案
		div, ok := elems[0].(map[string]any)
		if !ok || div["tag"] != "div" {
			t.Fatalf("first element not div: %v", elems[0])
		}
		text, _ := div["text"].(map[string]string)
		if !strings.Contains(text["content"], tc.wantBadge) {
			t.Fatalf("card content %q missing badge %q", text["content"], tc.wantBadge)
		}
		if !strings.Contains(text["content"], tc.event) {
			t.Fatalf("card content %q missing event %q", text["content"], tc.event)
		}
	}
}

func signatureHex(ts int64, nonce, key, body string) string {
	h := sha256.New()
	h.Write([]byte(strconv.FormatInt(ts, 10) + nonce + key + body))
	return hex.EncodeToString(h.Sum(nil))
}
