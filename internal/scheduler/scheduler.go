// Package scheduler 负责按间隔调度并发探测、维护每服务的历史窗口，
// 并向状态 API 提供聚合快照。
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

const tickInterval = time.Second

const (
	purgeInterval = time.Hour
	retention     = 30 * 24 * time.Hour
)

// ServiceState 是调度器维护的某个服务的运行时状态。generation 区分同一 ID 的观测生命周期。
type ServiceState struct {
	svc        model.Service
	last       *model.ProbeResult
	history    []model.ProbeResult
	lastProbe  time.Time
	generation uint64
}

type probeJob struct {
	svc        model.Service
	generation uint64
}

// Scheduler 调度并聚合探测。
type Scheduler struct {
	mu             sync.RWMutex
	states         map[string]*ServiceState
	order          []string
	page           model.PageConfig
	store          *store.Store
	probeFn        func(context.Context, *model.Service) prober.Result
	logger         *slog.Logger
	nextGeneration uint64
	done           chan struct{}
	wg             sync.WaitGroup
}

func New(st *store.Store, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{states: make(map[string]*ServiceState), store: st, probeFn: prober.Probe, logger: logger, done: make(chan struct{})}
}

func (s *Scheduler) Start() { s.wg.Add(1); go s.run() }
func (s *Scheduler) Stop()  { close(s.done); s.wg.Wait() }

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

// Reload 保留普通同 ID 更新的历史；禁用后重新启用、删除服务均开启新的观测生命周期。
func (s *Scheduler) Reload(services []model.Service, page model.PageConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.page = page

	next := make(map[string]*ServiceState, len(services))
	order := make([]string, 0, len(services))
	seen := make(map[string]bool, len(services))
	for i := range services {
		svc := services[i]
		seen[svc.ID] = true
		st, exists := s.states[svc.ID]
		if !exists {
			st = s.newStateLocked(svc, page)
		} else if !st.svc.IsEnabled() && svc.IsEnabled() {
			// 停用期的结果不能与新的观测窗口混合。
			s.resetStateLocked(st, svc)
		} else {
			st.svc = svc
		}
		next[svc.ID] = st
		order = append(order, svc.ID)
	}
	for id := range s.states {
		if !seen[id] {
			s.deleteHistoryLocked(id)
		}
	}
	s.states, s.order = next, order
}

func (s *Scheduler) newStateLocked(svc model.Service, page model.PageConfig) *ServiceState {
	s.nextGeneration++
	st := &ServiceState{svc: svc, generation: s.nextGeneration}
	if s.store == nil {
		return st
	}
	hist, err := s.store.LoadHistory(context.Background(), svc.ID, page.HistoryLen)
	if err != nil {
		s.logger.Warn("恢复历史失败", "svc", svc.ID, "err", err)
		return st
	}
	if len(hist) > 0 {
		st.history = hist
		last := hist[len(hist)-1]
		st.last = &last
	}
	return st
}

func (s *Scheduler) resetStateLocked(st *ServiceState, svc model.Service) {
	s.nextGeneration++
	st.svc, st.last, st.history, st.lastProbe, st.generation = svc, nil, nil, time.Time{}, s.nextGeneration
	s.deleteHistoryLocked(svc.ID)
}

func (s *Scheduler) deleteHistoryLocked(id string) {
	if s.store == nil {
		return
	}
	if _, err := s.store.DeleteHistory(context.Background(), id); err != nil {
		s.logger.Warn("删除服务历史失败", "svc", id, "err", err)
	}
}

func (s *Scheduler) checkDue() {
	now := time.Now()
	s.mu.Lock()
	var due []probeJob
	for _, st := range s.states {
		if !st.svc.IsEnabled() || now.Sub(st.lastProbe) < time.Duration(st.svc.IntervalSec)*time.Second {
			continue
		}
		st.lastProbe = now
		due = append(due, probeJob{svc: st.svc, generation: st.generation})
	}
	s.mu.Unlock()
	for _, job := range due {
		go s.probe(job)
	}
}

func (s *Scheduler) probe(job probeJob) {
	res := s.probeFn(context.Background(), &job.svc)
	s.recordGeneration(job.svc.ID, job.generation, model.ProbeResult{OK: res.OK, TS: time.Now().Unix(), LatencyMS: res.LatencyMS, Error: res.Error})
}

// record 为包内测试与同步调用保留的快捷入口。
func (s *Scheduler) record(id string, r model.ProbeResult) {
	s.mu.RLock()
	st := s.states[id]
	var generation uint64
	if st != nil {
		generation = st.generation
	}
	s.mu.RUnlock()
	if generation != 0 {
		s.recordGeneration(id, generation, r)
	}
}

// recordGeneration 只有在服务仍处于启动该探测时的生命周期且已启用时才接受结果。
// 持久化在相同互斥边界内完成，避免删除历史后旧异步写入重新污染数据库。
func (s *Scheduler) recordGeneration(id string, generation uint64, r model.ProbeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	if !ok || !st.svc.IsEnabled() || st.generation != generation {
		return
	}
	st.history = append(st.history, r)
	if n := s.page.HistoryLen; len(st.history) > n {
		st.history = st.history[len(st.history)-n:]
	}
	st.last = &r
	if s.store != nil {
		if err := s.store.AppendResult(context.Background(), id, r); err != nil {
			s.logger.Warn("持久化探测结果失败", "svc", id, "err", err)
		}
	}
}

func (s *Scheduler) ProbeNow(id string) (*model.ProbeResult, error) {
	s.mu.RLock()
	st := s.states[id]
	if st == nil {
		s.mu.RUnlock()
		return nil, fmt.Errorf("服务不存在: %s", id)
	}
	job := probeJob{svc: st.svc, generation: st.generation}
	s.mu.RUnlock()
	res := s.probeFn(context.Background(), &job.svc)
	r := model.ProbeResult{OK: res.OK, TS: time.Now().Unix(), LatencyMS: res.LatencyMS, Error: res.Error}
	s.recordGeneration(id, job.generation, r)
	return &r, nil
}

func (s *Scheduler) Snapshot() model.StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	page := s.page
	resp := model.StatusResponse{GeneratedAt: time.Now().Unix(), AllOK: true, Page: &page, Services: make([]model.ServiceView, 0, len(s.order))}
	for _, id := range s.order {
		st := s.states[id]
		if st == nil || !st.svc.IsEnabled() {
			continue
		}
		view := model.ServiceView{Model: st.svc.Name, Provider: st.svc.Provider, IntervalSec: st.svc.IntervalSec, UptimePct: uptimePct(st.history), Last: st.last, History: append([]model.ProbeResult(nil), st.history...)}
		resp.Services = append(resp.Services, view)
		if st.last != nil && !st.last.OK {
			resp.AllOK = false
		}
	}
	return resp
}

func uptimePct(history []model.ProbeResult) float64 {
	if len(history) == 0 {
		return 100
	}
	ok := 0
	for _, r := range history {
		if r.OK {
			ok++
		}
	}
	return float64(ok) / float64(len(history)) * 100
}

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
