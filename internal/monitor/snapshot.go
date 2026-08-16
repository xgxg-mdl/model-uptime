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
			ID: st.svc.ID, Model: st.svc.Name, Provider: st.svc.Provider, IntervalSec: st.svc.IntervalSec,
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
