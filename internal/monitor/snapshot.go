package monitor

import (
	"sort"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/timeline"
)

type serviceSnapshot struct {
	service        model.Service
	last           *model.ProbeResult
	history        []model.ProbeResult
	observedSince  int64
	probeStartedAt int64
	pauses         []model.PauseSpan
}

func (s *Scheduler) Snapshot() model.StatusResponse {
	s.mu.RLock()
	// 时钟必须在状态锁内读取，确保 generated_at 不早于同一快照中的探测状态。
	now := time.Now().Unix()
	historyLength, states := s.captureSnapshotLocked(now)
	s.mu.RUnlock()
	return buildSnapshot(now, historyLength, states)
}

func (s *Scheduler) snapshotAt(now int64) model.StatusResponse {
	s.mu.RLock()
	historyLength, states := s.captureSnapshotLocked(now)
	s.mu.RUnlock()
	return buildSnapshot(now, historyLength, states)
}

func (s *Scheduler) captureSnapshotLocked(now int64) (int, []serviceSnapshot) {
	historyLength := s.page.HistoryLen
	states := make([]serviceSnapshot, 0, len(s.order))
	for _, id := range s.order {
		st := s.states[id]
		if st == nil || !st.svc.IsEnabled() {
			continue
		}
		state := serviceSnapshot{
			service:       st.svc,
			history:       append([]model.ProbeResult{}, st.history...),
			observedSince: st.observedSince,
			pauses:        make([]model.PauseSpan, 0, len(st.pauses)),
		}
		if st.last != nil {
			last := *st.last
			state.last = &last
		}
		for _, pause := range st.pauses {
			if pause.To == 0 {
				pause.To = now
			}
			state.pauses = append(state.pauses, pause)
		}
		if st.flight != nil {
			state.probeStartedAt = st.flight.startedAt
		}
		states = append(states, state)
	}
	return historyLength, states
}

func buildSnapshot(now int64, historyLength int, states []serviceSnapshot) model.StatusResponse {
	// Slot 投影可能遍历大量手动探测结果；锁内只捕获一致状态，避免阻塞结果写入。
	resp := model.StatusResponse{GeneratedAt: now, AllOK: true, Services: make([]model.ServiceView, 0, len(states))}
	for _, state := range states {
		service := state.service
		projection := timeline.Project(timeline.Input{
			AsOf: now, IntervalSec: service.IntervalSec, SlotCount: historyLength,
			WarningSec: service.WarningSec, ObservedSince: state.observedSince,
			ProbeStartedAt: state.probeStartedAt, History: state.history, Pauses: state.pauses,
		})
		view := model.ServiceView{
			ID: service.ID, Model: service.Name, Provider: service.Provider, SortOrder: service.SortOrder,
			IntervalSec: service.IntervalSec, WarningSec: service.WarningSec,
			ObservedSince: state.observedSince, ProbeStartedAt: state.probeStartedAt,
			UptimePct: projection.UptimePct, Timeline: projection.Slots, Last: state.last,
			History: state.history, Pauses: state.pauses,
		}
		resp.Services = append(resp.Services, view)
		if state.last != nil && !state.last.OK {
			resp.AllOK = false
		}
	}
	sort.SliceStable(resp.Services, func(i, j int) bool {
		return model.ServiceViewLess(resp.Services[i], resp.Services[j])
	})
	return resp
}
