package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// 两类请求：
//  1. URL 验证：飞书配置回调地址时探测，回 challenge。
//  2. 卡片按钮点击：验签后按 action.value 更新 AlertState 状态、联动冷却、就地更新卡片。
//
// 验签：sha256(timestamp + nonce + encryptKey + body) 对比 X-Lark-Signature。
// 全程错误对外返回 200（避免飞书疯狂重试），错误细节记日志。
// 详见计划 2.4。
func handleFeishuCallback(c *gin.Context, d *Deps) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return
	}
	// 复原 body 供后续 c.ShouldBindJSON 失败时的兜底使用
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	if !verifyFeishuSignature(c, body, d.FeishuEncryptKey) {
		if d.Log != nil {
			d.Log.Warn("feishu callback signature mismatch")
		}
		c.JSON(http.StatusOK, gin.H{"code": 1, "msg": "signature mismatch"})
		return
	}

	// URL 验证探测。
	var probe struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Type == "url_verification" {
		c.JSON(http.StatusOK, gin.H{"challenge": probe.Challenge})
		return
	}

	// 卡片点击。
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

type feishuCardEvent struct {
	Action struct {
		Value  map[string]string `json:"value"`
		OpenID string            `json:"open_id"`
	} `json:"action"`
	Operator struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
}

// processFeishuCardAction 处理一次按钮点击，返回飞书 toast 文案。
func processFeishuCardAction(c *gin.Context, ev *feishuCardEvent, d *Deps) string {
	v := ev.Action.Value
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
	openID := ev.Action.OpenID
	if openID == "" {
		openID = ev.Operator.OpenID
	}
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
		updateFeishuCardAsync(c, d, updated)
	}
	return statusToast(status)
}

// updateFeishuCardAsync 用通知渠道配置构造飞书 app client 更新卡片。
// here 用同步：回调响应里返回新卡片内容也可，但 PATCH 更新整张卡片更直观。
func updateFeishuCardAsync(c *gin.Context, d *Deps, st *storage.AlertState) {
	ch, err := d.Notifies.FindChannel(st.NotifyChannelID)
	if err != nil || ch == nil {
		// NotifyChannelID 未落库时（旧路径）兜底：尝试所有已启用飞书渠道
		ch = findFeishuAppChannel(d)
		if ch == nil {
			return
		}
	}
	cfgJSON, err := d.Cipher.Decrypt(ch.ConfigCipher)
	if err != nil {
		return
	}
	app, err := notify.NewFeishuAppFromConfig(cfgJSON)
	if err != nil {
		return
	}
	statusText := "✅ 已由 " + st.HandledBy + " 标记为" + statusLabel(st.Status)
	if err := app.UpdateCard(c.Request.Context(), st.FeishuMessageID, statusText); err != nil && d.Log != nil {
		d.Log.Warn("feishu update card", "message_id", st.FeishuMessageID, "err", err)
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
