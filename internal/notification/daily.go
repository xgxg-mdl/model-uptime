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

// Start 只等待下一个北京时间零点。进程启动或升级本身不属于日报触发条件。
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
	CurrentDown int
	Recovered   int
	Unobserved  int
	UpSec       int64
	DownSec     int64
	DownCount   int
	Models      []dailyModel
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
		name := service.Model
		if name == "" {
			name = service.Name
		}
		item := dailyModel{
			ServiceID: id, Model: compactDailyLabel(name, 512),
			Provider: compactDailyLabel(service.Provider, 256), Stats: stats,
		}
		if stats.ObservedSec() == 0 {
			report.Unobserved++
			report.Models = append(report.Models, item)
			continue
		}
		report.UpSec += stats.UpSec
		report.DownSec += stats.DownSec
		report.DownCount += stats.DownCount
		if stats.DownSec > 0 {
			report.Unavailable++
			if stats.LastOK() {
				report.Recovered++
			} else {
				report.CurrentDown++
			}
		} else {
			report.Healthy++
		}
		report.Models = append(report.Models, item)
	}
	sort.SliceStable(report.Models, func(i, j int) bool {
		left, right := dailyStatusRank(report.Models[i]), dailyStatusRank(report.Models[j])
		if left != right {
			return left < right
		}
		leftName := strings.ToLower(report.Models[i].Provider + "/" + report.Models[i].Model)
		rightName := strings.ToLower(report.Models[j].Provider + "/" + report.Models[j].Model)
		return leftName < rightName
	})
	return report, nil
}

func buildDailyDeliveries(subscription compiledSubscription, dayStart time.Time, report dailyReport, statusPageURL string) ([]Delivery, error) {
	items := report.Models
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

func renderDailyText(language string, report dailyReport, models []dailyModel, statusPageURL string) (string, error) {
	availability := 0.0
	if total := report.UpSec + report.DownSec; total > 0 {
		availability = float64(report.UpSec) / float64(total) * 100
	}
	var output strings.Builder
	if normalizeLanguage(language) == LanguageEnglish {
		output.WriteString("📊 <b>Model daily report</b>\n\n")
		fmt.Fprintf(&output, "<blockquote><b>Date</b>　<code>%s</code> (UTC+8)\n", report.Date.Format("2006-01-02"))
		fmt.Fprintf(&output, "<b>Scope</b>　%d models · 🟢 %d healthy · 🟡 %d recovered · 🔴 %d down · ⚪ %d no data\n", report.Total, report.Healthy, report.Recovered, report.CurrentDown, report.Unobserved)
		fmt.Fprintf(&output, "<b>Availability</b>　<code>%.2f%%</code>\n", availability)
		fmt.Fprintf(&output, "<b>Incidents</b>　%d · %s downtime</blockquote>", report.DownCount, formatDurationEN(report.DownSec))
		if len(models) > 0 {
			output.WriteString("\n\n<b>Model status</b>\n<blockquote>")
			for index, item := range models {
				if index > 0 {
					output.WriteByte('\n')
				}
				output.WriteString(renderDailyModelEN(item))
			}
			output.WriteString("</blockquote>")
		}
	} else {
		output.WriteString("📊 <b>模型运行日报</b>\n\n")
		fmt.Fprintf(&output, "<blockquote><b>日期</b>　<code>%s</code>（北京时间）\n", report.Date.Format("2006-01-02"))
		fmt.Fprintf(&output, "<b>范围</b>　%d 个模型 · 🟢 %d 正常 · 🟡 %d 已恢复 · 🔴 %d 异常 · ⚪ %d 无数据\n", report.Total, report.Healthy, report.Recovered, report.CurrentDown, report.Unobserved)
		fmt.Fprintf(&output, "<b>可用率</b>　<code>%.2f%%</code>\n", availability)
		fmt.Fprintf(&output, "<b>故障</b>　%d 次 · 累计异常 %s</blockquote>", report.DownCount, formatDurationCN(report.DownSec))
		if len(models) > 0 {
			output.WriteString("\n\n<b>模型状态</b>\n<blockquote>")
			for index, item := range models {
				if index > 0 {
					output.WriteByte('\n')
				}
				output.WriteString(renderDailyModelCN(item))
			}
			output.WriteString("</blockquote>")
		}
	}
	text := output.String()
	if utf8.RuneCountInString(text) > TelegramMessageLimit {
		return "", ErrMessageTooLong
	}
	return appendStatusPageLink(text, statusPageURL, language)
}

func renderDailyModelCN(item dailyModel) string {
	label := dailyModelLabel(item)
	if item.Stats.ObservedSec() == 0 {
		return "⚪ " + label + " · 无数据"
	}
	if !item.Stats.LastOK() {
		return fmt.Sprintf("🔴 %s · <code>%.2f%%</code> · 异常 %s · %d 次", label, item.Stats.UptimePct(), formatDurationCN(item.Stats.DownSec), item.Stats.DownCount)
	}
	if item.Stats.DownSec > 0 {
		return fmt.Sprintf("🟡 %s · <code>%.2f%%</code> · 异常 %s · %d 次", label, item.Stats.UptimePct(), formatDurationCN(item.Stats.DownSec), item.Stats.DownCount)
	}
	return fmt.Sprintf("🟢 %s · <code>%.2f%%</code>", label, item.Stats.UptimePct())
}

func renderDailyModelEN(item dailyModel) string {
	label := dailyModelLabel(item)
	if item.Stats.ObservedSec() == 0 {
		return "⚪ " + label + " · no data"
	}
	if !item.Stats.LastOK() {
		return fmt.Sprintf("🔴 %s · <code>%.2f%%</code> · %s down · %d incidents", label, item.Stats.UptimePct(), formatDurationEN(item.Stats.DownSec), item.Stats.DownCount)
	}
	if item.Stats.DownSec > 0 {
		return fmt.Sprintf("🟡 %s · <code>%.2f%%</code> · %s down · %d incidents", label, item.Stats.UptimePct(), formatDurationEN(item.Stats.DownSec), item.Stats.DownCount)
	}
	return fmt.Sprintf("🟢 %s · <code>%.2f%%</code>", label, item.Stats.UptimePct())
}

func dailyModelLabel(item dailyModel) string {
	modelName := "<code>" + template.HTMLEscapeString(item.Model) + "</code>"
	if item.Provider == "" {
		return modelName
	}
	return "<b>" + template.HTMLEscapeString(item.Provider) + "</b> / " + modelName
}

func dailyStatusRank(item dailyModel) int {
	if item.Stats.ObservedSec() == 0 {
		return 3
	}
	if !item.Stats.LastOK() {
		return 0
	}
	if item.Stats.DownSec > 0 {
		return 1
	}
	return 2
}

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
