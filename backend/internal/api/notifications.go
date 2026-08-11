package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

func registerNotifications(g *gin.RouterGroup, d *Deps) {
	gpc := g.Group("/notifications/channels")
	gpc.GET("", func(c *gin.Context) {
		list, err := d.Notifies.ListChannels()
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": notifyChannelResponses(d, list)})
	})
	gpc.POST("", func(c *gin.Context) { createNotifyChannel(c, d) })
	gpc.PUT("/:id", func(c *gin.Context) { updateNotifyChannel(c, d) })
	gpc.DELETE("/:id", func(c *gin.Context) {
		id, err := uintParam(c, "id")
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		if err := d.Notifies.DeleteChannel(id); err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	gpc.POST("/:id/test", func(c *gin.Context) { testNotify(c, d) })

	g.GET("/notifications/logs", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
		list, err := d.Notifies.ListLogs(limit)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": list})
	})
}

type notifyChannelInput struct {
	Name          string                          `json:"name" binding:"required"`
	Type          storage.NotificationChannelType `json:"type" binding:"required"`
	Config        string                          `json:"config"` // JSON string；编辑时可留空保留原值
	Subscriptions string                          `json:"subscriptions"`
	Enabled       bool                            `json:"enabled"`
}

// normalizeSubscriptions 把输入的订阅 JSON 字符串规整为 "[]" 或合法 JSON 数组。
// 解析失败返回错误以便 API 返回 400。
func normalizeSubscriptions(raw string) (string, error) {
	if raw == "" || raw == "null" {
		return "[]", nil
	}
	var list []notify.Subscription
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return "", err
	}
	out, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func createNotifyChannel(c *gin.Context, d *Deps) {
	var in notifyChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if in.Config == "" {
		fail(c, http.StatusBadRequest, errors.New("config is required"))
		return
	}
	subs, err := normalizeSubscriptions(in.Subscriptions)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	cipherCfg, err := d.Cipher.Encrypt(in.Config)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	ch := &storage.NotificationChannel{
		Name:          in.Name,
		Type:          in.Type,
		ConfigCipher:  cipherCfg,
		Subscriptions: subs,
		Enabled:       in.Enabled,
	}
	if err := d.Notifies.CreateChannel(ch); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": notifyChannelResponse(d, ch)})
}

func updateNotifyChannel(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Notifies.FindChannel(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	var in notifyChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	subs, err := normalizeSubscriptions(in.Subscriptions)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch.Name = in.Name
	ch.Type = in.Type
	ch.Enabled = in.Enabled
	ch.Subscriptions = subs
	if in.Config != "" {
		// 合并哨兵字段：incoming 里值为 __REDACTED__ 的密钥保留数据库已有真实值，
		// 避免“只改名 / 只改一个字段”把其它密钥冲空。
		existingPlain, _ := d.Cipher.Decrypt(ch.ConfigCipher)
		merged, err := notify.MergeConfig(in.Config, existingPlain)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		cipherCfg, err := d.Cipher.Encrypt(merged)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		ch.ConfigCipher = cipherCfg
	}
	if err := d.Notifies.UpdateChannel(ch); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": notifyChannelResponse(d, ch)})
}

func testNotify(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Notifies.FindChannel(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	msg := notify.Message{
		Subject: "[upstream-hub] 测试通知",
		Body:    "这是一条来自 upstream-hub 的测试消息。",
	}
	if err := d.Dispatcher.Send(c.Request.Context(), ch, msg); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// notifyChannelDTO 是通知渠道的 API 响应 DTO：在 GORM 模型字段之外附带
// config_preview，供前端编辑表单回显当前配置结构（mode / 非敏感字段）+ 密钥脱敏占位。
// ConfigCipher 本身是 json:"-"，永远不会外泄。
type notifyChannelDTO struct {
	*storage.NotificationChannel
	ConfigPreview map[string]any `json:"config_preview,omitempty"`
}

// buildNotifyChannelDTO 解密 + 脱敏单个渠道，返回 DTO（不包 data 外壳）。
// 解密失败时 ConfigPreview 留空，前端回退到空表单（留空 = 保留原配置）。
func buildNotifyChannelDTO(d *Deps, ch *storage.NotificationChannel) notifyChannelDTO {
	plain, err := d.Cipher.Decrypt(ch.ConfigCipher)
	preview := notify.RedactConfig(string(ch.Type), plain)
	if err != nil {
		preview = nil
	}
	return notifyChannelDTO{NotificationChannel: ch, ConfigPreview: preview}
}

// notifyChannelResponse 单个渠道响应：{"data": dto}。给创建/更新接口用。
func notifyChannelResponse(d *Deps, ch *storage.NotificationChannel) gin.H {
	return gin.H{"data": buildNotifyChannelDTO(d, ch)}
}

// notifyChannelResponses 批量版，给列表接口用。
// 注意：列表元素必须是渠道对象本身（含 config_preview），不能再套一层 data 外壳，
// 否则前端 useApi<NotificationChannel[]> 拿到的每个元素没有 id/name/type。
func notifyChannelResponses(d *Deps, list []storage.NotificationChannel) []notifyChannelDTO {
	out := make([]notifyChannelDTO, 0, len(list))
	for i := range list {
		out = append(out, buildNotifyChannelDTO(d, &list[i]))
	}
	return out
}
