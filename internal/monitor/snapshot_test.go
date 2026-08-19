package monitor

import (
	"reflect"
	"testing"
	"time"

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

func TestSnapshotSortsServicesByOrderModelAndID(t *testing.T) {
	s := New(nil, nil)
	services := []model.Service{
		testSvc("unordered", true),
		testSvc("z-id", true),
		testSvc("a-id", true),
		testSvc("first", true),
	}
	services[0].SortOrder = 0
	services[1].SortOrder, services[1].Name = 20, "Same"
	services[2].SortOrder, services[2].Name = 20, "Same"
	services[3].SortOrder = 10
	if err := s.Reload(services, defaultPage()); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	got := make([]string, len(snapshot.Services))
	for index, service := range snapshot.Services {
		got[index] = service.ID
	}
	want := []string{"first", "a-id", "z-id", "unordered"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("状态页模型顺序 = %v，期望 %v", got, want)
	}
}

func TestSnapshotUptimeExcludesHistoryBeforeCurrentWindow(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 2
	service := testSvc("s1", true)
	service.IntervalSec = 60
	if err := s.Reload([]model.Service{service}, page); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	s.mu.Lock()
	s.states["s1"].history = []model.ProbeResult{
		{OK: false, TS: now - 180, StartedAt: now - 180},
		{OK: true, TS: now - 60, StartedAt: now - 60},
	}
	s.mu.Unlock()

	if got := s.Snapshot().Services[0].UptimePct; got != 100 {
		t.Fatalf("窗口起点前的延续记录不应进入 uptime，got %v", got)
	}
}

func TestSnapshotUptimeExcludesCurrentPartialInterval(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 2
	service := testSvc("s1", true)
	service.IntervalSec = 60
	if err := s.Reload([]model.Service{service}, page); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	windowEnd := now - now%60
	s.mu.Lock()
	s.states["s1"].history = []model.ProbeResult{
		{OK: true, TS: windowEnd - 30, StartedAt: windowEnd - 30},
		{OK: false, TS: windowEnd + 1, StartedAt: windowEnd},
	}
	s.mu.Unlock()

	if got := s.Snapshot().Services[0].UptimePct; got != 100 {
		t.Fatalf("当前未完成 interval 的结果不应进入 uptime，got %v", got)
	}
}

func TestSnapshotUptimeIncludesFailureAtCompletedWindowStart(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 2
	service := testSvc("s1", true)
	service.IntervalSec = 3600
	if err := s.Reload([]model.Service{service}, page); err != nil {
		t.Fatal(err)
	}
	windowEnd := completedWindowEnd(time.Now().Unix(), service.IntervalSec)
	s.mu.Lock()
	s.states["s1"].history = []model.ProbeResult{
		{OK: false, TS: windowEnd - 2*int64(service.IntervalSec), StartedAt: windowEnd - 2*int64(service.IntervalSec)},
		{OK: true, TS: windowEnd - int64(service.IntervalSec), StartedAt: windowEnd - int64(service.IntervalSec)},
	}
	s.mu.Unlock()

	if got := s.Snapshot().Services[0].UptimePct; got != 50 {
		t.Fatalf("完整窗口首桶边界的失败结果应进入 uptime，got %v", got)
	}
}
