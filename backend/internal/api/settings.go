package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

func registerSettings(g *gin.RouterGroup, d *Deps) {
	g.GET("/settings/notify-templates", func(c *gin.Context) {
		userTmpls, err := d.Settings.GetNotifyTemplates()
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		// 合并默认模板：用户填了的覆盖默认，未填的用默认，前端直接展示当前生效文案。
		current := notify.Templates(userTmpls)
		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"current":  current,
				"defaults": notify.DefaultTemplates(),
			},
		})
	})
	g.PUT("/settings/notify-templates", func(c *gin.Context) { updateNotifyTemplates(c, d) })
}

type notifyTemplatesInput map[storage.NotificationEvent]string

func updateNotifyTemplates(c *gin.Context, d *Deps) {
	var in notifyTemplatesInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	// 仅接受已知事件类型，过滤非法 key；空值视为"用默认"，直接不存该 key。
	cleaned := make(map[storage.NotificationEvent]string, len(in))
	known := map[storage.NotificationEvent]bool{
		storage.EventBalanceLow:    true,
		storage.EventRateChanged:   true,
		storage.EventLoginFailed:   true,
		storage.EventCaptchaFailed: true,
		storage.EventMonitorFailed: true,
	}
	for k, v := range in {
		if !known[k] {
			continue
		}
		cleaned[k] = v
	}
	if err := d.Settings.SetNotifyTemplates(cleaned); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "note": "修改需重启后端生效"})
}
