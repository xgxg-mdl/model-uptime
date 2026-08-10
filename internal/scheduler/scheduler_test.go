package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/prober"
)

func boolp(b bool) *bool { return &b }

func testSvc(id string, enabled bool) model.Service {
	return model.Service{
		ID: id, Name: id, Protocol: model.ProtocolHTTP, BaseURL: "http://example.com",
		IntervalSec: 60, TimeoutSec: 5, Enabled: boolp(enabled),
	}
}

func defaultPage() model.PageConfig {
	return model.PageConfig{HistoryLen: 60, RefreshSec: 5, ShowUptime: true}
}

func TestUptimePct(t *testing.T) {
	cases := []struct {
		name    string
		history []model.ProbeResult
		want    float64
	}{
		{"空窗口", nil, 100.0},
		{"全成功", []model.ProbeResult{{OK: true}, {OK: true}}, 100.0},
		{"半失败", []model.ProbeResult{{OK: true}, {OK: false}}, 50.0},
		{"一失败三成功", []model.ProbeResult{{OK: true}, {OK: false}, {OK: true}, {OK: true}}, 75.0},
	}
	for _, tc := range cases {
		if got := uptimePct(tc.history); got != tc.want {
			t.Errorf("%s: uptimePct = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}

func TestReloadPreservesHistory(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	s.record("s1", model.ProbeResult{OK: true, TS: 1, LatencyMS: 10})
	s.record("s1", model.ProbeResult{OK: false, TS: 2, Error: "boom"})

	// 同 id 再次 Reload：历史保留、配置更新
	updated := testSvc("s1", true)
	updated.Name = "renamed"
	s.Reload([]model.Service{updated}, defaultPage())

	snap := s.Snapshot()
	if len(snap.Services) != 1 {
		t.Fatalf("服务数 = %d", len(snap.Services))
	}
	svc := snap.Services[0]
	if svc.Model != "renamed" {
		t.Errorf("Model = %q，期望 renamed", svc.Model)
	}
	if len(svc.History) != 2 {
		t.Errorf("历史应保留 2 条，got %d", len(svc.History))
	}
	if svc.Last == nil || svc.Last.OK || svc.Last.Error != "boom" {
		t.Errorf("Last 应是最新失败: %+v", svc.Last)
	}
	if svc.UptimePct != 50.0 {
		t.Errorf("UptimePct = %v", svc.UptimePct)
	}
	if snap.AllOK {
		t.Error("存在失败服务时 all_ok 应为 false")
	}
}

func TestReloadDropsRemovedService(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("s1", true), testSvc("s2", true)}, defaultPage())
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	snap := s.Snapshot()
	if len(snap.Services) != 1 || snap.Services[0].Model != "s1" {
		t.Errorf("应只保留 s1: %+v", snap.Services)
	}
}

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
	if svc.UptimePct != 100.0 {
		t.Errorf("pending 服务 uptime 应为 100: %v", svc.UptimePct)
	}
	if !snap.AllOK {
		t.Error("pending 服务不应影响 all_ok")
	}
}

func TestHistoryWindowTruncation(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 3
	s.Reload([]model.Service{testSvc("s1", true)}, page)

	for i := int64(1); i <= 10; i++ {
		s.record("s1", model.ProbeResult{OK: true, TS: i, LatencyMS: i})
	}
	snap := s.Snapshot()
	if len(snap.Services[0].History) != 3 {
		t.Errorf("历史应截断到 3 条，got %d", len(snap.Services[0].History))
	}
	if snap.Services[0].History[0].TS != 8 {
		t.Errorf("应保留最新 3 条: %+v", snap.Services[0].History)
	}
}

func TestCheckDueTriggersOnce(t *testing.T) {
	s := New(nil, nil)
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, svc *model.Service) prober.Result {
		_ = svc
		calls.Add(1)
		return prober.Result{OK: true, LatencyMS: 5}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	s.checkDue()
	s.checkDue() // 第二次调用：lastProbe 已更新，不应重复触发
	time.Sleep(20 * time.Millisecond) // 等待 goroutine 完成

	if got := calls.Load(); got != 1 {
		t.Errorf("probeFn 调用次数 = %d，期望 1", got)
	}
	// 结果应已记录
	snap := s.Snapshot()
	if snap.Services[0].Last == nil || !snap.Services[0].Last.OK {
		t.Errorf("探测结果未记录: %+v", snap.Services[0])
	}
}

func TestCheckDueSkipsDisabled(t *testing.T) {
	s := New(nil, nil)
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, svc *model.Service) prober.Result {
		_ = svc
		calls.Add(1)
		return prober.Result{OK: true}
	}
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	s.checkDue()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("禁用服务不应被探测")
	}
}

func TestProbeNow(t *testing.T) {
	s := New(nil, nil)
	s.probeFn = func(_ context.Context, svc *model.Service) prober.Result {
		if svc.Protocol == model.ProtocolHTTP {
			return prober.Result{OK: false, Error: "boom"}
		}
		return prober.Result{OK: true}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	r, err := s.ProbeNow("s1")
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Error != "boom" {
		t.Errorf("ProbeNow 结果 = %+v", r)
	}
	// 结果计入历史
	snap := s.Snapshot()
	if snap.Services[0].Last == nil || snap.Services[0].Last.OK {
		t.Errorf("ProbeNow 结果未记录: %+v", snap.Services[0])
	}

	if _, err := s.ProbeNow("nope"); err == nil {
		t.Error("探测不存在的服务应报错")
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
