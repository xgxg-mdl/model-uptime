// Package model 定义领域模型：监控目标、探测结果、页面显示配置。
package model

import (
	"crypto/rand"
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
	UID         string `yaml:"uid" json:"uid"`                               // 内部稳定标识，不随 model 修改
	LegacyID    string `yaml:"id,omitempty" json:"-"`                        // 仅用于读取旧配置；写回时省略
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
	WarningSec  int    `yaml:"warning_sec,omitempty" json:"warning_sec,omitempty"`
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

// MarshalYAML 不再写回旧 id 字段；其值只用于把旧配置迁移到 uid。
func (s Service) MarshalYAML() (any, error) {
	type serviceYAML Service
	s.LegacyID = ""
	return serviceYAML(s), nil
}

// IsEnabled 返回服务是否启用（未显式配置时默认启用）。
func (s *Service) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// IsStreaming 返回 LLM 探针是否走流式 SSE（未显式配置时默认流式）。
func (s *Service) IsStreaming() bool {
	return s.Protocol != ProtocolHTTP && (s.Stream == nil || *s.Stream)
}

// Normalize 填充默认值并清理字段。
func (s *Service) Normalize() {
	s.UID = strings.TrimSpace(s.UID)
	s.LegacyID = strings.TrimSpace(s.LegacyID)
	s.Name = strings.TrimSpace(s.Name)
	s.Provider = strings.TrimSpace(s.Provider)
	s.Protocol = strings.ToLower(strings.TrimSpace(s.Protocol))
	s.Model = strings.TrimSpace(s.Model)
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	s.Method = strings.ToUpper(strings.TrimSpace(s.Method))
	if s.UID == "" {
		if s.LegacyID != "" {
			s.UID = s.LegacyID
		} else {
			s.UID = newServiceUID()
		}
	}
	// 旧 HTTP 配置没有 model；原 id 是其用户可见标识，可无损迁移为 model。
	if s.Model == "" && s.Protocol == ProtocolHTTP && s.LegacyID != "" {
		s.Model = s.LegacyID
	}
	if s.IntervalSec <= 0 {
		s.IntervalSec = 60
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = 60
	}
	if s.WarningSec <= 0 {
		s.WarningSec = 30
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

func newServiceUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		// crypto/rand 失败意味着运行环境已无法安全生成身份，继续运行会制造重复关联。
		panic(fmt.Sprintf("生成服务 uid 失败: %v", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// Validate 校验服务定义，返回首个错误。
func (s *Service) Validate() error {
	if s.UID == "" {
		return fmt.Errorf("uid 不能为空")
	}
	if s.Model == "" {
		return fmt.Errorf("服务 %q: model 不能为空", s.UID)
	}
	if s.Name == "" {
		return fmt.Errorf("服务 %q: name 不能为空", s.Model)
	}
	switch s.Protocol {
	case ProtocolChat, ProtocolResponse, ProtocolMessage, ProtocolHTTP:
	default:
		return fmt.Errorf("服务 %q: 不支持的协议 %q（支持 chat|response|message|http）", s.Model, s.Protocol)
	}
	if s.Protocol == ProtocolHTTP {
		if s.BaseURL == "" {
			return fmt.Errorf("服务 %q: http 协议需要 base_url", s.Model)
		}
		if s.ExpectStatus < 100 || s.ExpectStatus > 599 {
			return fmt.Errorf("服务 %q: expect_status 必须是合法 HTTP 状态码", s.Model)
		}
	} else {
		if s.BaseURL == "" {
			return fmt.Errorf("服务 %q: 需要 base_url", s.Model)
		}
	}
	if s.IntervalSec < 5 {
		return fmt.Errorf("服务 %q: interval_sec 不能小于 5", s.Model)
	}
	if s.TimeoutSec < 1 || s.TimeoutSec > 300 {
		return fmt.Errorf("服务 %q: timeout_sec 需在 1~300 之间", s.Model)
	}
	if s.WarningSec < 1 || s.WarningSec > 300 {
		return fmt.Errorf("服务 %q: warning_sec 需在 1~300 之间", s.Model)
	}
	if s.SortOrder < 0 {
		return fmt.Errorf("服务 %q: sort_order 不能为负数", s.Model)
	}
	return nil
}

// ProbeResult 是一次探测的运行时结果。字段由调度器与状态 API 共用。
type ProbeResult struct {
	OK        bool   `json:"ok"`
	TS        int64  `json:"ts"`                   // 探测完成时间，unix 秒
	StartedAt int64  `json:"started_at,omitempty"` // 探测开始时间；旧记录为 0 时回退到 TS
	LatencyMS int64  `json:"latency_ms"`           // 端到端耗时；0 表示极快响应，不省略
	Error     string `json:"error,omitempty"`
}

// StatusChange 描述单个服务一次最终状态变化，不包含任何投递渠道语义。
type StatusChange struct {
	ServiceUID        string  `json:"service_uid"`
	LegacyServiceID   string  `json:"service_id,omitempty"` // 仅用于读取升级前持久化的通知 payload
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
	// 指针用于区分旧配置未声明（默认开启）与用户显式关闭。
	EnableCommandAnimation *bool `yaml:"enable_command_animation,omitempty" json:"enable_command_animation"`
	ShowUptime             bool  `yaml:"show_uptime" json:"show_uptime"`
	ShowSamples            bool  `yaml:"show_samples" json:"show_samples"`
	ShowLatency            bool  `yaml:"show_latency" json:"show_latency"`
	ShowAvgLoad            bool  `yaml:"show_avg_load" json:"show_avg_load"`
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
		p.ProbeComment = "model-uptime · service health and performance"
	}
	if p.HistoryLen <= 0 {
		p.HistoryLen = 60
	}
	if p.RefreshSec <= 0 {
		p.RefreshSec = 5
	}
	if p.EnableCommandAnimation == nil {
		enabled := true
		p.EnableCommandAnimation = &enabled
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

// PauseSpan 表示一段半开暂停区间，用于状态页显式渲染禁用空档。
// 区间在运行时由 monitor 记录，不持久化：重启后历史样本本身已表达那段时间的状态。
type PauseSpan struct {
	From int64 `json:"from"` // 暂停起始 unix 秒
	To   int64 `json:"to"`   // 恢复 unix 秒（半开区间右边界）；0 表示仍在暂停
}

// StatusTimelineState 是完整观测时间桶的权威状态。
type StatusTimelineState string

const (
	StatusTimelineHealthy    StatusTimelineState = "healthy"
	StatusTimelineSlow       StatusTimelineState = "slow"
	StatusTimelineFailing    StatusTimelineState = "failing"
	StatusTimelineProbing    StatusTimelineState = "probing"
	StatusTimelinePaused     StatusTimelineState = "paused"
	StatusTimelineUnobserved StatusTimelineState = "unobserved"
	StatusTimelineNotStarted StatusTimelineState = "not-started"
)

// StatusTimelineSlot 是一个服务在完整 interval 内的聚合观测投影。
type StatusTimelineSlot struct {
	StartTS          int64               `json:"start_ts"`
	EndTS            int64               `json:"end_ts"`
	Status           StatusTimelineState `json:"status"`
	ObservationCount int                 `json:"observation_count"`
	Result           *ProbeResult        `json:"result,omitempty"`
	ProbeStartedAt   int64               `json:"probe_started_at,omitempty"`
}

// ServiceView 是状态 API 中单个服务的表示，保持稳定的公开状态 API 结构。
type ServiceView struct {
	ServiceUID     string               `json:"-"`
	Name           string               `json:"name"`
	Model          string               `json:"model"`
	Provider       string               `json:"provider,omitempty"`
	SortOrder      int                  `json:"-"`
	IntervalSec    int                  `json:"interval_sec"`
	WarningSec     int                  `json:"warning_sec"`
	ObservedSince  int64                `json:"observed_since,omitempty"`
	ProbeStartedAt int64                `json:"current_probe_started_at,omitempty"`
	UptimePct      float64              `json:"uptime_pct"`
	Timeline       []StatusTimelineSlot `json:"timeline"`
	Last           *ProbeResult         `json:"last"`
	History        []ProbeResult        `json:"history"`
	Pauses         []PauseSpan          `json:"pauses,omitempty"` // 运行时记录的暂停区间
}

// ServiceViewLess 定义公开页面统一使用的模型顺序：显式 order 优先，
// 未设置的 order 置底，同 order 再以展示名和模型 ID 消除来源切片差异。
func ServiceViewLess(left, right ServiceView) bool {
	if left.SortOrder != right.SortOrder {
		if left.SortOrder <= 0 {
			return false
		}
		if right.SortOrder <= 0 {
			return true
		}
		return left.SortOrder < right.SortOrder
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.Model < right.Model
}

// StatusResponse 是 /api/status 的监控数据响应体。
type StatusResponse struct {
	GeneratedAt int64         `json:"generated_at"` // unix 秒
	AllOK       bool          `json:"all_ok"`
	Services    []ServiceView `json:"services"`
}
