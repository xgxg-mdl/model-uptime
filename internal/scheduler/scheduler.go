// Package scheduler 负责按间隔调度并发探测、维护每服务的历史窗口，
// 并向状态 API 提供聚合快照。
//
// 设计要点：
//   - 1s 级 ticker 检查各服务是否到期，到期即启动独立 goroutine 探测，
//     服务之间互不阻塞；
//   - lastProbe 在探测启动前更新，防止探测耗时长于间隔时重复触发；
//   - Reload 热重载配置时按 id 保留历史，新建服务从 SQLite 恢复历史。
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/prober"
	"github.com/lefachao/model-uptime/internal/store"
)

// tickInterval 调度器的主循环精度。间隔远小于最小探测间隔(5s)，开销可忽略。
const tickInterval = time.Second

// purgeInterval / retention 历史清理：SQLite 仅用于重启恢复，无需长期保留。
const (
	purgeInterval = time.Hour
	retention     = 30 * 24 * time.Hour
)

// ServiceState 是调度器维护的某个服务的运行时状态。
type ServiceState struct {
	svc       model.Service
	last      *model.ProbeResult
	history   []model.ProbeResult // 升序，最新在末尾
	lastProbe time.Time
}

// Scheduler 调度并聚合探测。
type Scheduler struct {
	mu      sync.RWMutex
	states  map[string]*ServiceState
	order   []string // 保持配置顺序，供状态页稳定输出
	page    model.PageConfig
	store   *store.Store
	probeFn func(context.Context, *model.Service) prober.Result
	logger  *slog.Logger
	done    chan struct{}
	wg      sync.WaitGroup
}

// New 创建调度器。store 可为 nil（纯内存运行），logger 可为 nil。
func New(st *store.Store, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		states:  make(map[string]*ServiceState),
		store:   st,
		probeFn: prober.Probe,
		logger:  logger,
		done:    make(chan struct{}),
	}
}

// Start 启动后台调度循环。
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.run()
}

// Stop 停止调度循环并等待在途探测结束。
func (s *Scheduler) Stop() {
	close(s.done)
	s.wg.Wait()
}

func (s *Scheduler) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	lastPurge := time.Now()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.checkDue()
			if time.Since(lastPurge) >= purgeInterval {
				s.purge()
				lastPurge = time.Now()
			}
		}
	}
}

// Reload 热重载服务列表与页面配置：按 id 保留历史与 last 状态。
func (s *Scheduler) Reload(services []model.Service, page model.PageConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.page = page

	next := make(map[string]*ServiceState, len(services))
	order := make([]string, 0, len(services))
	ctx := context.Background()
	for i := range services {
		svc := services[i]
		st, ok := s.states[svc.ID]
		if !ok {
			st = &ServiceState{svc: svc}
			// 新建服务：从持久化恢复历史，避免重启后状态条清零
			if s.store != nil {
				if hist, err := s.store.LoadHistory(ctx, svc.ID, page.HistoryLen); err != nil {
					s.logger.Warn("恢复历史失败", "svc", svc.ID, "err", err)
				} else if len(hist) > 0 {
					st.history = hist
					last := hist[len(hist)-1]
					st.last = &last
				}
			}
		} else {
			st.svc = svc // 配置更新，历史保留
		}
		next[svc.ID] = st
		order = append(order, svc.ID)
	}
	s.states = next
	s.order = order
}

// checkDue 找出到期服务并启动探测。同步锁内标记 lastProbe，避免重复触发。
func (s *Scheduler) checkDue() {
	now := time.Now()
	s.mu.Lock()
	var due []*ServiceState
	for _, st := range s.states {
		if !st.svc.IsEnabled() {
			continue
		}
		if now.Sub(st.lastProbe) >= time.Duration(st.svc.IntervalSec)*time.Second {
			st.lastProbe = now
			due = append(due, st)
		}
	}
	s.mu.Unlock()

	for _, st := range due {
		go s.probe(st)
	}
}

// probe 执行一次探测并把结果写入内存历史与 SQLite。
func (s *Scheduler) probe(st *ServiceState) {
	svc := st.svc // 快照配置，探测期间配置变更不影响本次请求
	res := s.probeFn(context.Background(), &svc)
	r := model.ProbeResult{
		OK:        res.OK,
		TS:        time.Now().Unix(),
		LatencyMS: res.LatencyMS,
		Error:     res.Error,
	}
	s.record(svc.ID, r)
}

// record 更新内存状态并异步持久化。
func (s *Scheduler) record(id string, r model.ProbeResult) {
	s.mu.Lock()
	st, ok := s.states[id]
	if ok {
		st.history = append(st.history, r)
		if n := s.page.HistoryLen; len(st.history) > n {
			st.history = st.history[len(st.history)-n:]
		}
		st.last = &r
	}
	s.mu.Unlock()

	if ok && s.store != nil {
		go func() {
			if err := s.store.AppendResult(context.Background(), id, r); err != nil {
				s.logger.Warn("持久化探测结果失败", "svc", id, "err", err)
			}
		}()
	}
}

// ProbeNow 立即探测指定服务（配置页"测试连接"用），结果也计入历史。
func (s *Scheduler) ProbeNow(id string) (*model.ProbeResult, error) {
	s.mu.RLock()
	st, ok := s.states[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("服务不存在: %s", id)
	}
	svc := st.svc
	res := s.probeFn(context.Background(), &svc)
	r := model.ProbeResult{
		OK:        res.OK,
		TS:        time.Now().Unix(),
		LatencyMS: res.LatencyMS,
		Error:     res.Error,
	}
	s.record(id, r)
	return &r, nil
}

// Snapshot 生成 /api/status 所需的聚合快照。
func (s *Scheduler) Snapshot() model.StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	page := s.page
	resp := model.StatusResponse{
		GeneratedAt: time.Now().Unix(),
		AllOK:       true,
		Page:        &page,
		Services:    make([]model.ServiceView, 0, len(s.order)),
	}
	for _, id := range s.order {
		st, ok := s.states[id]
		if !ok || !st.svc.IsEnabled() {
			continue
		}
		view := model.ServiceView{
			Model:     st.svc.Name,
			Provider:  st.svc.Provider,
			UptimePct: uptimePct(st.history),
			Last:      st.last,
			History:   append([]model.ProbeResult(nil), st.history...),
		}
		resp.Services = append(resp.Services, view)
		if st.last != nil && !st.last.OK {
			resp.AllOK = false
		}
	}
	return resp
}

// uptimePct 计算历史窗口内的可用率百分比。空窗口（pending）按 100 处理。
func uptimePct(history []model.ProbeResult) float64 {
	if len(history) == 0 {
		return 100.0
	}
	ok := 0
	for _, r := range history {
		if r.OK {
			ok++
		}
	}
	return float64(ok) / float64(len(history)) * 100.0
}

// purge 清理超过保留期的历史记录。
func (s *Scheduler) purge() {
	if s.store == nil {
		return
	}
	if n, err := s.store.PurgeBefore(context.Background(), time.Now().Add(-retention)); err != nil {
		s.logger.Warn("清理历史失败", "err", err)
	} else if n > 0 {
		s.logger.Debug("清理历史", "rows", n)
	}
}
