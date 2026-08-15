// Package notifier 负责将模型状态变化聚合为 Telegram 通知。
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// TelegramMessageLimit 是 Telegram sendMessage 接受的最大消息字符数。
	TelegramMessageLimit  = 4096
	DefaultLanguage       = "zh-CN"
	LanguageEnglish       = "en-US"
	defaultQueueSize      = 64
	defaultRequestTimeout = 10 * time.Second
)

// DefaultTemplate 是默认中文模板，同时展示一轮探测中的异常与恢复模型。
const DefaultTemplate = `<b>模型状态变更</b>
<code>{{.ChangedAt}}</code>

<b>状态概览</b>
🔴 异常 <b>{{.DownCount}}</b>   🟢 恢复 <b>{{.RecoveryCount}}</b>   变更 <b>{{.TotalChanges}}</b>

{{if .DownModels}}<b>🔴 异常模型</b>
{{range .DownModels}}• <b>{{.Model}}</b>{{if .Provider}} · {{.Provider}}{{end}}
  {{if .Error}}<code>{{.Error}}</code>{{else}}<code>探测失败</code>{{end}}
{{end}}
{{end}}{{if .RecoveredModels}}<b>🟢 恢复模型</b>
{{range .RecoveredModels}}• <b>{{.Model}}</b>{{if .Provider}} · {{.Provider}}{{end}}
  延迟 <code>{{.LatencyMS}} ms</code> · 可用率 <code>{{printf "%.2f" .UptimePct}}%</code>
{{end}}{{end}}`

// EnglishTemplate 是英文内置模板，可按订阅选择。
const EnglishTemplate = `<b>MODEL STATUS UPDATE</b>
<code>{{.ChangedAt}}</code>

<b>OVERVIEW</b>
🔴 Down <b>{{.DownCount}}</b>   🟢 Recovered <b>{{.RecoveryCount}}</b>   Total <b>{{.TotalChanges}}</b>

{{if .DownModels}}<b>🔴 DOWN</b>
{{range .DownModels}}• <b>{{.Model}}</b>{{if .Provider}} · {{.Provider}}{{end}}
  {{if .Error}}<code>{{.Error}}</code>{{else}}<code>Probe failed</code>{{end}}
{{end}}
{{end}}{{if .RecoveredModels}}<b>🟢 RECOVERED</b>
{{range .RecoveredModels}}• <b>{{.Model}}</b>{{if .Provider}} · {{.Provider}}{{end}}
  Latency <code>{{.LatencyMS}} ms</code> · Uptime <code>{{printf "%.2f" .UptimePct}}%</code>
{{end}}{{end}}`

var (
	ErrQueueFull            = errors.New("Telegram 通知队列已满")
	ErrClosed               = errors.New("Telegram 通知器已关闭")
	ErrMessageTooLong       = errors.New("Telegram 消息超过 4096 字符")
	ErrSubscriptionNotFound = errors.New("Telegram 订阅不存在")
)

// HTTPClient 允许测试或调用方注入 HTTP 实现。
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Config 是通知器运行时配置的独立快照。
type Config struct {
	BotToken      string         `yaml:"bot_token" json:"bot_token"`
	Subscriptions []Subscription `yaml:"subscriptions" json:"subscriptions"`
}

// Subscription 描述一组模型共享的 Telegram 接收目标和模板。
type Subscription struct {
	ID         string   `yaml:"id" json:"id"`
	Name       string   `yaml:"name" json:"name"`
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	ChatID     string   `yaml:"chat_id" json:"chat_id"`
	Language   string   `yaml:"language" json:"language"`
	ServiceIDs []string `yaml:"service_ids" json:"service_ids"`
	Template   string   `yaml:"template" json:"template"`
}

// Change 是一个模型在本轮探测中的最终状态变化。
type Change struct {
	ServiceID      string
	Model          string
	Provider       string
	Protocol       string
	OK             bool
	LatencyMS      int64
	Error          string
	UptimePct      float64
	Samples        int
	PreviousStatus string
	Status         string
	LastTS         int64
}

// Batch 是一次调度轮次产生的全部状态变化。
type Batch struct {
	ChangedAt time.Time
	Changes   []Change
}

// TemplateContext 是聚合模板可访问的完整上下文。
type TemplateContext struct {
	ChangedAt       string
	DownCount       int
	RecoveryCount   int
	DownModels      []Change
	RecoveredModels []Change
	Changes         []Change
	TotalChanges    int
}

// Options 配置通知器的运行时依赖。
type Options struct {
	Client      HTTPClient
	Logger      *slog.Logger
	QueueSize   int
	APIBaseURL  string
	RetryDelays []time.Duration
}

type compiledSubscription struct {
	Subscription
	template *template.Template
}

type runtimeConfig struct {
	botToken      string
	subscriptions []compiledSubscription
}

type sendJob struct {
	botToken string
	chatID   string
	text     string
	name     string
}

// Notifier 异步消费有界队列，并持有可热更新的配置快照。
type Notifier struct {
	client      HTTPClient
	logger      *slog.Logger
	apiBaseURL  string
	retryDelays []time.Duration

	configMu sync.RWMutex
	config   runtimeConfig

	lifecycleMu sync.RWMutex
	closed      bool
	jobs        chan sendJob
	wg          sync.WaitGroup
}

// New 校验初始配置并启动单个有序发送 worker。
func New(options Options, config Config) (*Notifier, error) {
	compiled, err := compileConfig(config)
	if err != nil {
		return nil, err
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: defaultRequestTimeout}
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.QueueSize <= 0 {
		options.QueueSize = defaultQueueSize
	}
	if strings.TrimSpace(options.APIBaseURL) == "" {
		options.APIBaseURL = "https://api.telegram.org"
	}
	retryDelays := options.RetryDelays
	if retryDelays == nil {
		retryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	n := &Notifier{
		client:      options.Client,
		logger:      options.Logger,
		apiBaseURL:  strings.TrimRight(options.APIBaseURL, "/"),
		retryDelays: append([]time.Duration(nil), retryDelays...),
		config:      compiled,
		jobs:        make(chan sendJob, options.QueueSize),
	}
	n.wg.Add(1)
	go n.run()
	return n, nil
}

// UpdateConfig 先完整编译新模板，再原子替换运行时配置。
func (n *Notifier) UpdateConfig(config Config) error {
	compiled, err := compileConfig(config)
	if err != nil {
		return err
	}
	n.configMu.Lock()
	n.config = compiled
	n.configMu.Unlock()
	return nil
}

// NormalizeConfig 清理配置字段，并按订阅语言填入内置卡片。
func NormalizeConfig(config *Config) {
	config.BotToken = strings.TrimSpace(config.BotToken)
	for i := range config.Subscriptions {
		normalizeSubscription(&config.Subscriptions[i])
	}
}

// TemplateForLanguage 返回受支持语言的内置模板；未知语言交由配置校验报告。
func TemplateForLanguage(language string) string {
	if normalizeLanguage(language) == LanguageEnglish {
		return EnglishTemplate
	}
	return DefaultTemplate
}

func normalizeSubscription(subscription *Subscription) {
	originalLanguage := strings.TrimSpace(subscription.Language)
	subscription.ID = strings.TrimSpace(subscription.ID)
	subscription.Name = strings.TrimSpace(subscription.Name)
	subscription.ChatID = strings.TrimSpace(subscription.ChatID)
	subscription.Language = normalizeLanguage(originalLanguage)
	templateText := strings.TrimSpace(subscription.Template)
	// v0.4.x 会把内置英文模板写入配置；未声明语言时将它迁移为新的中文默认。
	if templateText == "" || (originalLanguage == "" && templateText == strings.TrimSpace(EnglishTemplate)) {
		subscription.Template = TemplateForLanguage(subscription.Language)
	}
	for j := range subscription.ServiceIDs {
		subscription.ServiceIDs[j] = strings.TrimSpace(subscription.ServiceIDs[j])
	}
}

func normalizeLanguage(language string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-")) {
	case "", "zh", "zh-cn":
		return DefaultLanguage
	case "en", "en-us":
		return LanguageEnglish
	default:
		return strings.TrimSpace(language)
	}
}

// ValidateConfig 校验订阅身份、凭据与模板，不启动后台 worker。
func ValidateConfig(config Config) error {
	_, err := compileConfig(config)
	return err
}

// Notify 按订阅筛选并聚合变化，每个订阅至多入队一条消息。
func (n *Notifier) Notify(batch Batch) error {
	config := n.configSnapshot()
	changes := finalChanges(batch.Changes)
	if len(changes) == 0 {
		return nil
	}
	changedAt := batch.ChangedAt
	if changedAt.IsZero() {
		changedAt = time.Now()
	}

	var errs []error
	for _, subscription := range config.subscriptions {
		if !subscription.Enabled {
			continue
		}
		selected := selectChanges(changes, subscription.ServiceIDs)
		if len(selected) == 0 {
			continue
		}
		ctx := NewTemplateContext(changedAt, selected)
		text, err := executeTemplate(subscription.template, ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("渲染订阅 %q: %w", subscription.ID, err))
			continue
		}
		if err := n.enqueue(sendJob{botToken: config.botToken, chatID: subscription.ChatID, text: text, name: subscription.ID}); err != nil {
			errs = append(errs, fmt.Errorf("订阅 %q: %w", subscription.ID, err))
		}
	}
	return errors.Join(errs...)
}

// SendTest 同步发送一条包含异常和恢复示例的消息，便于管理 API 返回准确结果。
func (n *Notifier) SendTest(ctx context.Context, subscriptionID string) error {
	config := n.configSnapshot()
	for _, subscription := range config.subscriptions {
		if subscription.ID != subscriptionID {
			continue
		}
		now := time.Now()
		downModel, recoveredModel, provider, probeError := "example-down", "example-recovered", "example", "probe timeout"
		if subscription.Language == DefaultLanguage {
			downModel, recoveredModel, provider, probeError = "示例异常模型", "示例恢复模型", "示例提供商", "探测超时"
		}
		templateContext := NewTemplateContext(now, []Change{
			{ServiceID: "example-down", Model: downModel, Provider: provider, Protocol: "chat", Error: probeError, PreviousStatus: "up", Status: "down", LastTS: now.Unix()},
			{ServiceID: "example-recovered", Model: recoveredModel, Provider: provider, Protocol: "chat", OK: true, LatencyMS: 128, PreviousStatus: "down", Status: "up", LastTS: now.Unix()},
		})
		text, err := executeTemplate(subscription.template, templateContext)
		if err != nil {
			return fmt.Errorf("渲染订阅 %q: %w", subscriptionID, err)
		}
		return n.sendWithRetry(ctx, sendJob{botToken: config.botToken, chatID: subscription.ChatID, text: text, name: subscription.ID})
	}
	return fmt.Errorf("%w: %s", ErrSubscriptionNotFound, subscriptionID)
}

// Close 停止接收新通知，并等待队列中的消息发送完毕。
func (n *Notifier) Close(ctx context.Context) error {
	n.lifecycleMu.Lock()
	if !n.closed {
		n.closed = true
		close(n.jobs)
	}
	n.lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
func NewTemplateContext(changedAt time.Time, changes []Change) TemplateContext {
	final := finalChanges(changes)
	sort.SliceStable(final, func(i, j int) bool {
		left, right := strings.ToLower(final[i].Model), strings.ToLower(final[j].Model)
		if left == right {
			return final[i].ServiceID < final[j].ServiceID
		}
		return left < right
	})
	context := TemplateContext{
		ChangedAt:    changedAt.Format("2006-01-02 15:04:05 MST"),
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

func compileConfig(config Config) (runtimeConfig, error) {
	runtime := runtimeConfig{botToken: strings.TrimSpace(config.BotToken)}
	seen := make(map[string]struct{}, len(config.Subscriptions))
	for _, subscription := range config.Subscriptions {
		normalizeSubscription(&subscription)
		if subscription.ID == "" {
			return runtimeConfig{}, errors.New("Telegram 订阅 id 不能为空")
		}
		if _, exists := seen[subscription.ID]; exists {
			return runtimeConfig{}, fmt.Errorf("Telegram 订阅 id 重复: %q", subscription.ID)
		}
		seen[subscription.ID] = struct{}{}
		if subscription.Enabled && runtime.botToken == "" {
			return runtimeConfig{}, fmt.Errorf("Telegram 订阅 %q 已启用但 bot token 为空", subscription.ID)
		}
		if subscription.Enabled && subscription.ChatID == "" {
			return runtimeConfig{}, fmt.Errorf("Telegram 订阅 %q 已启用但 chat id 为空", subscription.ID)
		}
		if subscription.Language != DefaultLanguage && subscription.Language != LanguageEnglish {
			return runtimeConfig{}, fmt.Errorf("Telegram 订阅 %q 的 language 不受支持: %q", subscription.ID, subscription.Language)
		}
		tmpl, err := parseTemplate(subscription.Template)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("Telegram 订阅 %q 模板无效: %w", subscription.ID, err)
		}
		subscription.ServiceIDs = append([]string(nil), subscription.ServiceIDs...)
		runtime.subscriptions = append(runtime.subscriptions, compiledSubscription{Subscription: subscription, template: tmpl})
	}
	return runtime, nil
}

func parseTemplate(text string) (*template.Template, error) {
	if strings.TrimSpace(text) == "" {
		text = DefaultTemplate
	}
	return template.New("telegram").Option("missingkey=error").Parse(text)
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

func finalChanges(changes []Change) []Change {
	positions := make(map[string]int, len(changes))
	merged := make([]Change, 0, len(changes))
	for _, change := range changes {
		change.ServiceID = strings.TrimSpace(change.ServiceID)
		if change.ServiceID == "" {
			continue
		}
		if change.Status == "" {
			if change.OK {
				change.Status = "up"
			} else {
				change.Status = "down"
			}
		}
		if change.Status != "up" && change.Status != "down" {
			continue
		}
		change.OK = change.Status == "up"
		if position, exists := positions[change.ServiceID]; exists {
			// 聚合窗口以首个旧状态和最后新状态为准，避免 up -> down -> up
			// 被误报为恢复事件。
			change.PreviousStatus = merged[position].PreviousStatus
			merged[position] = change
			continue
		}
		positions[change.ServiceID] = len(merged)
		merged = append(merged, change)
	}
	result := make([]Change, 0, len(merged))
	for _, change := range merged {
		if change.PreviousStatus != change.Status {
			result = append(result, change)
		}
	}
	return result
}

func selectChanges(changes []Change, serviceIDs []string) []Change {
	selectedIDs := make(map[string]struct{}, len(serviceIDs))
	for _, id := range serviceIDs {
		selectedIDs[id] = struct{}{}
	}
	selected := make([]Change, 0, len(changes))
	for _, change := range changes {
		if _, ok := selectedIDs[change.ServiceID]; ok {
			selected = append(selected, change)
		}
	}
	return selected
}

func (n *Notifier) configSnapshot() runtimeConfig {
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	return n.config
}

func (n *Notifier) enqueue(job sendJob) error {
	n.lifecycleMu.RLock()
	defer n.lifecycleMu.RUnlock()
	if n.closed {
		return ErrClosed
	}
	select {
	case n.jobs <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

func (n *Notifier) run() {
	defer n.wg.Done()
	for job := range n.jobs {
		if err := n.sendWithRetry(context.Background(), job); err != nil {
			n.logger.Error("Telegram 通知发送失败", "subscription", job.name, "err", err)
		}
	}
}

func (n *Notifier) sendWithRetry(ctx context.Context, job sendJob) error {
	var err error
	for attempt := 0; ; attempt++ {
		var retryable bool
		retryable, err = n.send(ctx, job)
		if err == nil || !retryable || attempt >= len(n.retryDelays) {
			return err
		}
		delay := n.retryDelays[attempt]
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
}

func (n *Notifier) send(ctx context.Context, job sendJob) (bool, error) {
	form := url.Values{
		"chat_id":                  {job.chatID},
		"text":                     {job.text},
		"parse_mode":               {"HTML"},
		"disable_web_page_preview": {"true"},
	}
	endpoint := n.apiBaseURL + "/bot" + job.botToken + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, redactTokenError(err, job.botToken)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := n.client.Do(request)
	if err != nil {
		return true, redactTokenError(err, job.botToken)
	}
	defer response.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode >= 200 && response.StatusCode < 300 && decodeErr == nil && result.OK {
		return false, nil
	}
	description := strings.TrimSpace(result.Description)
	if description == "" {
		if decodeErr != nil {
			description = decodeErr.Error()
		} else {
			description = http.StatusText(response.StatusCode)
		}
	}
	err = fmt.Errorf("Telegram API 返回 %d: %s", response.StatusCode, description)
	// 429 和服务端故障可恢复；其他 4xx 通常是 token、chat id 或模板参数错误。
	return response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500, err
}

func redactTokenError(err error, token string) error {
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "****")
	}
	return errors.New(message)
}
