package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lefachao/model-uptime/internal/model"
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
	if !svc.IsEnabled() {
		t.Error("服务默认应启用")
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
			HistoryLen: 30,
		},
		Services: []model.Service{{
			ID: "s1", Name: "svc-1", Protocol: model.ProtocolHTTP,
			BaseURL: "https://example.com/health",
		}},
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
	if len(got.Services) != 1 || got.Services[0].ID != "s1" {
		t.Errorf("round-trip services 不一致: %+v", got.Services)
	}
}
