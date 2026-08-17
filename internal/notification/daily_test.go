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
				ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"incident", "healthy", "unobserved"},
			}}},
			Services: []model.Service{
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
	for _, want := range []string{"模型运行日报 · 2026-08-16", "总计 3 · 正常 1 · 异常 1 · 未监测 1", "整体可用率 <code>", "故障 1 次", "<b>alpha</b> · A", "查看探针页"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日报缺少 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "beta") || strings.Contains(text, "gamma") {
		t.Fatalf("日报不应逐项罗列正常或未监测模型:\n%s", text)
	}
}

func TestBuildDailyDeliveriesSplitsOnlyBetweenModels(t *testing.T) {
	report := dailyReport{Date: time.Date(2026, 8, 16, 0, 0, 0, 0, beijingLocation), Total: 2, Unavailable: 2}
	for i := 0; i < 2; i++ {
		report.Incidents = append(report.Incidents, dailyModel{
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
