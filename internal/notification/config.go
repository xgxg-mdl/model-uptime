package notification

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"slices"
	"sort"
	"strings"
)

const (
	DefaultLanguage = "zh-CN"
	LanguageEnglish = "en-US"
)

const legacyChineseTemplate = `<b>模型状态变更</b>
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

const legacyEnglishTemplate = `<b>MODEL STATUS UPDATE</b>
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

type compiledSubscription struct {
	Subscription
	template    *template.Template
	fingerprint string
}

type runtimeConfig struct {
	botToken      string
	subscriptions []compiledSubscription
}

func (config runtimeConfig) equivalent(other runtimeConfig) bool {
	if config.botToken != other.botToken || len(config.subscriptions) != len(other.subscriptions) {
		return false
	}
	for index := range config.subscriptions {
		left := config.subscriptions[index].Subscription
		right := other.subscriptions[index].Subscription
		if left.ID != right.ID || left.Name != right.Name || left.Enabled != right.Enabled ||
			left.ChatID != right.ChatID || left.Language != right.Language ||
			left.Template != right.Template || !slices.Equal(left.ServiceIDs, right.ServiceIDs) {
			return false
		}
	}
	return true
}

func (config runtimeConfig) activeFingerprints() map[string]string {
	fingerprints := make(map[string]string, len(config.subscriptions))
	for _, subscription := range config.subscriptions {
		if subscription.Enabled {
			fingerprints[subscription.ID] = subscription.fingerprint
		}
	}
	return fingerprints
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
	// 识别历史版本写入配置的内置模板并升级；用户修改过的自定义模板保持不变。
	legacyBuiltIn := templateText == strings.TrimSpace(legacyChineseTemplate) ||
		templateText == strings.TrimSpace(legacyEnglishTemplate) ||
		templateText == strings.TrimSpace(legacyStatisticsTemplate(DefaultLanguage)) ||
		templateText == strings.TrimSpace(legacyStatisticsTemplate(LanguageEnglish))
	if templateText == "" || legacyBuiltIn || (originalLanguage == "" && templateText == strings.TrimSpace(EnglishTemplate)) {
		subscription.Template = TemplateForLanguage(subscription.Language)
	}
	for j := range subscription.ServiceIDs {
		subscription.ServiceIDs[j] = strings.TrimSpace(subscription.ServiceIDs[j])
	}
}

// legacyStatisticsTemplate 还原 v0.6.0 带错误详情的内置模板，用于无损识别并迁移。
func legacyStatisticsTemplate(language string) string {
	if normalizeLanguage(language) == LanguageEnglish {
		return strings.Replace(EnglishTemplate, "Confirmed at:", "Error: {{if .Error}}{{.Error}}{{else}}Probe failed{{end}}\nConfirmed at:", 1)
	}
	return strings.Replace(DefaultTemplate, "确认时间：", "异常原因：{{if .Error}}{{.Error}}{{else}}探测失败{{end}}\n确认时间：", 1)
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

func compileConfig(config Config) (runtimeConfig, error) {
	runtime := runtimeConfig{botToken: strings.TrimSpace(config.BotToken)}
	seen := make(map[string]struct{}, len(config.Subscriptions))
	for _, subscription := range config.Subscriptions {
		// 配置编译结果必须拥有自己的切片，调用方后续修改不能改变运行时筛选。
		subscription.ServiceIDs = append([]string(nil), subscription.ServiceIDs...)
		normalizeSubscription(&subscription)
		if subscription.ID == "" {
			return runtimeConfig{}, errors.New("Telegram subscription id is required")
		}
		if _, exists := seen[subscription.ID]; exists {
			return runtimeConfig{}, fmt.Errorf("duplicate Telegram subscription id: %q", subscription.ID)
		}
		seen[subscription.ID] = struct{}{}
		if subscription.Enabled && runtime.botToken == "" {
			return runtimeConfig{}, fmt.Errorf("Telegram subscription %q is enabled but the bot token is empty", subscription.ID)
		}
		if subscription.Enabled && subscription.ChatID == "" {
			return runtimeConfig{}, fmt.Errorf("Telegram subscription %q is enabled but the chat id is empty", subscription.ID)
		}
		if subscription.Language != DefaultLanguage && subscription.Language != LanguageEnglish {
			return runtimeConfig{}, fmt.Errorf("Telegram subscription %q has unsupported language %q", subscription.ID, subscription.Language)
		}
		tmpl, err := parseTemplate(subscription.Template)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("Telegram subscription %q has an invalid template: %w", subscription.ID, err)
		}
		runtime.subscriptions = append(runtime.subscriptions, compiledSubscription{
			Subscription: subscription,
			template:     tmpl,
			fingerprint:  subscriptionFingerprint(runtime.botToken, subscription),
		})
	}
	return runtime, nil
}

func subscriptionFingerprint(botToken string, subscription Subscription) string {
	serviceIDs := append([]string(nil), subscription.ServiceIDs...)
	sort.Strings(serviceIDs)
	serviceIDs = slices.Compact(serviceIDs)
	identity := struct {
		BotToken   string
		ID         string
		ChatID     string
		Language   string
		ServiceIDs []string
		Template   string
	}{
		BotToken: botToken, ID: subscription.ID, ChatID: subscription.ChatID,
		Language: subscription.Language, ServiceIDs: serviceIDs, Template: subscription.Template,
	}
	encoded, _ := json.Marshal(identity) // 字段类型固定，编码不会失败。
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
