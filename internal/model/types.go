// Package model 定义领域模型：监控目标、探测结果、页面显示配置。
package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 支持的协议类型。
const (
	ProtocolChat     = "chat"     // OpenAI Chat Completions
	ProtocolResponse = "response" // OpenAI Responses
	ProtocolMessage  = "message"  // Anthropic Messages
	ProtocolHTTP     = "http"     // 通用 HTTP
)

// APIKeySentinel 表示"编辑时保持原密钥不变"的哨兵值。
// 配置页编辑表单留空或填写该值时，更新接口保留旧密钥。
const APIKeySentinel = "***unchanged***"

// Service 是一个监控目标。字段同时用于 YAML 配置文件和配置页 JSON API。
type Service struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`                             // 状态页显示的模型名
	Provider    string `yaml:"provider,omitempty" json:"provider,omitempty"` // 提供商标签
	Protocol    string `yaml:"protocol" json:"protocol"`                     // chat|response|message|http
	Model       string `yaml:"model,omitempty" json:"model,omitempty"`       // 发给 API 的模型 ID
	BaseURL     string `yaml:"base_url" json:"base_url"`
	APIKey      string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Path        string `yaml:"path,omitempty" json:"path,omitempty"` // 覆盖默认请求路径
	SortOrder   int    `yaml:"sort_order,omitempty" json:"sort_order"`
	IntervalSec int    `yaml:"interval_sec,omitempty" json:"interval_sec,omitempty"`
	TimeoutSec  int    `yaml:"timeout_sec,omitempty" json:"timeout_sec,omitempty"`
	// 指针而非 bool：区分“未配置”（nil，默认启用）与“显式禁用”（false）
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// LLM 协议默认走流式 SSE；显式 false 保持同步 JSON 兼容模式。
	// http 协议忽略此字段。
	Stream *bool `yaml:"stream,omitempty" json:"stream,omitempty"`
	// http 协议专用
	Method       string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body         string            `yaml:"body,omitempty" json:"body,omitempty"`
	ExpectStatus int               `yaml:"expect_status,omitempty" json:"expect_status,omitempty"`
}

// IsEnabled 返回服务是否启用（未显式配置时默认启用）。
func (s *Service) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// IsStreaming 返回 LLM 探针是否走流式 SSE（未显式配置时默认流式）。
func (s *Service) IsStreaming() bool {
	return s.Protocol != ProtocolHTTP && (s.Stream == nil || *s.Stream)
}

// Normalize 填充默认值并清理字段。
func (s *Service) Normalize() {
	s.ID = strings.TrimSpace(s.ID)
	s.Name = strings.TrimSpace(s.Name)
	s.Provider = strings.TrimSpace(s.Provider)
	s.Protocol = strings.ToLower(strings.TrimSpace(s.Protocol))
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.Method = strings.ToUpper(strings.TrimSpace(s.Method))
	if s.IntervalSec <= 0 {
		s.IntervalSec = 60
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = 15
	}
	if s.Protocol == ProtocolHTTP {
		s.Stream = nil
		if s.Method == "" {
			s.Method = "GET"
		}
		if s.ExpectStatus == 0 {
			s.ExpectStatus = 200
		}
	}
}

// Validate 校验服务定义，返回首个错误。
func (s *Service) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("id 不能为空")
	}
	if s.Name == "" {
		return fmt.Errorf("服务 %q: name 不能为空", s.ID)
	}
	switch s.Protocol {
	case ProtocolChat, ProtocolResponse, ProtocolMessage, ProtocolHTTP:
	default:
		return fmt.Errorf("服务 %q: 不支持的协议 %q（支持 chat|response|message|http）", s.ID, s.Protocol)
	}
	if s.Protocol == ProtocolHTTP {
		if s.BaseURL == "" {
			return fmt.Errorf("服务 %q: http 协议需要 base_url", s.ID)
		}
		if s.ExpectStatus < 100 || s.ExpectStatus > 599 {
			return fmt.Errorf("服务 %q: expect_status 必须是合法 HTTP 状态码", s.ID)
		}
	} else {
		if s.BaseURL == "" {
			return fmt.Errorf("服务 %q: 需要 base_url", s.ID)
		}
		if s.Model == "" {
			return fmt.Errorf("服务 %q: %s 协议需要 model", s.ID, s.Protocol)
		}
	}
	if s.IntervalSec < 5 {
		return fmt.Errorf("服务 %q: interval_sec 不能小于 5", s.ID)
	}
	if s.TimeoutSec < 1 || s.TimeoutSec > 300 {
		return fmt.Errorf("服务 %q: timeout_sec 需在 1~300 之间", s.ID)
	}
	if s.SortOrder < 0 {
		return fmt.Errorf("服务 %q: sort_order 不能为负数", s.ID)
	}
	return nil
}

// ProbeResult 是一次探测的运行时结果。字段由调度器与状态 API 共用。
type ProbeResult struct {
	OK        bool   `json:"ok"`
	TS        int64  `json:"ts"`         // unix 秒
	LatencyMS int64  `json:"latency_ms"` // 端到端耗时；0 表示极快响应，不省略
	Error     string `json:"error,omitempty"`
}

// StatusChange 描述单个服务一次最终状态变化，不包含任何投递渠道语义。
type StatusChange struct {
	ServiceID         string  `json:"service_id"`
	SortOrder         int     `json:"sort_order,omitempty"`
	Model             string  `json:"model"`
	Provider          string  `json:"provider"`
	Protocol          string  `json:"protocol"`
	OK                bool    `json:"ok"`
	LatencyMS         int64   `json:"latency_ms"`
	Error             string  `json:"error,omitempty"`
	UptimePct         float64 `json:"uptime_pct"`
	Samples           int     `json:"samples"`
	PreviousStatus    string  `json:"previous_status"`
	Status            string  `json:"status"`
	LastTS            int64   `json:"last_ts"`
	OutageDurationSec int64   `json:"outage_duration_sec"`
	TodayUpSec        int64   `json:"today_up_sec"`
	TodayDownSec      int64   `json:"today_down_sec"`
	TodayDownCount    int     `json:"today_down_count"`
	TodayUptimePct    float64 `json:"today_uptime_pct"`
}

// StatusTransition 是与探测结果原子持久化的中性状态变化事件。
// AvailableAt 允许上层用持久化防抖窗口聚合相邻变化。
type StatusTransition struct {
	Change        StatusChange `json:"change"`
	ChangedAt     time.Time    `json:"changed_at"`
	AvailableAt   time.Time    `json:"available_at"`
	StatusPageURL string       `json:"status_page_url,omitempty"`
}

// TransitionBatch 是消费者一次领取到的稳定事件组。
type TransitionBatch struct {
	Key           string         `json:"key"`
	ChangedAt     time.Time      `json:"changed_at"`
	StatusPageURL string         `json:"status_page_url,omitempty"`
	Changes       []StatusChange `json:"changes"`
}

// PageConfig 是探针页的显示配置（"统计维度"开关所在）。
type PageConfig struct {
	Title        string `yaml:"title" json:"title"`
	Subtitle     string `yaml:"subtitle" json:"subtitle"`
	ProbeComment string `yaml:"probe_comment" json:"probe_comment"`
	PublicURL    string `yaml:"public_url" json:"public_url"`
	HistoryLen   int    `yaml:"history_len" json:"history_len"`
	RefreshSec   int    `yaml:"refresh_sec" json:"refresh_sec"`
	ShowUptime   bool   `yaml:"show_uptime" json:"show_uptime"`
	ShowSamples  bool   `yaml:"show_samples" json:"show_samples"`
	ShowLatency  bool   `yaml:"show_latency" json:"show_latency"`
	ShowAvgLoad  bool   `yaml:"show_avg_load" json:"show_avg_load"`
}

// Normalize 填充页面显示配置的默认值。
func (p *PageConfig) Normalize() {
	p.PublicURL = strings.TrimSpace(p.PublicURL)
	if p.Title == "" {
		p.Title = "model-uptime // status"
	}
	if p.Subtitle == "" {
		p.Subtitle = "model-uptime"
	}
	if p.ProbeComment == "" {
		p.ProbeComment = "model-uptime service monitor · probing every 60s"
	}
	if p.HistoryLen <= 0 {
		p.HistoryLen = 60
	}
	if p.RefreshSec <= 0 {
		p.RefreshSec = 5
	}
	if !p.ShowUptime && !p.ShowSamples && !p.ShowLatency && !p.ShowAvgLoad {
		// 全关会导致页面无统计维度，回退到全开
		p.ShowUptime, p.ShowSamples, p.ShowLatency, p.ShowAvgLoad = true, true, true, true
	}
}

// Validate 校验页面配置。
func (p *PageConfig) Validate() error {
	if p.HistoryLen < 1 || p.HistoryLen > 200 {
		return fmt.Errorf("history_len 需在 1~200 之间")
	}
	if p.RefreshSec < 1 || p.RefreshSec > 60 {
		return fmt.Errorf("refresh_sec 需在 1~60 之间")
	}
	if p.PublicURL != "" {
		parsed, err := url.Parse(p.PublicURL)
		if err != nil || parsed == nil {
			return fmt.Errorf("public_url 必须是无账号密码的完整 http/https 地址")
		}
		scheme := strings.ToLower(parsed.Scheme)
		if parsed.Host == "" || (scheme != "http" && scheme != "https") || parsed.User != nil {
			return fmt.Errorf("public_url 必须是无账号密码的完整 http/https 地址")
		}
	}
	return nil
}

// PauseSpan 表示一段暂停区间，用于状态页显式渲染禁用空档。
// 区间在运行时由 monitor 记录，不持久化：重启后历史样本本身已表达那段时间的状态。
type PauseSpan struct {
	From int64 `json:"from"` // 暂停起始 unix 秒
	To   int64 `json:"to"`   // 恢复 unix 秒（闭区间右边界）
}

// ServiceView 是状态 API 中单个服务的表示，保持稳定的公开状态 API 结构。
type ServiceView struct {
	ID          string        `json:"id"`
	Model       string        `json:"model"`
	Provider    string        `json:"provider,omitempty"`
	IntervalSec int           `json:"interval_sec"`
	UptimePct   float64       `json:"uptime_pct"`
	Last        *ProbeResult  `json:"last"`
	History     []ProbeResult `json:"history"`
	Pauses      []PauseSpan   `json:"pauses,omitempty"` // 运行时记录的暂停区间
}

// StatusResponse 是 /api/status 的响应体，结构保持稳定的公开状态 API 结构，
// 额外携带 page 显示配置供前端渲染。
type StatusResponse struct {
	GeneratedAt int64         `json:"generated_at"` // unix 秒
	AllOK       bool          `json:"all_ok"`
	Page        *PageConfig   `json:"page,omitempty"`
	Services    []ServiceView `json:"services"`
}
