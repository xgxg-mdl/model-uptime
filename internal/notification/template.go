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

// DefaultTemplate 是默认中文模板：沿用 Smart_Group_Bot 的标题、引用卡片和
// “粗体字段名 + 全角空格 + 值”结构，在紧凑布局中保留处置所需信息。
const DefaultTemplate = `{{if and .DownModels .RecoveredModels}}⚠️ <b>模型状态更新</b>{{else if .DownModels}}🔴 <b>模型异常告警</b>{{else}}🟢 <b>模型恢复通知</b>{{end}}

<blockquote><b>时间</b>　<code>{{.ChangedTime}}</code>（北京时间）
<b>变化</b>　🔴 {{.DownCount}} 异常 · 🟢 {{.RecoveryCount}} 恢复
{{range .DownModels}}<b>异常</b>　🔴 {{if .Provider}}<b>{{.Provider}}</b> / {{end}}<code>{{.Model}}</code>
{{end}}{{range .RecoveredModels}}<b>恢复</b>　🟢 {{if .Provider}}<b>{{.Provider}}</b> / {{end}}<code>{{.Model}}</code>{{if .OutageDurationSec}}
<b>用时</b>　<code>{{durationCN .OutageDurationSec}}</code>{{if .LatencyMS}} · 延迟 <code>{{.LatencyMS}} ms</code>{{end}}{{else if .LatencyMS}}
<b>延迟</b>　<code>{{.LatencyMS}} ms</code>{{end}}
{{end}}</blockquote>{{if .DownModels}}

<blockquote expandable><b>异常详情</b>
{{range .DownModels}}<b>模型</b>　{{if .Provider}}{{.Provider}} / {{end}}<code>{{.Model}}</code>
<b>原因</b>　{{if .Error}}{{.Error}}{{else}}未返回错误详情{{end}}
{{end}}</blockquote>{{end}}`

// EnglishTemplate 是英文内置模板，可按订阅选择。
const EnglishTemplate = `{{if and .DownModels .RecoveredModels}}⚠️ <b>Model status update</b>{{else if .DownModels}}🔴 <b>Model incident alert</b>{{else}}🟢 <b>Model recovery</b>{{end}}

<blockquote><b>Time</b>　<code>{{.ChangedTime}}</code> (UTC+8)
<b>Changes</b>　🔴 {{.DownCount}} down · 🟢 {{.RecoveryCount}} recovered
{{range .DownModels}}<b>Down</b>　🔴 {{if .Provider}}<b>{{.Provider}}</b> / {{end}}<code>{{.Model}}</code>
{{end}}{{range .RecoveredModels}}<b>Recovered</b>　🟢 {{if .Provider}}<b>{{.Provider}}</b> / {{end}}<code>{{.Model}}</code>{{if .OutageDurationSec}}
<b>Duration</b>　<code>{{durationEN .OutageDurationSec}}</code>{{if .LatencyMS}} · latency <code>{{.LatencyMS}} ms</code>{{end}}{{else if .LatencyMS}}
<b>Latency</b>　<code>{{.LatencyMS}} ms</code>{{end}}
{{end}}</blockquote>{{if .DownModels}}

<blockquote expandable><b>Incident details</b>
{{range .DownModels}}<b>Model</b>　{{if .Provider}}{{.Provider}} / {{end}}<code>{{.Model}}</code>
<b>Reason</b>　{{if .Error}}{{.Error}}{{else}}No error details returned{{end}}
{{end}}</blockquote>{{end}}`

var (
	ErrMessageTooLong       = errors.New("Telegram 消息超过 4096 字符")
	ErrInvalidStatusPageURL = errors.New("探针页地址必须是无账号密码的完整 http/https 地址")
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
	ChangedTime     string
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
		ChangedTime:  changedAt.In(beijingLocation).Format("01-02 15:04"),
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
