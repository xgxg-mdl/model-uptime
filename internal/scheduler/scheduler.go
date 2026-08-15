// Package scheduler 负责按间隔调度并发探测、维护每服务的历史窗口，
// 并向状态 API 提供聚合快照。
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
	"github.com/lefachao/model-uptime/internal/prober"
	"github.com/lefachao/model-uptime/internal/store"
)

const tickInterval = time.Second

const (
	purgeInterval = time.Hour
	retention     = 30 * 24 * time.Hour
)

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// ServiceState 是调度器维护的某个服务的运行时状态。generation 区分同一 ID 的观测生命周期。
// pauses 记录运行时禁用区间，用于状态页显式渲染暂停空档；不持久化。
type ServiceState struct {
	svc        model.Service
	last       *model.ProbeResult
	history    []model.ProbeResult
	lastProbe  time.Time
	generation uint64
	pauses     []model.PauseSpan
}

type probeJob struct {
	svc        model.Service
	generation uint64
	cycleID    uint64
}

type probeCycle struct {
	remaining int
	changes   map[string]notifier.Change
}

type batchNotifier interface {
	Notify(notifier.Batch) error
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
	nextCycleID    uint64
	cycles         map[uint64]*probeCycle
	notifier       batchNotifier
	manualChanges  map[string]notifier.Change
	manualTimer    *time.Timer
	manualDebounce time.Duration
	done           chan struct{}
	wg             sync.WaitGroup
}

func New(st *store.Store, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		states: make(map[string]*ServiceState), cycles: make(map[uint64]*probeCycle),
		manualChanges: make(map[string]notifier.Change), store: st, probeFn: prober.Probe,
		logger: logger, manualDebounce: 3 * time.Second, done: make(chan struct{}),
	}
}

func (s *Scheduler) Start() { s.wg.Add(1); go s.run() }
func (s *Scheduler) Stop() {
	close(s.done)
	s.wg.Wait()
	s.flushManualChanges()
}

// SetNotifier 设置状态变更接收器。nil 表示禁用通知。
func (s *Scheduler) SetNotifier(n batchNotifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifier = n
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

// Reload 保留同 ID 服务的观测历史；启用状态切换被视为暂停或恢复，而非新生命周期，
// 因此 last/history 与持久化记录始终保留。仅当服务从配置中移除时才终止观测并删除其历史。
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
		} else {
			prevEnabled, nextEnabled := st.svc.IsEnabled(), svc.IsEnabled()
			switch {
			case prevEnabled && !nextEnabled:
				// 暂停：推进 generation 使在飞探测失效，但保留历史与持久化记录。
				s.pauseStateLocked(st, svc)
			case !prevEnabled && nextEnabled:
				// 恢复：再次推进 generation 隔离停用期的在飞探测，并尽快重新调度。
				s.resumeStateLocked(st, svc)
			default:
				if !reflect.DeepEqual(st.svc, svc) {
					// 任意服务定义变化都使在途结果失效，避免旧端点的结果使用
					// 新名称、Provider 或模型信息写入并触发错误归因的通知。
					s.advanceGenerationLocked(st)
					st.lastProbe = time.Time{}
				}
				st.svc = svc
			}
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

// advanceGenerationLocked 递增 generation 并应用到状态，使此前启动的在飞探测无法写回。
func (s *Scheduler) advanceGenerationLocked(st *ServiceState) {
	s.nextGeneration++
	st.generation = s.nextGeneration
}

// pauseStateLocked 切换到禁用：推进 generation 失效在飞探测，记录暂停起点，
// 但保留历史与持久化记录。
func (s *Scheduler) pauseStateLocked(st *ServiceState, svc model.Service) {
	s.advanceGenerationLocked(st)
	st.svc = svc
	st.pauses = append(st.pauses, model.PauseSpan{From: time.Now().Unix()})
}

// resumeStateLocked 切换到重新启用：再次推进 generation 隔离停用期的在飞探测，
// 关闭上一个未闭合的暂停区间，清零 lastProbe 使下一次调度立即触发，
// 历史与持久化记录保持不变。
func (s *Scheduler) resumeStateLocked(st *ServiceState, svc model.Service) {
	s.advanceGenerationLocked(st)
	st.svc = svc
	st.lastProbe = time.Time{}
	now := time.Now().Unix()
	// 关闭最后一个未闭合的暂停区间（To == 0）。
	for i := len(st.pauses) - 1; i >= 0; i-- {
		if st.pauses[i].To == 0 {
			st.pauses[i].To = now
			break
		}
	}
}

// deleteHistoryLocked 删除服务从配置中移除时的全部持久化历史。
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
	if len(due) > 0 {
		// 同一次 tick 选出的 due 服务构成通知聚合边界。remaining 统计所有任务，
		// 包括随后因 reload 失效的任务，保证慢任务结束后批次仍能确定关闭。
		s.nextCycleID++
		cycleID := s.nextCycleID
		s.cycles[cycleID] = &probeCycle{remaining: len(due), changes: make(map[string]notifier.Change)}
		for i := range due {
			due[i].cycleID = cycleID
		}
	}
	s.mu.Unlock()
	for _, job := range due {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.probe(job)
		}()
	}
}

func (s *Scheduler) probe(job probeJob) {
	res := s.probeFn(context.Background(), &job.svc)
	change := s.recordGeneration(job.svc.ID, job.generation, model.ProbeResult{OK: res.OK, TS: time.Now().Unix(), LatencyMS: res.LatencyMS, Error: res.Error})
	s.completeCycle(job.cycleID, change)
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
		_ = s.recordGeneration(id, generation, r)
	}
}

// recordGeneration 只有在服务仍处于启动该探测时的生命周期且已启用时才接受结果。
// 持久化在相同互斥边界内完成，避免删除历史后旧异步写入重新污染数据库。
func (s *Scheduler) recordGeneration(id string, generation uint64, r model.ProbeResult) *notifier.Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	if !ok || !st.svc.IsEnabled() || st.generation != generation {
		return nil
	}
	previous := st.last
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
	if previous == nil || previous.OK == r.OK {
		return nil
	}
	previousStatus, status := "down", "up"
	if previous.OK {
		previousStatus, status = "up", "down"
	}
	statsHistory := st.history
	dayStart := beijingDayStart(r.TS)
	if s.store != nil {
		persisted, err := s.store.LoadResultsSinceWithPrevious(context.Background(), id, dayStart, r.TS)
		if err != nil {
			s.logger.Warn("查询通知今日统计失败", "svc", id, "err", err)
		} else {
			statsHistory = persisted
		}
	}
	today := calculateDailyStats(statsHistory, dayStart, r.TS)
	outageDuration := int64(0)
	if status == "up" {
		failureStart := failureStartFromResults(statsHistory, r.TS)
		if s.store != nil {
			persistedStart, err := s.store.LoadFailureStart(context.Background(), id, r.TS)
			if err != nil {
				s.logger.Warn("查询通知异常持续时间失败", "svc", id, "err", err)
			} else if persistedStart > 0 {
				failureStart = persistedStart
			}
		}
		if failureStart > 0 && failureStart < r.TS {
			outageDuration = r.TS - failureStart
		}
	}
	modelName := st.svc.Model
	if modelName == "" {
		modelName = st.svc.Name
	}
	return &notifier.Change{
		ServiceID: id, Model: modelName, Provider: st.svc.Provider, Protocol: st.svc.Protocol,
		OK: r.OK, LatencyMS: r.LatencyMS, Error: r.Error, UptimePct: uptimePct(st.history),
		Samples: len(st.history), PreviousStatus: previousStatus, Status: status, LastTS: r.TS,
		OutageDurationSec: outageDuration, TodayUpSec: today.upSec, TodayDownSec: today.downSec,
		TodayDownCount: today.downCount, TodayUptimePct: today.uptimePct,
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
	if change := s.recordGeneration(id, job.generation, r); change != nil {
		s.queueManualChange(*change)
	}
	return &r, nil
}

// completeCycle 在本轮每个探测结束时调用；最后一个任务负责把净变化整批提交。
func (s *Scheduler) completeCycle(cycleID uint64, change *notifier.Change) {
	if cycleID == 0 {
		return
	}
	s.mu.Lock()
	cycle := s.cycles[cycleID]
	if cycle == nil {
		s.mu.Unlock()
		return
	}
	if change != nil {
		cycle.changes[change.ServiceID] = *change
	}
	cycle.remaining--
	if cycle.remaining > 0 {
		s.mu.Unlock()
		return
	}
	delete(s.cycles, cycleID)
	changes := changeValues(cycle.changes)
	n := s.notifier
	s.mu.Unlock()
	s.notify(n, changes)
}

// queueManualChange 为不属于调度轮次的 ProbeNow 使用防抖窗口，避免连续手动探测
// 将多个模型的变化拆成多条 Telegram 消息。
func (s *Scheduler) queueManualChange(change notifier.Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.manualChanges[change.ServiceID]; exists {
		// 防抖窗口按首次旧状态与最终新状态判断净变化，up → down → up
		// 不应被误报为一次恢复。
		change.PreviousStatus = previous.PreviousStatus
	}
	s.manualChanges[change.ServiceID] = change
	if s.manualTimer != nil {
		s.manualTimer.Stop()
	}
	s.manualTimer = time.AfterFunc(s.manualDebounce, s.flushManualChanges)
}

func (s *Scheduler) flushManualChanges() {
	s.mu.Lock()
	changes := changeValues(s.manualChanges)
	s.manualChanges = make(map[string]notifier.Change)
	s.manualTimer = nil
	n := s.notifier
	s.mu.Unlock()
	s.notify(n, changes)
}

func changeValues(changes map[string]notifier.Change) []notifier.Change {
	out := make([]notifier.Change, 0, len(changes))
	for _, change := range changes {
		out = append(out, change)
	}
	return out
}

func (s *Scheduler) notify(n batchNotifier, changes []notifier.Change) {
	if n == nil || len(changes) == 0 {
		return
	}
	s.mu.RLock()
	statusPageURL := s.page.PublicURL
	s.mu.RUnlock()
	if err := n.Notify(notifier.Batch{ChangedAt: time.Now(), Changes: changes, StatusPageURL: statusPageURL}); err != nil {
		s.logger.Warn("提交状态变更通知失败", "err", err, "changes", len(changes))
	}
}

func (s *Scheduler) Snapshot() model.StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	page := s.page
	now := time.Now().Unix()
	resp := model.StatusResponse{GeneratedAt: now, AllOK: true, Page: &page, Services: make([]model.ServiceView, 0, len(s.order))}
	for _, id := range s.order {
		st := s.states[id]
		if st == nil || !st.svc.IsEnabled() {
			continue
		}
		view := model.ServiceView{
			Model: st.svc.Name, Provider: st.svc.Provider, IntervalSec: st.svc.IntervalSec,
			UptimePct: uptimePct(st.history), Last: st.last,
			History: append([]model.ProbeResult(nil), st.history...),
		}
		// 输出暂停区间；进行中的区间（To == 0）以当前时刻闭合，供前端渲染当前暂停状态。
		for _, p := range st.pauses {
			to := p.To
			if to == 0 {
				to = now
			}
			view.Pauses = append(view.Pauses, model.PauseSpan{From: p.From, To: to})
		}
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

type dailyStats struct {
	upSec     int64
	downSec   int64
	downCount int
	uptimePct float64
}

func beijingDayStart(timestamp int64) int64 {
	current := time.Unix(timestamp, 0).In(beijingLocation)
	return time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, beijingLocation).Unix()
}

// calculateDailyStats 将相邻探测之间的时间归属于前一状态；起点前最后一条记录
// 仅用于确定零点状态，不会把统计范围扩展到前一天。
func calculateDailyStats(results []model.ProbeResult, since, until int64) dailyStats {
	var stats dailyStats
	var known, statusOK bool
	var cursor int64
	addDuration := func(seconds int64) {
		if seconds <= 0 {
			return
		}
		if statusOK {
			stats.upSec += seconds
		} else {
			stats.downSec += seconds
		}
	}
	for _, result := range results {
		if result.TS > until {
			break
		}
		if result.TS < since {
			statusOK, known, cursor = result.OK, true, since
			continue
		}
		if !known {
			statusOK, known, cursor = result.OK, true, result.TS
			if !result.OK {
				stats.downCount++
			}
			continue
		}
		addDuration(result.TS - cursor)
		if statusOK && !result.OK {
			stats.downCount++
		}
		statusOK, cursor = result.OK, result.TS
	}
	if known {
		addDuration(until - cursor)
	}
	observed := stats.upSec + stats.downSec
	if observed == 0 {
		if known && statusOK {
			stats.uptimePct = 100
		}
		return stats
	}
	stats.uptimePct = float64(stats.upSec) / float64(observed) * 100
	return stats
}

func failureStartFromResults(results []model.ProbeResult, recoveredAt int64) int64 {
	startedAt := int64(0)
	for i := len(results) - 1; i >= 0; i-- {
		result := results[i]
		if result.TS >= recoveredAt {
			continue
		}
		if result.OK {
			break
		}
		startedAt = result.TS
	}
	return startedAt
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
