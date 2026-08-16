package notification

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestRenderTemplateAggregatesAndEscapes(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Date(2026, 8, 15, 10, 50, 2, 0, time.FixedZone("UTC+8", 8*60*60)).Unix()
	context := NewTemplateContext(time.Date(2026, 8, 15, 2, 50, 2, 0, time.UTC), []model.StatusChange{
		{ServiceID: "a", Model: "alpha <fast>", Provider: "vendor & co", Error: "timeout <5s", Status: "down", PreviousStatus: "up", LastTS: confirmedAt, TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
		{ServiceID: "b", Model: "beta", OK: true, LatencyMS: 42, Status: "up", PreviousStatus: "down", LastTS: confirmedAt, OutageDurationSec: 474, TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
	})
	if context.ChangedAt != "2026-08-15 10:50:02" {
		t.Fatalf("ChangedAt 未使用北京时间: %q", context.ChangedAt)
	}
	text, err := RenderTemplate(DefaultTemplate, context)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"⚠️ 模型状态变更：多项状态变化", "异常模型：alpha &lt;fast&gt;", "vendor &amp; co", "异常持续时间：7.9 分钟", "监控模型：<b>beta</b>", "确认时间：2026-08-15 10:50:02", "今日运行时间：9 小时 39 分钟", "今日异常时间：1 小时 10 分钟", "今日异常次数：4 次", "今日可用率：89.20%"} {
		if !strings.Contains(text, want) {
			t.Errorf("渲染结果缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "timeout") || strings.Contains(text, "异常原因") {
		t.Fatalf("默认模板不应返回探测错误详情:\n%s", text)
	}
	custom, err := RenderTemplate(`{{range .Changes}}{{.Error}}{{end}}`, context)
	if err != nil || custom != "timeout &lt;5s" {
		t.Fatalf("自定义模板仍应能安全使用 Error 变量: text=%q err=%v", custom, err)
	}
}

func TestRenderTemplateRejectsLongMessage(t *testing.T) {
	t.Parallel()
	_, err := RenderTemplate(strings.Repeat("界", TelegramMessageLimit+1), TemplateContext{})
	if err != ErrMessageTooLong {
		t.Fatalf("期望 ErrMessageTooLong，得到 %v", err)
	}
}

func TestNewTemplateContextDropsNetUnchangedModel(t *testing.T) {
	t.Parallel()
	context := NewTemplateContext(time.Now(), []model.StatusChange{
		{ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up"},
		{ServiceID: "a", Model: "alpha", Status: "up", PreviousStatus: "down"},
	})
	if context.TotalChanges != 0 {
		t.Fatalf("窗口开始与结束状态相同时不应通知: %+v", context)
	}
}

func TestAppendStatusPageLinkIsLocalizedAndChecksLength(t *testing.T) {
	t.Parallel()
	english, err := appendStatusPageLink("message", "https://status.example.com/", LanguageEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if english != "message\n\n<a href=\"https://status.example.com/\">Open status page</a>" {
		t.Fatalf("英文探针页链接错误: %q", english)
	}
	plain, err := appendStatusPageLink("message", "", DefaultLanguage)
	if err != nil || plain != "message" {
		t.Fatalf("空地址不应修改消息: text=%q err=%v", plain, err)
	}
	if _, err := appendStatusPageLink(strings.Repeat("界", TelegramMessageLimit), "https://status.example.com/", DefaultLanguage); !errors.Is(err, ErrMessageTooLong) {
		t.Fatalf("追加链接后超长应失败: %v", err)
	}
	if _, err := appendStatusPageLink("message", "javascript:alert(1)", DefaultLanguage); !errors.Is(err, ErrInvalidStatusPageURL) {
		t.Fatalf("危险探针页地址应被拒绝: %v", err)
	}
}
