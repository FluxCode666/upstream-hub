package notify

import (
	"strings"
	"testing"

	"github.com/worryzyy/upstream-hub/internal/storage"
)

func TestAppendRechargeURL(t *testing.T) {
	if got := AppendRechargeURL("告警正文", ""); got != "告警正文" {
		t.Fatalf("empty recharge URL changed body: %q", got)
	}
	want := "告警正文\n充值链接：https://example.com/topup"
	if got := AppendRechargeURL("告警正文\n", " https://example.com/topup "); got != want {
		t.Fatalf("AppendRechargeURL() = %q, want %q", got, want)
	}
}

func TestBuildBatchMessageIncludesRechargeURL(t *testing.T) {
	channel := &storage.Channel{
		ID:          1,
		Name:        "渠道 A",
		RechargeURL: "https://example.com/topup",
	}
	msg := BuildBatchMessage(channel, []RateChange{{
		GroupName: "default",
		OldRatio:  1,
		NewRatio:  0.8,
	}})
	if !strings.Contains(msg.Body, "充值链接：https://example.com/topup") {
		t.Fatalf("rate notification body missing recharge URL: %q", msg.Body)
	}
}
