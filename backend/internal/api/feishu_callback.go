package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

const feishuReplayWindow = 5 * time.Minute

func registerFeishuCallback(r gin.IRouter, path string, d *Deps) {
	r.POST(path, func(c *gin.Context) { handleFeishuCallback(c, d) })
}

// handleFeishuCallback 处理飞书交互卡片回调。
//
// 飞书后台「事件与回调」开启加密策略后，请求体被 AES 加密成
// {"encrypt":"<base64>"}，且不带签名头；关闭加密策略时是明文 JSON + 签名头。
// 本端点两种都兼容：
//
//  1. 解密：body 若含 encrypt 字段，用 Encrypt Key 按 AES-256-CBC 解密（与飞书
//     官方 SDK event.EventDecrypt 算法一致）得到明文，解密本身即鉴权。
//  2. 验签：非加密请求必须靠 X-Lark-Signature 验签；加密请求跳过（飞书加密模式不带签名头）。
//  3. URL 验证：探测请求回 challenge。
//  4. 卡片点击：按 action.value 更新 AlertState 状态、联动冷却、就地更新卡片。
//
// 全程错误对外返回 200（避免飞书疯狂重试），错误细节记日志。详见计划 2.4。
func handleFeishuCallback(c *gin.Context, d *Deps) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return
	}
	// 复原 body 供后续 c.ShouldBindJSON 失败时的兜底使用
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	// (1) 加密模式：body 形如 {"encrypt":"..."} → 解密成明文后续统一处理。
	// 加密模式飞书不带签名头（X-Lark-Signature），解密本身即鉴权；
	// 非加密模式是明文 body，必须靠签名头验签。
	encrypted := false
	var enc struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &enc) == nil && enc.Encrypt != "" {
		plain, derr := feishuDecrypt(enc.Encrypt, d.FeishuEncryptKey)
		if derr != nil {
			if d.Log != nil {
				d.Log.Warn("feishu callback decrypt", "err", derr)
			}
			c.JSON(http.StatusOK, gin.H{"code": 1, "msg": "decrypt fail"})
			return
		}
		body = plain
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		encrypted = true
	}

	// (2) 验签：非加密请求必须通过签名校验；加密请求解密本身即鉴权，跳过。
	if !encrypted && !verifyFeishuSignature(c, body, d.FeishuEncryptKey) {
		if d.Log != nil {
			d.Log.Warn("feishu callback signature mismatch")
		}
		c.JSON(http.StatusOK, gin.H{"code": 1, "msg": "signature mismatch"})
		return
	}

	// (3) URL 验证探测（解密后或明文都走这里）。
	var probe struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Type == "url_verification" {
		c.JSON(http.StatusOK, gin.H{"challenge": probe.Challenge})
		return
	}

	// (4) 卡片点击。
	var ev feishuCardEvent
	if err := c.ShouldBindJSON(&ev); err != nil {
		if d.Log != nil {
			d.Log.Warn("feishu callback parse", "err", err)
		}
		c.JSON(http.StatusOK, gin.H{"code": 0})
		return
	}
	toast := processFeishuCardAction(c, &ev, d)
	c.JSON(http.StatusOK, gin.H{
		"toast": gin.H{"type": "success", "content": toast},
	})
}

// feishuCardEvent 飞书卡片点击回调事件。
//
// 飞书 2.0 schema（card.action.trigger）把 action/operator 包在 event 字段下：
//
//	{"schema":"2.0","header":{...},"event":{"operator":{...},"action":{"value":{...},"tag":"button"}}}
//
// 旧版 schema 则把 action/operator 放在顶层。下面用内嵌结构同时绑定两层：
// ShouldBindJSON 后 ev.Action/ev.Operator 优先取自 event，为空时回落顶层。
type feishuCardEvent struct {
	Event struct {
		Action   feishuAction `json:"action"`
		Operator feishuOperator `json:"operator"`
	} `json:"event"`
	// 旧版 schema 顶层字段（2.0 下为空）。
	Action   feishuAction   `json:"action"`
	Operator feishuOperator `json:"operator"`
}

type feishuAction struct {
	Value  map[string]string `json:"value"`
	OpenID string             `json:"open_id"`
	Tag    string             `json:"tag"`
}

type feishuOperator struct {
	OpenID  string `json:"open_id"`
	UnionID string `json:"union_id"`
}

// action 返回事件携带的按钮动作（优先 event 层，回落顶层）。
func (ev *feishuCardEvent) action() feishuAction {
	if ev.Event.Action.Value != nil || ev.Event.Action.Tag != "" {
		return ev.Event.Action
	}
	return ev.Action
}

// operator 返回操作人（优先 event 层，回落顶层）。
func (ev *feishuCardEvent) operator() feishuOperator {
	if ev.Event.Operator.OpenID != "" {
		return ev.Event.Operator
	}
	return ev.Operator
}

// processFeishuCardAction 处理一次按钮点击，返回飞书 toast 文案。
func processFeishuCardAction(c *gin.Context, ev *feishuCardEvent, d *Deps) string {
	a := ev.action()
	v := a.Value
	alertID := v["alert_id"]
	action := v["action"]
	channelID, _ := strconv.ParseUint(v["channel_id"], 10, 64)
	event := storage.NotificationEvent(v["event"])

	if alertID == "" {
		return "无效的告警"
	}

	st, err := d.AlertStates.FindByAlertID(alertID)
	if err != nil || st == nil {
		if d.Log != nil {
			d.Log.Warn("feishu alert not found", "alert_id", alertID, "err", err)
		}
		return "告警记录不存在"
	}

	// 已终态：幂等返回当前状态，不重复处理 / 更新卡片。
	if st.Status == storage.AlertStatusHandled || st.Status == storage.AlertStatusIgnored {
		return statusToast(st.Status)
	}

	status := storage.AlertStatusHandled
	if action == "ignored" {
		status = storage.AlertStatusIgnored
	}
	openID := ev.operator().OpenID
	updated, err := d.AlertStates.UpdateStatus(alertID, status, openID)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("feishu update status", "alert_id", alertID, "err", err)
		}
		return "处理失败"
	}

	// 已处理 → 清除该上游该事件冷却，让下次扫描能重新评估（静默窗由 dispatcher.suppressByAlertState 兜底）。
	if status == storage.AlertStatusHandled && channelID != 0 && event != "" {
		if err := d.Notifies.ResetCooldown(uint(channelID), event); err != nil && d.Log != nil {
			d.Log.Warn("feishu reset cooldown", "channel_id", channelID, "event", event, "err", err)
		}
	}

	// 就地更新卡片（best-effort，失败不影响状态已落库）。
	if updated.FeishuMessageID != "" {
		if d.Log != nil {
			d.Log.Info("feishu try update card", "alert_id", alertID,
				"message_id", updated.FeishuMessageID, "status", status)
		}
		updateFeishuCardAsync(c, d, updated)
	} else if d.Log != nil {
		d.Log.Info("feishu skip update card (no message_id)", "alert_id", alertID)
	}
	return statusToast(status)
}

// updateFeishuCardAsync 用通知渠道配置构造飞书 app client 更新卡片。
// here 用同步：回调响应里返回新卡片内容也可，但 PATCH 更新整张卡片更直观。
// 各分支均记日志，便于线上定位"卡片不更新"卡在哪一步。
func updateFeishuCardAsync(c *gin.Context, d *Deps, st *storage.AlertState) {
	ch, err := d.Notifies.FindChannel(st.NotifyChannelID)
	if err != nil || ch == nil {
		// NotifyChannelID 未落库时（旧路径）兜底：尝试所有已启用飞书渠道
		if d.Log != nil {
			d.Log.Info("feishu update card: channel not found, fallback",
				"notify_channel_id", st.NotifyChannelID, "err", err)
		}
		ch = findFeishuAppChannel(d)
		if ch == nil {
			if d.Log != nil {
				d.Log.Warn("feishu update card: no enabled feishu channel")
			}
			return
		}
	}
	cfgJSON, err := d.Cipher.Decrypt(ch.ConfigCipher)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("feishu update card: decrypt config", "channel", ch.ID, "err", err)
		}
		return
	}
	app, err := notify.NewFeishuAppFromConfig(cfgJSON)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("feishu update card: new app", "channel", ch.ID, "err", err)
		}
		return
	}
	statusText := "✅ 已由 " + st.HandledBy + " 标记为" + statusLabel(st.Status)
	if err := app.UpdateCard(c.Request.Context(), st.FeishuMessageID, statusText); err != nil {
		if d.Log != nil {
			d.Log.Warn("feishu update card", "message_id", st.FeishuMessageID, "err", err)
		}
	} else if d.Log != nil {
		d.Log.Info("feishu update card ok", "message_id", st.FeishuMessageID, "channel", ch.ID)
	}
}

func findFeishuAppChannel(d *Deps) *storage.NotificationChannel {
	list, err := d.Notifies.ListEnabledChannels()
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Type == storage.NotifyFeishu {
			return &list[i]
		}
	}
	return nil
}

// feishuDecrypt 解密飞书加密模式的请求体。
//
// 算法与飞书官方 SDK（github.com/larksuite/oapi-sdk-go v3 event.EventDecrypt）
// 完全一致：
//  1. base64 解码 encrypt 字段。
//  2. 前 aes.BlockSize(16) 字节作为 IV，其余为密文。
//  3. sha256(encryptKey) 派生 32 字节 AES-256 密钥。
//  4. AES-CBC 解密，截取首个 '{' 到末个 '}' 之间的明文 JSON。
func feishuDecrypt(encryptB64, key string) ([]byte, error) {
	buf, err := base64.StdEncoding.DecodeString(encryptB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(buf) < aes.BlockSize {
		return nil, fmt.Errorf("cipher too short: %d", len(buf))
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:sha256.Size])
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}
	iv := buf[:aes.BlockSize]
	ct := buf[aes.BlockSize:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not block-aligned: %d", len(ct))
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(ct))
	mode.CryptBlocks(plain, ct)
	// 飞书密文含随机 IV 前缀 + PKCS7 填充，明文 JSON 夹在中间，按 SDK 做法
	// 截取首个 '{' 到末个 '}'。
	n := strings.Index(string(plain), "{")
	if n == -1 {
		n = 0
	}
	m := strings.LastIndex(string(plain), "}")
	if m == -1 {
		m = len(plain) - 1
	}
	return plain[n : m+1], nil
}

// hasSignatureHeader 判断请求是否携带飞书签名头（加密模式不带，非加密模式带）。
// 保留供调试/未来按需校验，当前验签由 encrypted 标记决定。
func hasSignatureHeader(c *gin.Context) bool {
	return c.GetHeader("X-Lark-Signature") != ""
}

func verifyFeishuSignature(c *gin.Context, body []byte, key string) bool {
	if key == "" {
		return false
	}
	ts := c.GetHeader("X-Lark-Request-Timestamp")
	nonce := c.GetHeader("X-Lark-Request-Nonce")
	sig := c.GetHeader("X-Lark-Signature")
	if ts == "" || nonce == "" || sig == "" {
		return false
	}
	// 防重放：时间戳偏移 >5min 拒绝。
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if abs(time.Now().Unix()-tsInt) > int64(feishuReplayWindow.Seconds()) {
		return false
	}
	h := sha256.New()
	h.Write([]byte(ts + nonce + key + string(body)))
	want := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(want, sig)
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func statusToast(s storage.AlertStatus) string {
	switch s {
	case storage.AlertStatusHandled:
		return "已标记为已处理"
	case storage.AlertStatusIgnored:
		return "已标记为不处理"
	default:
		return string(s)
	}
}

func statusLabel(s storage.AlertStatus) string {
	switch s {
	case storage.AlertStatusHandled:
		return "已处理"
	case storage.AlertStatusIgnored:
		return "不处理"
	default:
		return string(s)
	}
}
