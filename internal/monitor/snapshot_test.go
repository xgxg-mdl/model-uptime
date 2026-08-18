package monitor

import (
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestPendingAndDisabled(t *testing.T) {
	s := New(nil, nil)
	// s1 启用但从未探测（pending），s2 禁用不展示
	s.Reload([]model.Service{testSvc("s1", true), testSvc("s2", false)}, defaultPage())
	snap := s.Snapshot()

	if len(snap.Services) != 1 {
		t.Fatalf("禁用服务不应展示: %+v", snap.Services)
	}
	svc := snap.Services[0]
	if svc.Last != nil {
		t.Errorf("pending 服务 Last 应为 nil: %+v", svc.Last)
	}
	if svc.ObservedSince == 0 {
		t.Error("pending 服务应携带观测生命周期起点")
	}
	if svc.UptimePct != 100.0 {
		t.Errorf("pending 服务 uptime 应为 100: %v", svc.UptimePct)
	}
	if !snap.AllOK {
		t.Error("pending 服务不应影响 all_ok")
	}
}

func TestSnapshotPageCopy(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 42
	s.Reload([]model.Service{testSvc("s1", true)}, page)
	snap := s.Snapshot()
	if snap.Page == nil || snap.Page.HistoryLen != 42 {
		t.Errorf("快照应携带页面配置: %+v", snap.Page)
	}
}

func TestSnapshotIncludesStableServiceID(t *testing.T) {
	s := New(nil, nil)
	service := testSvc("stable-id", true)
	service.WarningSec = 12
	if err := s.Reload([]model.Service{service}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	if len(snapshot.Services) != 1 || snapshot.Services[0].ID != "stable-id" {
		t.Fatalf("状态快照缺少服务 ID: %+v", snapshot.Services)
	}
	if snapshot.Services[0].WarningSec != 12 {
		t.Fatalf("状态快照 warning_sec = %d，期望 12", snapshot.Services[0].WarningSec)
	}
}
