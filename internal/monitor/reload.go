package monitor

import (
	"fmt"
	"reflect"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

// Reload 保留同 ID 服务的观测历史；启用状态切换被视为暂停或恢复，而非新生命周期，
// 因此 last/history 与持久化记录始终保留。仅当服务从配置中移除时才终止观测并删除其历史。
func (s *Scheduler) Reload(services []model.Service, page model.PageConfig) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	ctx, err := s.beginOperation()
	if err != nil {
		return err
	}
	defer s.operationWG.Done()

	// reloadGate 只暂停新的探测登记；Snapshot 仍可持续读取旧的完整状态。
	s.reloadGate.Lock()
	defer s.reloadGate.Unlock()
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	s.mu.RLock()
	current := make(map[string]*serviceState, len(s.states))
	for id, state := range s.states {
		current[id] = cloneServiceState(state)
	}
	currentOrder := append([]string(nil), s.order...)
	nextGeneration := s.nextGeneration
	s.mu.RUnlock()

	histories := make(map[string][]model.ProbeResult)
	if s.store != nil {
		for _, service := range services {
			if _, exists := current[service.ID]; exists {
				continue
			}
			history, err := s.store.LoadHistory(ctx, service.ID, page.HistoryLen)
			if err != nil {
				return fmt.Errorf("恢复服务 %q 历史失败: %w", service.ID, err)
			}
			histories[service.ID] = history
		}
	}

	next := make(map[string]*serviceState, len(services))
	order := make([]string, 0, len(services))
	seen := make(map[string]struct{}, len(services))
	now := time.Now().Unix()
	for _, service := range services {
		seen[service.ID] = struct{}{}
		state, exists := current[service.ID]
		if !exists {
			nextGeneration++
			history := histories[service.ID]
			state = &serviceState{
				svc: service, history: append([]model.ProbeResult(nil), history...),
				generation: nextGeneration,
			}
			if len(history) > 0 {
				last := history[len(history)-1]
				state.last = &last
			}
		} else {
			previousEnabled, nextEnabled := state.svc.IsEnabled(), service.IsEnabled()
			switch {
			case previousEnabled && !nextEnabled:
				nextGeneration++
				state.generation = nextGeneration
				state.flight = nil
				state.pauses = append(state.pauses, model.PauseSpan{From: now})
			case !previousEnabled && nextEnabled:
				nextGeneration++
				state.generation = nextGeneration
				state.flight = nil
				state.lastProbe = time.Time{}
				closeLatestPause(state.pauses, now)
			case !reflect.DeepEqual(state.svc, service):
				// 配置变化会使旧端点的在途结果失效，但既有观测历史保持不变。
				nextGeneration++
				state.generation = nextGeneration
				state.flight = nil
				state.lastProbe = time.Time{}
			}
			state.svc = service
		}
		if limit := page.HistoryLen; limit > 0 && len(state.history) > limit {
			state.history = append([]model.ProbeResult(nil), state.history[len(state.history)-limit:]...)
		}
		next[service.ID] = state
		order = append(order, service.ID)
	}

	removed := make([]string, 0, len(current))
	for _, id := range currentOrder {
		if _, exists := seen[id]; !exists {
			removed = append(removed, id)
		}
	}
	if s.store != nil && len(removed) > 0 {
		if _, err := s.store.DeleteHistories(ctx, removed); err != nil {
			return fmt.Errorf("删除已移除服务历史失败: %w", err)
		}
	}

	// 持久化删除一旦提交，就必须发布与之对应的内存状态；否则调用方回滚
	// 配置时会留下无法回滚的数据缺口。Stop 会等待本次 operation 完成。
	s.mu.Lock()
	s.page = page
	s.states = next
	s.order = order
	s.nextGeneration = nextGeneration
	s.mu.Unlock()
	return nil
}

func cloneServiceState(state *serviceState) *serviceState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.history = append([]model.ProbeResult(nil), state.history...)
	clone.pauses = append([]model.PauseSpan(nil), state.pauses...)
	if state.last != nil {
		last := *state.last
		clone.last = &last
	}
	return &clone
}

func closeLatestPause(pauses []model.PauseSpan, now int64) {
	for index := len(pauses) - 1; index >= 0; index-- {
		if pauses[index].To == 0 {
			pauses[index].To = now
			return
		}
	}
}
