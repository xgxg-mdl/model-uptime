// Package monitor 负责按间隔调度并发探测、维护每服务的历史窗口，
// 并向状态 API 提供聚合快照。
package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor/probe"
)

const defaultMaxConcurrentProbe = 8

// serviceState 是调度器维护的某个服务的运行时状态。generation 区分同一 ID 的观测生命周期。
// pauses 记录运行时禁用区间，用于状态页显式渲染暂停空档；不持久化。
type serviceState struct {
	svc           model.Service
	last          *model.ProbeResult
	history       []model.ProbeResult
	observedSince int64
	lastProbe     time.Time
	generation    uint64
	pauses        []model.PauseSpan
	flight        *probeFlight
}

type probeFlight struct {
	done    chan struct{}
	result  *model.ProbeResult
	err     error
	waiters int
	cancel  context.CancelFunc
}

type probeJob struct {
	svc        model.Service
	generation uint64
	flight     *probeFlight
	ctx        context.Context
	manual     bool
}

// Options 控制调度器内部资源上限。零值使用适合单机部署的安全默认值。
type Options struct {
	MaxConcurrentProbes int
}

// Repository 是调度器消费的持久化接缝；SQLite adapter 由 app 负责注入。
type Repository interface {
	LoadHistory(context.Context, string, int) ([]model.ProbeResult, error)
	LoadResultsStartedBetween(context.Context, string, int64, int64) ([]model.ProbeResult, error)
	LoadObservationStart(context.Context, string) (int64, error)
	LoadResultsSinceWithPrevious(context.Context, string, int64, int64) ([]model.ProbeResult, error)
	LoadFailureStart(context.Context, string, int64) (int64, error)
	RecordProbeResult(context.Context, string, model.ProbeResult, *model.StatusTransition) error
	DeleteHistories(context.Context, []string) (int64, error)
	PurgeBefore(context.Context, time.Time) (int64, error)
}

// Scheduler 调度并聚合探测。
type Scheduler struct {
	mu             sync.RWMutex
	reloadGate     sync.RWMutex
	reloadMu       sync.Mutex
	storeMu        sync.Mutex
	states         map[string]*serviceState
	activeFlights  map[*probeFlight]struct{}
	order          []string
	page           model.PageConfig
	store          Repository
	probeFn        func(context.Context, *model.Service) probe.Result
	logger         *slog.Logger
	nextGeneration uint64
	probeSlots     chan struct{}
	lifecycleMu    sync.Mutex
	lifecycle      lifecycleState
	rootCtx        context.Context
	cancel         context.CancelFunc
	stopped        chan struct{}
	finalizeOnce   sync.Once
	runWG          sync.WaitGroup
	operationWG    sync.WaitGroup
	wg             sync.WaitGroup
}

func New(st Repository, logger *slog.Logger, options ...Options) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	maxConcurrent := defaultMaxConcurrentProbe
	if len(options) > 0 && options[0].MaxConcurrentProbes > 0 {
		maxConcurrent = options[0].MaxConcurrentProbes
	}
	return &Scheduler{
		states: make(map[string]*serviceState), activeFlights: make(map[*probeFlight]struct{}),
		store: st, probeFn: probe.Probe, logger: logger,
		probeSlots: make(chan struct{}, maxConcurrent), stopped: make(chan struct{}),
	}
}
