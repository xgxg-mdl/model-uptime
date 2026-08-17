package notification

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

// DailyRepository 提供日报所需的历史读取与原子入箱能力。
type DailyRepository interface {
	LoadResultsSinceWithPrevious(context.Context, string, int64, int64) ([]model.ProbeResult, error)
	EnqueueDailyReports(context.Context, string, []Delivery) error
}

// DailySnapshot 是生成一份日报所需的同一时刻配置视图。
type DailySnapshot struct {
	Telegram      Config
	Services      []model.Service
	StatusPageURL string
}

// DailyReporter 在北京时间零点汇总前一自然日的订阅模型运行情况。
type DailyReporter struct {
	repository DailyRepository
	snapshot   func() DailySnapshot
	logger     *slog.Logger
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewDailyReporter 创建日报任务。日报通过既有 outbox 投递，因此发送失败、
// 限流和进程重启后的投递恢复都复用既有行为。
func NewDailyReporter(repository DailyRepository, snapshot func() DailySnapshot, logger *slog.Logger) (*DailyReporter, error) {
	if repository == nil || snapshot == nil {
		return nil, fmt.Errorf("日报任务缺少 repository 或配置快照")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DailyReporter{repository: repository, snapshot: snapshot, logger: logger, ctx: ctx, cancel: cancel}, nil
}

// Start 会补发昨天尚未入箱的日报，然后等待下一个北京时间零点。
func (r *DailyReporter) Start() {
	r.wg.Add(1)
	go r.run()
}

// Close 停止等待中的日报任务。
func (r *DailyReporter) Close(context.Context) error {
	r.cancel()
	r.wg.Wait()
	return nil
}

func (r *DailyReporter) run() {
	defer r.wg.Done()
	r.reportPreviousDay(time.Now())
	for {
		next := nextBeijingMidnight(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			r.reportPreviousDay(next)
		}
	}
}

func (r *DailyReporter) reportPreviousDay(now time.Time) {
	dayEnd := beijingDayStart(now)
	dayStart := dayEnd.AddDate(0, 0, -1)
	for attempt := 0; attempt < persistenceAttemptLimit; attempt++ {
		err := r.Report(r.ctx, dayStart)
		if err == nil || r.ctx.Err() != nil {
			return
		}
		if attempt == persistenceAttemptLimit-1 {
			r.logger.Error("生成 Telegram 模型日报失败", "date", dayStart.Format("2006-01-02"), "err", err)
			return
		}
		delay := defaultPersistenceRetryDelays[attempt]
		r.logger.Warn("生成 Telegram 模型日报失败，将重试", "date", dayStart.Format("2006-01-02"), "err", err, "retry_in", delay)
		timer := time.NewTimer(delay)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Report 生成给定北京时间自然日的日报。该方法公开以支持确定性测试和手工补发。
func (r *DailyReporter) Report(ctx context.Context, dayStart time.Time) error {
	dayStart = beijingDayStart(dayStart)
	dayEnd := dayStart.AddDate(0, 0, 1)
	snapshot := r.snapshot()
	runtime, err := compileConfig(snapshot.Telegram)
	if err != nil {
		return err
	}
	services := make(map[string]model.Service, len(snapshot.Services))
	for _, service := range snapshot.Services {
		services[service.ID] = service
	}

	var allErrs []error
	for _, subscription := range runtime.subscriptions {
		if !subscription.Enabled {
			continue
		}
		report, err := r.buildReport(ctx, dayStart, dayEnd, subscription, services)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("生成订阅 %q 日报: %w", subscription.ID, err))
			continue
		}
		deliveries, err := buildDailyDeliveries(subscription, dayStart, report, snapshot.StatusPageURL)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("渲染订阅 %q 日报: %w", subscription.ID, err))
			continue
		}
		if err := r.repository.EnqueueDailyReports(ctx, dayStart.Format("2006-01-02"), deliveries); err != nil {
			allErrs = append(allErrs, fmt.Errorf("保存订阅 %q 日报: %w", subscription.ID, err))
		}
	}
	return joinErrors(allErrs)
}

type dailyModel struct {
	ServiceID string
	Model     string
	Provider  string
	Stats     model.DailyStats
}

type dailyReport struct {
	Date        time.Time
	Total       int
	Healthy     int
	Unavailable int
	Unobserved  int
	UpSec       int64
	DownSec     int64
	DownCount   int
	Incidents   []dailyModel
}

func (r *DailyReporter) buildReport(ctx context.Context, start, end time.Time, subscription compiledSubscription, services map[string]model.Service) (dailyReport, error) {
	report := dailyReport{Date: start}
	for _, id := range subscription.ServiceIDs {
		service, ok := services[id]
		if !ok {
			continue
		}
		report.Total++
		history, err := r.repository.LoadResultsSinceWithPrevious(ctx, id, start.Unix(), end.Unix())
		if err != nil {
			return dailyReport{}, err
		}
		stats := model.CalculateDailyStats(history, start.Unix(), end.Unix())
		if stats.ObservedSec() == 0 {
			report.Unobserved++
			continue
		}
		report.UpSec += stats.UpSec
		report.DownSec += stats.DownSec
		report.DownCount += stats.DownCount
		name := service.Model
		if name == "" {
			name = service.Name
		}
		item := dailyModel{
			ServiceID: id, Model: compactDailyLabel(name, 512),
			Provider: compactDailyLabel(service.Provider, 256), Stats: stats,
		}
		if stats.DownSec > 0 {
			report.Unavailable++
			report.Incidents = append(report.Incidents, item)
		} else {
			report.Healthy++
		}
	}
	sort.SliceStable(report.Incidents, func(i, j int) bool {
		if report.Incidents[i].Stats.DownSec != report.Incidents[j].Stats.DownSec {
			return report.Incidents[i].Stats.DownSec > report.Incidents[j].Stats.DownSec
		}
		return strings.ToLower(report.Incidents[i].Model) < strings.ToLower(report.Incidents[j].Model)
	})
	return report, nil
}

func buildDailyDeliveries(subscription compiledSubscription, dayStart time.Time, report dailyReport, statusPageURL string) ([]Delivery, error) {
	items := report.Incidents
	if len(items) == 0 {
		text, err := renderDailyText(subscription.Language, report, nil, statusPageURL)
		if err != nil {
			return nil, err
		}
		return []Delivery{{
			DedupeKey: dailyDedupeKey(dayStart, subscription.ID, 0), SubscriptionID: subscription.ID,
			Text: text, CreatedAt: dayStart.AddDate(0, 0, 1), AvailableAt: time.Now(),
		}}, nil
	}
	var deliveries []Delivery
	current := make([]dailyModel, 0, len(items))
	for _, item := range items {
		candidate := append(append([]dailyModel(nil), current...), item)
		if _, err := renderDailyText(subscription.Language, report, candidate, statusPageURL); err == nil {
			current = candidate
			continue
		} else if !errors.Is(err, ErrMessageTooLong) || len(current) == 0 {
			return nil, err
		}
		text, err := renderDailyText(subscription.Language, report, current, statusPageURL)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, Delivery{
			DedupeKey: dailyDedupeKey(dayStart, subscription.ID, len(deliveries)), SubscriptionID: subscription.ID,
			Text: text, CreatedAt: dayStart.AddDate(0, 0, 1), AvailableAt: time.Now(),
		})
		current = []dailyModel{item}
	}
	text, err := renderDailyText(subscription.Language, report, current, statusPageURL)
	if err != nil {
		return nil, err
	}
	return append(deliveries, Delivery{
		DedupeKey: dailyDedupeKey(dayStart, subscription.ID, len(deliveries)), SubscriptionID: subscription.ID,
		Text: text, CreatedAt: dayStart.AddDate(0, 0, 1), AvailableAt: time.Now(),
	}), nil
}

func renderDailyText(language string, report dailyReport, incidents []dailyModel, statusPageURL string) (string, error) {
	availability := 0.0
	if total := report.UpSec + report.DownSec; total > 0 {
		availability = float64(report.UpSec) / float64(total) * 100
	}
	var output strings.Builder
	if normalizeLanguage(language) == LanguageEnglish {
		fmt.Fprintf(&output, "<b>Model daily report · %s (UTC+8)</b>\n", report.Date.Format("2006-01-02"))
		fmt.Fprintf(&output, "Total %d · Healthy %d · Incident %d · Unobserved %d\n", report.Total, report.Healthy, report.Unavailable, report.Unobserved)
		fmt.Fprintf(&output, "Availability <code>%.2f%%</code> · Incidents %d · Downtime %s", availability, report.DownCount, formatDurationEN(report.DownSec))
		if len(incidents) > 0 {
			output.WriteString("\n\n<b>INCIDENTS</b>")
			for _, item := range incidents {
				fmt.Fprintf(&output, "\n❌ <b>%s</b>%s · %.2f%% · %s · %d incidents", template.HTMLEscapeString(item.Model), dailyProviderEN(item.Provider), item.Stats.UptimePct(), formatDurationEN(item.Stats.DownSec), item.Stats.DownCount)
			}
		}
	} else {
		fmt.Fprintf(&output, "<b>模型运行日报 · %s（北京时间）</b>\n", report.Date.Format("2006-01-02"))
		fmt.Fprintf(&output, "总计 %d · 正常 %d · 异常 %d · 未监测 %d\n", report.Total, report.Healthy, report.Unavailable, report.Unobserved)
		fmt.Fprintf(&output, "整体可用率 <code>%.2f%%</code> · 故障 %d 次 · 异常 %s", availability, report.DownCount, formatDurationCN(report.DownSec))
		if len(incidents) > 0 {
			output.WriteString("\n\n<b>异常模型</b>")
			for _, item := range incidents {
				fmt.Fprintf(&output, "\n❌ <b>%s</b>%s · %.2f%% · %s · %d 次", template.HTMLEscapeString(item.Model), dailyProviderCN(item.Provider), item.Stats.UptimePct(), formatDurationCN(item.Stats.DownSec), item.Stats.DownCount)
			}
		}
	}
	text := output.String()
	if utf8.RuneCountInString(text) > TelegramMessageLimit {
		return "", ErrMessageTooLong
	}
	return appendStatusPageLink(text, statusPageURL, language)
}

func dailyProviderCN(provider string) string {
	if provider == "" {
		return ""
	}
	return " · " + template.HTMLEscapeString(provider)
}

func dailyProviderEN(provider string) string { return dailyProviderCN(provider) }

func compactDailyLabel(value string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	for index := range value {
		if utf8.RuneCountInString(value[:index]) >= maxRunes-3 {
			return value[:index] + "..."
		}
	}
	return value
}

func dailyDedupeKey(dayStart time.Time, subscriptionID string, shard int) string {
	return fmt.Sprintf("daily:%s:%s:%d", dayStart.Format("2006-01-02"), subscriptionID, shard)
}

func beijingDayStart(value time.Time) time.Time {
	local := value.In(beijingLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijingLocation)
}

func nextBeijingMidnight(now time.Time) time.Time {
	return beijingDayStart(now).AddDate(0, 0, 1)
}

func joinErrors(errs []error) error { return errors.Join(errs...) }
