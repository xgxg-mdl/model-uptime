package notifier

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenderTemplateAggregatesAndEscapes(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Date(2026, 8, 15, 10, 50, 2, 0, time.FixedZone("UTC+8", 8*60*60)).Unix()
	context := NewTemplateContext(time.Date(2026, 8, 15, 2, 50, 2, 0, time.UTC), []Change{
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

func TestBuiltInTemplateLanguageSelection(t *testing.T) {
	t.Parallel()
	context := NewTemplateContext(time.Now(), []Change{{ServiceID: "a", Model: "alpha", Error: "timeout", Status: "down", PreviousStatus: "up"}})
	english, err := RenderTemplate(TemplateForLanguage(LanguageEnglish), context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(english, "Model status update: incident detected") || !strings.Contains(english, "Down model: alpha") {
		t.Fatalf("英文内置模板渲染错误:\n%s", english)
	}
	if got := TemplateForLanguage(""); got != DefaultTemplate {
		t.Fatal("空语言必须使用中文默认模板")
	}
}

func TestNormalizeConfigDefaultsToChineseAndMigratesLegacyTemplate(t *testing.T) {
	t.Parallel()
	config := Config{Subscriptions: []Subscription{
		{ID: "empty"},
		{ID: "legacy", Template: EnglishTemplate},
		{ID: "english", Language: LanguageEnglish},
		{ID: "custom", Template: "custom English content"},
		{ID: "old-chinese", Language: DefaultLanguage, Template: legacyChineseTemplate},
		{ID: "old-english", Language: LanguageEnglish, Template: legacyEnglishTemplate},
		{ID: "stats-chinese", Language: DefaultLanguage, Template: legacyStatisticsTemplate(DefaultLanguage)},
		{ID: "stats-english", Language: LanguageEnglish, Template: legacyStatisticsTemplate(LanguageEnglish)},
	}}
	NormalizeConfig(&config)
	for _, index := range []int{0, 1} {
		if config.Subscriptions[index].Language != DefaultLanguage || config.Subscriptions[index].Template != DefaultTemplate {
			t.Fatalf("订阅未迁移为中文默认模板: %+v", config.Subscriptions[index])
		}
	}
	if config.Subscriptions[2].Language != LanguageEnglish || config.Subscriptions[2].Template != EnglishTemplate {
		t.Fatalf("英文订阅未使用英文内置模板: %+v", config.Subscriptions[2])
	}
	if config.Subscriptions[3].Language != DefaultLanguage || config.Subscriptions[3].Template != "custom English content" {
		t.Fatalf("自定义模板不应被覆盖: %+v", config.Subscriptions[3])
	}
	if config.Subscriptions[4].Template != DefaultTemplate || config.Subscriptions[5].Template != EnglishTemplate {
		t.Fatalf("旧版内置模板应升级为新的统计模板: %+v %+v", config.Subscriptions[4], config.Subscriptions[5])
	}
	if config.Subscriptions[6].Template != DefaultTemplate || config.Subscriptions[7].Template != EnglishTemplate {
		t.Fatalf("带错误详情的默认模板应升级为脱敏模板: %+v %+v", config.Subscriptions[6], config.Subscriptions[7])
	}
}

func TestValidateConfigRejectsUnsupportedLanguage(t *testing.T) {
	t.Parallel()
	err := ValidateConfig(Config{Subscriptions: []Subscription{{ID: "ops", Language: "fr-FR"}}})
	if err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("未知语言应被拒绝: %v", err)
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
	context := NewTemplateContext(time.Now(), []Change{
		{ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up"},
		{ServiceID: "a", Model: "alpha", Status: "up", PreviousStatus: "down"},
	})
	if context.TotalChanges != 0 {
		t.Fatalf("窗口开始与结束状态相同时不应通知: %+v", context)
	}
}

func TestNotifySendsOneAggregatedMessagePerSubscription(t *testing.T) {
	t.Parallel()
	requests := make(chan requestRecord, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单: %v", err)
		}
		requests <- requestRecord{path: r.URL.Path, chatID: r.Form.Get("chat_id"), text: r.Form.Get("text"), parseMode: r.Form.Get("parse_mode")}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	n := newTestNotifier(t, server.URL, Config{
		BotToken: "token",
		Subscriptions: []Subscription{
			{ID: "ops", Enabled: true, ChatID: "-100", ServiceIDs: []string{"a", "b"}, Template: `{{.TotalChanges}}|{{range .DownModels}}D:{{.Model}};{{end}}{{range .RecoveredModels}}R:{{.Model}};{{end}}`},
			{ID: "other", Enabled: true, ChatID: "200", ServiceIDs: []string{"c"}, Template: `{{.TotalChanges}}`},
		},
	})
	if err := n.Notify(Batch{Changes: []Change{
		{ServiceID: "a", Model: "alpha-old", Status: "down", PreviousStatus: "up"},
		{ServiceID: "a", Model: "alpha-middle", OK: true, Status: "up", PreviousStatus: "down"},
		{ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up"},
		{ServiceID: "b", Model: "beta", OK: true, Status: "up", PreviousStatus: "down"},
	}}); err != nil {
		t.Fatal(err)
	}
	closeNotifier(t, n)

	select {
	case got := <-requests:
		if got.path != "/bottoken/sendMessage" || got.chatID != "-100" || got.parseMode != "HTML" {
			t.Fatalf("请求参数错误: %+v", got)
		}
		if got.text != "2|D:alpha;R:beta;" {
			t.Fatalf("聚合内容错误: %q", got.text)
		}
	default:
		t.Fatal("没有收到 Telegram 请求")
	}
	select {
	case extra := <-requests:
		t.Fatalf("收到额外请求: %+v", extra)
	default:
	}
}

func TestSendTestRetriesTransientFailureThreeTimes(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		status := http.StatusInternalServerError
		body := `{"ok":false,"description":"temporary"}`
		if attempt == 4 {
			status = http.StatusOK
			body = `{"ok":true}`
		}
		return response(status, body), nil
	})
	n, err := New(Options{Client: client, RetryDelays: []time.Duration{0, 0, 0}, Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	if err := n.SendTest(context.Background(), "ops"); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("期望初次发送加 3 次重试，实际 %d 次", got)
	}
}

func TestSendTestDoesNotRetryPermanentFailure(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return response(http.StatusBadRequest, `{"ok":false,"description":"bad chat id"}`), nil
	})
	n, err := New(Options{Client: client, RetryDelays: []time.Duration{0, 0, 0}, Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	if err := n.SendTest(context.Background(), "ops"); err == nil {
		t.Fatal("期望 Telegram 参数错误")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("永久错误不应重试，实际发送 %d 次", got)
	}
}

func TestSendErrorRedactsBotToken(t *testing.T) {
	t.Parallel()
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("request failed for " + request.URL.String())
	})
	n, err := New(Options{Client: client, RetryDelays: []time.Duration{}, Logger: discardLogger()}, validConfig("super-secret", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	err = n.SendTest(context.Background(), "ops")
	if err == nil || strings.Contains(err.Error(), "super-secret") || !strings.Contains(err.Error(), "****") {
		t.Fatalf("Bot Token 应从错误中脱敏: %v", err)
	}
}

func TestUpdateConfigIsUsedByFollowingNotifications(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	n := newTestNotifier(t, server.URL, validConfig("old-token", "old-chat"))
	if err := n.UpdateConfig(validConfig("new-token", "new-chat")); err != nil {
		t.Fatal(err)
	}
	if err := n.SendTest(context.Background(), "ops"); err != nil {
		t.Fatal(err)
	}
	closeNotifier(t, n)
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/botnew-token/sendMessage" {
		t.Fatalf("热更新未生效: %v", paths)
	}
}

func TestUpdateConfigKeepsPreviousSnapshotOnInvalidTemplate(t *testing.T) {
	t.Parallel()
	n := newTestNotifier(t, "http://unused", validConfig("token", "chat"))
	defer closeNotifier(t, n)
	err := n.UpdateConfig(Config{BotToken: "new", Subscriptions: []Subscription{{ID: "ops", Enabled: true, ChatID: "chat", Template: "{{"}}})
	if err == nil {
		t.Fatal("期望模板校验失败")
	}
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	if n.config.botToken != "token" {
		t.Fatalf("无效热更新覆盖了旧配置: %q", n.config.botToken)
	}
}

func TestQueueIsBounded(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	n, err := New(Options{Client: client, QueueSize: 1, RetryDelays: []time.Duration{}, Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Changes: []Change{{ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up"}}}
	if err := n.Notify(batch); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := n.Notify(batch); err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(batch); !strings.Contains(err.Error(), ErrQueueFull.Error()) {
		t.Fatalf("期望队列满错误，得到 %v", err)
	}
	close(release)
	closeNotifier(t, n)
}

type requestRecord struct {
	path      string
	chatID    string
	text      string
	parseMode string
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func validConfig(token, chatID string) Config {
	return Config{BotToken: token, Subscriptions: []Subscription{{ID: "ops", Enabled: true, ChatID: chatID, ServiceIDs: []string{"a"}, Template: `{{.TotalChanges}}`}}}
}

func newTestNotifier(t *testing.T, apiBaseURL string, config Config) *Notifier {
	t.Helper()
	n, err := New(Options{APIBaseURL: apiBaseURL, RetryDelays: []time.Duration{}, Logger: discardLogger()}, config)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func closeNotifier(t *testing.T, notifier *Notifier) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := notifier.Close(ctx); err != nil {
		t.Errorf("关闭通知器: %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
