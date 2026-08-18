package monitor

import (
	"context"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

const transitionAggregationDelay = 3 * time.Second

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
func (s *Scheduler) recordGeneration(id string, generation uint64, r model.ProbeResult) *model.StatusChange {
	return s.recordGenerationContext(context.Background(), id, generation, r)
}

// recordGenerationContext 先在内存中构造候选状态，再于状态锁之外完成 SQLite I/O。
// reloadGate 和 storeMu 保证 generation 校验、追加结果与 Reload 删除历史原子排序。
func (s *Scheduler) recordGenerationContext(ctx context.Context, id string, generation uint64, r model.ProbeResult) *model.StatusChange {
	if err := contextError(ctx); err != nil {
		return nil
	}
	if r.StartedAt == 0 {
		r.StartedAt = r.TS
	}
	s.reloadGate.RLock()
	defer s.reloadGate.RUnlock()
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	s.mu.RLock()
	state, ok := s.states[id]
	if !ok || !state.svc.IsEnabled() || state.generation != generation {
		s.mu.RUnlock()
		return nil
	}
	service := state.svc
	history := append([]model.ProbeResult(nil), state.history...)
	var previous *model.ProbeResult
	if state.last != nil {
		copy := *state.last
		previous = &copy
	}
	historyLimit := s.page.HistoryLen
	statusPageURL := s.page.PublicURL
	s.mu.RUnlock()

	history = append(history, r)
	statsHistory := history
	if historyLimit > 0 {
		cutoff := r.StartedAt - int64(historyLimit)*int64(service.IntervalSec)
		first := 0
		for first < len(history) && probeStartedAt(history[first]) <= cutoff {
			first++
		}
		statsHistory = history[first:]
		// 保留窗口起点前最后一次结果，前端需要它判断上一观测周期是否覆盖左边界。
		if first > 1 {
			history = append([]model.ProbeResult(nil), history[first-1:]...)
		}
	}
	var change *model.StatusChange
	if previous != nil && previous.OK != r.OK {
		change = s.buildChange(ctx, id, service, previous.OK, statsHistory, r)
	}
	if s.store != nil {
		var transition *model.StatusTransition
		if change != nil {
			changedAt := time.Unix(r.TS, 0)
			transition = &model.StatusTransition{
				Change: *change, ChangedAt: changedAt,
				AvailableAt: changedAt.Add(transitionAggregationDelay), StatusPageURL: statusPageURL,
			}
		}
		if err := s.store.RecordProbeResult(ctx, id, r, transition); err != nil {
			s.logger.Warn("持久化探测结果失败", "svc", id, "err", err)
			return nil
		}
	}

	s.mu.Lock()
	state, ok = s.states[id]
	if !ok || !state.svc.IsEnabled() || state.generation != generation {
		s.mu.Unlock()
		return nil
	}
	state.history = history
	if state.observedSince == 0 || r.StartedAt < state.observedSince {
		state.observedSince = r.StartedAt
	}
	last := r
	state.last = &last
	s.mu.Unlock()
	return change
}

func probeStartedAt(result model.ProbeResult) int64 {
	if result.StartedAt > 0 {
		return result.StartedAt
	}
	return result.TS
}
