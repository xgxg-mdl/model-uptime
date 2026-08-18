package monitor

import (
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

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
			ID: st.svc.ID, Model: st.svc.Name, Provider: st.svc.Provider, SortOrder: st.svc.SortOrder,
			IntervalSec: st.svc.IntervalSec, WarningSec: st.svc.WarningSec,
			ObservedSince: st.observedSince,
			UptimePct:     uptimePct(completedWindowHistory(st.history, now, page.HistoryLen, st.svc.IntervalSec)), Last: st.last,
			History: append([]model.ProbeResult(nil), st.history...),
		}
		if st.flight != nil {
			view.ProbeStartedAt = st.flight.startedAt
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

func completedWindowHistory(history []model.ProbeResult, now int64, historyLength, intervalSec int) []model.ProbeResult {
	if len(history) == 0 || historyLength <= 0 || intervalSec <= 0 {
		return history
	}
	windowEnd := completedWindowEnd(now, intervalSec)
	cutoff := windowEnd - int64(historyLength)*int64(intervalSec)
	first := 0
	for first < len(history) && probeStartedAt(history[first]) < cutoff {
		first++
	}
	last := first
	for last < len(history) && probeStartedAt(history[last]) < windowEnd {
		last++
	}
	return history[first:last]
}

func completedWindowEnd(timestamp int64, intervalSec int) int64 {
	if intervalSec <= 0 {
		return timestamp
	}
	return timestamp - timestamp%int64(intervalSec)
}
