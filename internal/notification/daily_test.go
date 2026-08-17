package notification

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

type dailyRepositoryStub struct {
	mu         sync.Mutex
	histories  map[string][]model.ProbeResult
	deliveries []Delivery
	runs       map[string]bool
}

func TestDailyReporterDoesNotSendOnStartup(t *testing.T) {
	repository := &dailyRepositoryStub{runs: map[string]bool{}, histories: map[string][]model.ProbeResult{}}
	reporter, err := NewDailyReporter(repository, func() DailySnapshot {
		return DailySnapshot{
			Telegram: Config{BotToken: "token", Subscriptions: []Subscription{{
				ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"alpha"},
			}}},
			Services: []model.Service{{ID: "alpha", Model: "alpha"}},
		}
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	reporter.Start()
	time.Sleep(20 * time.Millisecond)
	if err := reporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.deliveries) != 0 {
		t.Fatalf("日报器启动时发送了 %d 条日报，必须只在北京时间零点发送", len(repository.deliveries))
	}
}

func (s *dailyRepositoryStub) LoadResultsSinceWithPrevious(_ context.Context, id string, _, _ int64) ([]model.ProbeResult, error) {
	return append([]model.ProbeResult(nil), s.histories[id]...), nil
}

func (s *dailyRepositoryStub) EnqueueDailyReports(_ context.Context, date string, deliveries []Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := date + ":" + deliveries[0].SubscriptionID
	if s.runs[key] {
		return nil
	}
	s.runs[key] = true
	s.deliveries = append(s.deliveries, deliveries...)
	return nil
}

func TestDailyReporterSummarizesSelectedModelsAndDoesNotDuplicate(t *testing.T) {
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, beijingLocation)
	repository := &dailyRepositoryStub{
		runs: map[string]bool{},
		histories: map[string][]model.ProbeResult{
			"active-incident": {
				{TS: start.Add(-time.Minute).Unix(), OK: true},
				{TS: start.Add(4 * time.Hour).Unix(), OK: false},
			},
			"incident": {
				{TS: start.Add(-time.Minute).Unix(), OK: true},
				{TS: start.Add(2 * time.Hour).Unix(), OK: false},
				{TS: start.Add(3 * time.Hour).Unix(), OK: true},
			},
			"healthy": {{TS: start.Add(-time.Minute).Unix(), OK: true}},
		},
	}
	reporter, err := NewDailyReporter(repository, func() DailySnapshot {
		return DailySnapshot{
			Telegram: Config{BotToken: "token", Subscriptions: []Subscription{{
				ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"active-incident", "incident", "healthy", "unobserved"},
			}}},
			Services: []model.Service{
				{ID: "active-incident", Model: "delta", Provider: "D"},
				{ID: "incident", Model: "alpha", Provider: "A"},
				{ID: "healthy", Model: "beta", Provider: "B"},
				{ID: "unobserved", Model: "gamma", Provider: "C"},
			},
			StatusPageURL: "https://status.example.com/",
		}
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(repository.deliveries) != 1 {
		t.Fatalf("日报投递数 = %d，期望 1", len(repository.deliveries))
	}
	text := repository.deliveries[0].Text
	for _, want := range []string{"📊 <b>模型运行日报</b>\n\n<blockquote>", "<b>日期</b>　<code>2026-08-16</code>（北京时间）", "<b>范围</b>　4 个模型 · 🟢 1 正常 · 🟡 1 已恢复 · 🔴 1 异常 · ⚪ 1 无数据", "<b>可用率</b>　<code>", "<b>故障</b>　2 次", "<b>模型状态</b>\n<blockquote>", "🟡 <b>A</b> / <code>alpha</code>", "🔴 <b>D</b> / <code>delta</code>", "查看探针页"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日报缺少 %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"🟢 <b>B</b> / <code>beta</code>", "⚪ <b>C</b> / <code>gamma</code>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日报必须列出所有订阅模型，缺少 %q:\n%s", want, text)
		}
	}
}

func TestBuildDailyDeliveriesSplitsOnlyBetweenModels(t *testing.T) {
	report := dailyReport{Date: time.Date(2026, 8, 16, 0, 0, 0, 0, beijingLocation), Total: 2, Unavailable: 2}
	for i := 0; i < 2; i++ {
		report.Models = append(report.Models, dailyModel{
			ServiceID: string(rune('a' + i)), Model: strings.Repeat("model", 700), Provider: "provider",
			Stats: model.DailyStats{DownSec: 60, DownCount: 1},
		})
	}
	subscription := compiledSubscription{Subscription: Subscription{ID: "ops", Language: DefaultLanguage}}
	deliveries, err := buildDailyDeliveries(subscription, report.Date, report, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("分片数 = %d，期望 2", len(deliveries))
	}
	for _, delivery := range deliveries {
		if len([]rune(delivery.Text)) > TelegramMessageLimit || !strings.Contains(delivery.Text, "<b>模型运行日报") {
			t.Fatalf("日报分片无效: %d\n%s", len([]rune(delivery.Text)), delivery.Text)
		}
	}
}
