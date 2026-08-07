package notify

import (
	"strings"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

func TestRenderBalanceLow(t *testing.T) {
	data := RenderData{
		ChannelName: "渠道A",
		Balance:     12.3456,
		Threshold:   20,
	}
	subject, body := Render(storage.EventBalanceLow, DefaultTemplates()[storage.EventBalanceLow], data, nil)
	if subject != "[upstream-hub] 渠道A 余额低于阈值" {
		t.Fatalf("subject = %q", subject)
	}
	if body != "当前余额: 12.3456，阈值: 20.0000" {
		t.Fatalf("body = %q", body)
	}
}

func TestRenderRateChangedSingle(t *testing.T) {
	data := RenderData{
		ChannelName: "渠道A",
		Count:       1,
		GroupName:   "default",
		OldRatio:    1,
		NewRatio:    0.8,
		Arrow:       "下调",
		Changes: []RenderChangeItem{{
			GroupName: "default", OldRatio: 1, NewRatio: 0.8, Arrow: "下调",
		}},
	}
	subject, body := Render(storage.EventRateChanged, DefaultTemplates()[storage.EventRateChanged], data, nil)
	wantSubject := "【倍率变化提醒】渠道A · default"
	if subject != wantSubject {
		t.Fatalf("subject = %q, want %q", subject, wantSubject)
	}
	if !strings.Contains(body, "分组倍率：default 由 1 下调至 0.8") {
		t.Fatalf("body missing rate line: %q", body)
	}
}

func TestRenderRateChangedMerged(t *testing.T) {
	data := RenderData{
		ChannelName: "渠道A",
		Count:       2,
		Changes: []RenderChangeItem{
			{GroupName: "g1", OldRatio: 1, NewRatio: 1.2, Arrow: "上涨"},
			{GroupName: "g2", OldRatio: 2, NewRatio: 1.5, Arrow: "下调"},
		},
	}
	subject, body := Render(storage.EventRateChanged, DefaultTemplates()[storage.EventRateChanged], data, nil)
	wantSubject := "【倍率变化提醒】渠道A · 2 个分组变动"
	if subject != wantSubject {
		t.Fatalf("subject = %q, want %q", subject, wantSubject)
	}
	if !strings.Contains(body, "g1：1 上涨至 1.2") || !strings.Contains(body, "g2：2 下调至 1.5") {
		t.Fatalf("body missing change items: %q", body)
	}
	if !strings.Contains(body, "共 2 个分组倍率变化") {
		t.Fatalf("body missing count line: %q", body)
	}
}

func TestRenderFallsBackOnBadTemplate(t *testing.T) {
	data := RenderData{ChannelName: "渠道A", Balance: 1, Threshold: 2}
	// 故意写错语法：{{ .ChannelName 未闭合
	subject, body := Render(storage.EventBalanceLow, "{{ .ChannelName", data, nil)
	if subject != "[upstream-hub] 渠道A 余额低于阈值" {
		t.Fatalf("did not fall back to default subject, got %q", subject)
	}
	if body != "当前余额: 1.0000，阈值: 2.0000" {
		t.Fatalf("did not fall back to default body, got %q", body)
	}
}

func TestRenderEmptyTemplateFallsBack(t *testing.T) {
	subject, _ := Render(storage.EventMonitorFailed, "", RenderData{ChannelName: "C", Error: "boom"}, nil)
	if subject != "[upstream-hub] C 监控异常" {
		t.Fatalf("subject = %q", subject)
	}
}

func TestTemplatesMerge(t *testing.T) {
	user := map[storage.NotificationEvent]string{
		storage.EventBalanceLow: "[override] {{.ChannelName}}",
	}
	merged := Templates(user)
	if !strings.HasPrefix(merged[storage.EventBalanceLow], "[override]") {
		t.Fatalf("user override not applied: %q", merged[storage.EventBalanceLow])
	}
	// 未覆盖的事件仍是默认
	if merged[storage.EventRateChanged] != DefaultTemplates()[storage.EventRateChanged] {
		t.Fatal("rate_changed default not preserved")
	}
}
