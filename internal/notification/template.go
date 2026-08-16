package notification

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

// TelegramMessageLimit 是 Telegram sendMessage 接受的最大消息字符数。
const TelegramMessageLimit = 4096

// DefaultTemplate 是默认中文模板，同时展示一轮探测中的异常与恢复模型。
const DefaultTemplate = `<b>{{if and .DownModels .RecoveredModels}}⚠️ 模型状态变更：多项状态变化{{else if .DownModels}}❌ 模型状态变更：检测到异常{{else}}✅ 模型状态变更：恢复正常{{end}}</b>
{{if gt .TotalChanges 1}}本次变更：异常 {{.DownCount}}，恢复 {{.RecoveryCount}}
{{end}}
{{if .DownModels}}{{range .DownModels}}<b>异常模型：{{.Model}}</b>{{if .Provider}}（{{.Provider}}）{{end}}
确认时间：{{formatBeijing .LastTS}}

<b>今日统计（{{beijingDate .LastTS}}，北京时间）</b>
今日运行时间：{{durationCN .TodayUpSec}}
今日异常时间：{{durationCN .TodayDownSec}}
今日异常次数：{{.TodayDownCount}} 次
今日可用率：{{printf "%.2f" .TodayUptimePct}}%

{{end}}{{end}}{{if .RecoveredModels}}{{range .RecoveredModels}}异常持续时间：{{durationCN .OutageDurationSec}}
监控模型：<b>{{.Model}}</b>{{if .Provider}}（{{.Provider}}）{{end}}
确认时间：{{formatBeijing .LastTS}}

<b>今日统计（{{beijingDate .LastTS}}，北京时间）</b>
今日运行时间：{{durationCN .TodayUpSec}}
今日异常时间：{{durationCN .TodayDownSec}}
今日异常次数：{{.TodayDownCount}} 次
今日可用率：{{printf "%.2f" .TodayUptimePct}}%

{{end}}{{end}}`

// EnglishTemplate 是英文内置模板，可按订阅选择。
const EnglishTemplate = `<b>{{if and .DownModels .RecoveredModels}}⚠️ Model status update: multiple changes{{else if .DownModels}}❌ Model status update: incident detected{{else}}✅ Model status update: recovered{{end}}</b>
{{if gt .TotalChanges 1}}Changes: {{.DownCount}} down, {{.RecoveryCount}} recovered
{{end}}
{{if .DownModels}}{{range .DownModels}}<b>Down model: {{.Model}}</b>{{if .Provider}} ({{.Provider}}){{end}}
Confirmed at: {{formatBeijing .LastTS}} (UTC+8)

<b>Today ({{beijingDate .LastTS}}, UTC+8)</b>
Uptime: {{durationEN .TodayUpSec}}
Downtime: {{durationEN .TodayDownSec}}
Incidents: {{.TodayDownCount}}
Availability: {{printf "%.2f" .TodayUptimePct}}%

{{end}}{{end}}{{if .RecoveredModels}}{{range .RecoveredModels}}Incident duration: {{durationEN .OutageDurationSec}}
Model: <b>{{.Model}}</b>{{if .Provider}} ({{.Provider}}){{end}}
Confirmed at: {{formatBeijing .LastTS}} (UTC+8)

<b>Today ({{beijingDate .LastTS}}, UTC+8)</b>
Uptime: {{durationEN .TodayUpSec}}
Downtime: {{durationEN .TodayDownSec}}
Incidents: {{.TodayDownCount}}
Availability: {{printf "%.2f" .TodayUptimePct}}%

{{end}}{{end}}`

var (
	ErrMessageTooLong       = errors.New("Telegram message exceeds 4096 characters")
	ErrInvalidStatusPageURL = errors.New("status page URL must be a complete http/https URL without credentials")
	beijingLocation         = time.FixedZone("Asia/Shanghai", 8*60*60)
	templateFunctions       = template.FuncMap{
		"beijingDate":   formatBeijingDate,
		"durationCN":    formatDurationCN,
		"durationEN":    formatDurationEN,
		"formatBeijing": formatBeijingTime,
	}
)

// TemplateContext 是聚合模板可访问的完整上下文。
type TemplateContext struct {
	ChangedAt       string
	DownCount       int
	RecoveryCount   int
	DownModels      []model.StatusChange
	RecoveredModels []model.StatusChange
	Changes         []model.StatusChange
	TotalChanges    int
}

// ValidateTemplate 检查模板语法，供配置层在不创建通知器时复用。
func ValidateTemplate(text string) error {
	_, err := parseTemplate(text)
	return err
}

// RenderTemplate 渲染并执行 Telegram 长度校验。
func RenderTemplate(text string, context TemplateContext) (string, error) {
	tmpl, err := parseTemplate(text)
	if err != nil {
		return "", err
	}
	return executeTemplate(tmpl, context)
}

// NewTemplateContext 将变化按最终状态拆分成模板需要的三个列表。
func NewTemplateContext(changedAt time.Time, changes []model.StatusChange) TemplateContext {
	final := finalChanges(changes)
	sortChangesForDelivery(final)
	context := TemplateContext{
		ChangedAt:    changedAt.In(beijingLocation).Format("2006-01-02 15:04:05"),
		Changes:      final,
		TotalChanges: len(final),
	}
	for _, change := range final {
		if change.Status == "up" {
			context.RecoveredModels = append(context.RecoveredModels, change)
		} else {
			context.DownModels = append(context.DownModels, change)
		}
	}
	context.DownCount = len(context.DownModels)
	context.RecoveryCount = len(context.RecoveredModels)
	return context
}

func parseTemplate(text string) (*template.Template, error) {
	if strings.TrimSpace(text) == "" {
		text = DefaultTemplate
	}
	return template.New("telegram").Funcs(templateFunctions).Option("missingkey=error").Parse(text)
}

func formatBeijingTime(timestamp int64) string {
	if timestamp <= 0 {
		return "-"
	}
	return time.Unix(timestamp, 0).In(beijingLocation).Format("2006-01-02 15:04:05")
}

func formatBeijingDate(timestamp int64) string {
	if timestamp <= 0 {
		return "-"
	}
	return time.Unix(timestamp, 0).In(beijingLocation).Format("2006-01-02")
}

func formatDurationCN(seconds int64) string {
	if seconds < 60 {
		return "不足 1 分钟"
	}
	if seconds < 60*60 {
		return fmt.Sprintf("%.1f 分钟", float64(seconds)/60)
	}
	hours := seconds / (60 * 60)
	minutes := seconds % (60 * 60) / 60
	if minutes == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
}

func formatDurationEN(seconds int64) string {
	if seconds < 60 {
		return "less than 1 minute"
	}
	if seconds < 60*60 {
		return fmt.Sprintf("%.1f minutes", float64(seconds)/60)
	}
	hours := seconds / (60 * 60)
	minutes := seconds % (60 * 60) / 60
	if minutes == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, minutes)
}

func executeTemplate(tmpl *template.Template, context TemplateContext) (string, error) {
	var output bytes.Buffer
	if err := tmpl.Execute(&output, context); err != nil {
		return "", err
	}
	text := output.String()
	if utf8.RuneCountInString(text) > TelegramMessageLimit {
		return "", ErrMessageTooLong
	}
	return text, nil
}

func appendStatusPageLink(text, statusPageURL, language string) (string, error) {
	statusPageURL = strings.TrimSpace(statusPageURL)
	if statusPageURL == "" {
		return text, nil
	}
	parsed, err := url.Parse(statusPageURL)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil {
		return "", ErrInvalidStatusPageURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrInvalidStatusPageURL
	}
	label := "查看探针页"
	if normalizeLanguage(language) == LanguageEnglish {
		label = "Open status page"
	}
	text = strings.TrimRight(text, "\n") + `

<a href="` + template.HTMLEscapeString(statusPageURL) + `">` + label + `</a>`
	if utf8.RuneCountInString(text) > TelegramMessageLimit {
		return "", ErrMessageTooLong
	}
	return text, nil
}
