package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
)

func TestLoadDefaultsOnMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load(missing) err = %v", err)
	}
	if c.AdminToken != "" {
		t.Errorf("AdminToken 应为空，got %q", c.AdminToken)
	}
	if len(c.Services) != 0 {
		t.Errorf("Services 应为空")
	}
}

func TestLoadNormalizesAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
admin_token: sekret
page:
  show_uptime: false
services:
  - id: s1
    name: gpt-test
    protocol: chat
    model: gpt-test
    base_url: https://api.example.com/v1/
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load err = %v", err)
	}
	if c.AdminToken != "sekret" {
		t.Errorf("AdminToken = %q", c.AdminToken)
	}
	// 默认值
	if c.Page.HistoryLen != 60 {
		t.Errorf("HistoryLen 默认应为 60，got %d", c.Page.HistoryLen)
	}
	if c.Page.Title == "" {
		t.Error("Title 应有默认值")
	}
	// 全关回退全开
	if !c.Page.ShowUptime || !c.Page.ShowSamples || !c.Page.ShowLatency || !c.Page.ShowAvgLoad {
		t.Error("全部统计维度关闭时应回退全开")
	}
	svc := c.Services[0]
	if svc.IntervalSec != 60 || svc.TimeoutSec != 15 {
		t.Errorf("服务默认 interval/timeout = %d/%d", svc.IntervalSec, svc.TimeoutSec)
	}
	if !svc.IsStreaming() {
		t.Error("LLM 服务未配置 stream 时应默认流式")
	}
	if strings.HasSuffix(svc.BaseURL, "/") {
		t.Errorf("BaseURL 应去除末尾斜杠: %q", svc.BaseURL)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cases := []string{
		// 未知协议
		"services:\n  - id: a\n    name: a\n    protocol: nope\n    model: m\n    base_url: http://x",
		// 重复 id
		"services:\n  - id: a\n    name: x\n    protocol: chat\n    model: m\n    base_url: http://x\n  - id: a\n    name: y\n    protocol: chat\n    model: m\n    base_url: http://x",
		// LLM 协议缺 model
		"services:\n  - id: a\n    name: a\n    protocol: message\n    base_url: http://x",
		// http 协议缺 base_url
		"services:\n  - id: a\n    name: a\n    protocol: http",
		// 探针页公开地址不是安全的绝对 HTTP(S) URL
		"page:\n  public_url: javascript:alert(1)",
	}
	for i, content := range cases {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("case %d: 期望校验失败", i)
		}
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	c := &Config{
		AdminToken: "tok",
		Page: model.PageConfig{
			Title:      "My Status",
			Subtitle:   "status.example",
			PublicURL:  "https://status.example.com/",
			HistoryLen: 30,
		},
		Services: []model.Service{{
			ID: "s1", Name: "svc-1", Protocol: model.ProtocolHTTP,
			BaseURL: "https://example.com/health",
		}},
		Telegram: notifier.Config{
			BotToken: "bot-token",
			Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "-100", ServiceIDs: []string{"s1"},
			}},
		},
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save err = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load err = %v", err)
	}
	if got.AdminToken != "tok" {
		t.Errorf("round-trip AdminToken = %q", got.AdminToken)
	}
	if got.Page.PublicURL != "https://status.example.com/" {
		t.Errorf("round-trip PublicURL = %q", got.Page.PublicURL)
	}
	if len(got.Services) != 1 || got.Services[0].ID != "s1" {
		t.Errorf("round-trip services 不一致: %+v", got.Services)
	}
	if got.Telegram.BotToken != "bot-token" || len(got.Telegram.Subscriptions) != 1 {
		t.Errorf("round-trip Telegram 配置不一致: %+v", got.Telegram)
	}
	if got.Telegram.Subscriptions[0].Template != notifier.DefaultTemplate {
		t.Error("空通知模板应填充默认卡片")
	}
	if got.Telegram.Subscriptions[0].Language != notifier.DefaultLanguage {
		t.Errorf("空通知语言应默认为中文，得到 %q", got.Telegram.Subscriptions[0].Language)
	}
}

func TestValidateTelegramSubscriptions(t *testing.T) {
	service := model.Service{ID: "s1", Name: "svc", Protocol: model.ProtocolHTTP, BaseURL: "https://example.com"}
	cases := []struct {
		name     string
		telegram notifier.Config
	}{
		{
			name: "启用订阅缺少 token",
			telegram: notifier.Config{Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "1", ServiceIDs: []string{"s1"},
			}}},
		},
		{
			name: "引用不存在服务",
			telegram: notifier.Config{BotToken: "token", Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "1", ServiceIDs: []string{"missing"},
			}}},
		},
		{
			name: "模板语法错误",
			telegram: notifier.Config{BotToken: "token", Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "1", ServiceIDs: []string{"s1"}, Template: "{{",
			}}},
		},
		{
			name: "不支持的通知语言",
			telegram: notifier.Config{BotToken: "token", Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "1", Language: "fr-FR", ServiceIDs: []string{"s1"},
			}}},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Services: []model.Service{service}, Telegram: test.telegram}
			cfg.Normalize()
			if err := cfg.Validate(); err == nil {
				t.Fatal("期望 Telegram 配置校验失败")
			}
		})
	}
}

func TestExampleConfigLoads(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("示例配置必须保持可加载: %v", err)
	}
	if len(config.Telegram.Subscriptions) != 1 {
		t.Fatalf("示例配置应包含 Telegram 聚合订阅: %+v", config.Telegram)
	}
	if strings.TrimSpace(config.Telegram.Subscriptions[0].Template) != strings.TrimSpace(notifier.DefaultTemplate) {
		t.Error("示例配置中的模板必须与内置默认卡片一致")
	}
	if config.Telegram.Subscriptions[0].Language != notifier.DefaultLanguage {
		t.Errorf("示例配置应默认使用中文通知，得到 %q", config.Telegram.Subscriptions[0].Language)
	}
}
