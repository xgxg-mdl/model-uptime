package notification

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestRenderTemplateSeparatesStatusCardsAndEscapes(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Date(2026, 8, 15, 10, 50, 2, 0, time.FixedZone("UTC+8", 8*60*60)).Unix()
	context := NewTemplateContext(time.Date(2026, 8, 15, 2, 50, 2, 0, time.UTC), []model.StatusChange{
		{ServiceID: "a", Model: "alpha <fast>", Provider: "vendor & co", Error: "timeout <5s", Status: "down", PreviousStatus: "up", LastTS: confirmedAt, TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
		{ServiceID: "b", Model: "beta", OK: true, LatencyMS: 42, Status: "up", PreviousStatus: "down", LastTS: confirmedAt, OutageDurationSec: 474, TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
	})
	if context.ChangedAt != "2026-08-15 10:50:02" {
		t.Fatalf("ChangedAt 未使用北京时间: %q", context.ChangedAt)
	}
	downText, err := RenderTemplate(DefaultTemplate, NewTemplateContext(
		time.Date(2026, 8, 15, 2, 50, 2, 0, time.UTC), context.DownModels,
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"🔴 <b>模型异常告警 · 1</b>", "<blockquote><b>异常模型</b>", "• <b>vendor &amp; co</b> / <code>alpha &lt;fast&gt;</code>", "<b>检测时间</b>　<code>08-15 10:50</code>（北京时间）", "<blockquote expandable><b>异常详情</b>", "• <code>alpha &lt;fast&gt;</code>　原因：timeout &lt;5s"} {
		if !strings.Contains(downText, want) {
			t.Errorf("异常卡片缺少 %q:\n%s", want, downText)
		}
	}
	if strings.Contains(downText, "beta") || strings.Count(downText, "🔴") != 1 {
		t.Fatalf("异常卡片混入恢复模型或重复状态图标:\n%s", downText)
	}

	recoveryText, err := RenderTemplate(DefaultTemplate, NewTemplateContext(
		time.Date(2026, 8, 15, 2, 50, 2, 0, time.UTC), context.RecoveredModels,
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"🟢 <b>模型恢复通知 · 1</b>", "<blockquote><b>恢复模型</b>", "• <code>beta</code>　用时 <code>7.9 分钟</code> · 延迟 <code>42 ms</code>", "<b>恢复时间</b>"} {
		if !strings.Contains(recoveryText, want) {
			t.Errorf("恢复卡片缺少 %q:\n%s", want, recoveryText)
		}
	}
	if strings.Contains(recoveryText, "alpha") || strings.Count(recoveryText, "🟢") != 1 {
		t.Fatalf("恢复卡片混入异常模型或重复状态图标:\n%s", recoveryText)
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

func TestStatusPageButtonMarkupIsLocalizedAndValidated(t *testing.T) {
	t.Parallel()
	english, err := statusPageButtonMarkup("https://status.example.com/", LanguageEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(english, `"text":"Open status page"`) || !strings.Contains(english, `"url":"https://status.example.com/"`) {
		t.Fatalf("英文探针页按钮错误: %q", english)
	}
	empty, err := statusPageButtonMarkup("", DefaultLanguage)
	if err != nil || empty != "" {
		t.Fatalf("空地址不应生成按钮: markup=%q err=%v", empty, err)
	}
	if _, err := statusPageButtonMarkup("javascript:alert(1)", DefaultLanguage); !errors.Is(err, ErrInvalidStatusPageURL) {
		t.Fatalf("危险探针页地址应被拒绝: %v", err)
	}
}
