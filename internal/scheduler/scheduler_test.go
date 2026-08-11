package scheduler

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/prober"
	"github.com/lefachao/model-uptime/internal/store"
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

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatalf("打开测试 store 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
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

func TestReloadPauseAndResumePreservesHistoryAndDropsOldProbe(t *testing.T) {
	s := New(nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return prober.Result{OK: false, Error: "old lifecycle"}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	// 先写入旧历史，作为暂停前已存在的观测状态。
	s.record("s1", model.ProbeResult{OK: true, TS: 1, LatencyMS: 10})
	s.checkDue()
	<-started

	// 暂停后恢复：两次转换都推进 generation，旧 in-flight 探测不能再写回。
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	close(release)
	<-finished

	deadline := time.After(time.Second)
	for {
		snap := s.Snapshot()
		if len(snap.Services) != 1 {
			t.Fatalf("恢复后服务应可见: %+v", snap.Services)
		}
		// 旧异步失败既不能覆盖 Last，也不能新增历史。
		if (snap.Services[0].Last == nil || snap.Services[0].Last.OK) && len(snap.Services[0].History) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("旧生命周期结果不应写回: %+v", snap.Services[0])
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// 新 generation 的探测结果应正常追加，旧历史仍在。
	s.record("s1", model.ProbeResult{OK: true, TS: 2, LatencyMS: 5})
	snap := s.Snapshot()
	if snap.Services[0].Last == nil || !snap.Services[0].Last.OK || len(snap.Services[0].History) != 2 {
		t.Errorf("恢复后应保留旧历史并追加新结果: %+v", snap.Services[0])
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
	s.checkDue()                      // 第二次调用：lastProbe 已更新，不应重复触发
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

// 暂停/恢复必须保留持久化历史，并允许恢复后立即重新调度。
func TestReloadPauseResumePersistsHistoryAcrossRestart(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// 预置持久化历史，模拟暂停前已有的观测记录。
	if err := st.AppendResult(ctx, "s1", model.ProbeResult{OK: true, TS: 100, LatencyMS: 12}); err != nil {
		t.Fatalf("预置历史失败: %v", err)
	}
	if err := st.AppendResult(ctx, "s1", model.ProbeResult{OK: true, TS: 200, LatencyMS: 8}); err != nil {
		t.Fatalf("预置历史失败: %v", err)
	}

	s := New(st, nil)
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	before := s.Snapshot()
	if len(before.Services) != 1 || len(before.Services[0].History) != 2 {
		t.Fatalf("恢复应加载全部历史: %+v", before.Services)
	}

	// 暂停：禁用服务不展示，但历史不得被删除。
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	if paused := s.Snapshot(); len(paused.Services) != 0 {
		t.Errorf("禁用服务不应展示: %+v", paused.Services)
	}
	if hist, err := st.LoadHistory(ctx, "s1", 10); err != nil || len(hist) != 2 {
		t.Errorf("暂停后持久化历史不应减少: hist=%+v err=%v", hist, err)
	}

	// 恢复：历史与最近状态与暂停前一致。
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	resumed := s.Snapshot()
	if len(resumed.Services) != 1 || len(resumed.Services[0].History) != 2 {
		t.Errorf("恢复后历史应保留: %+v", resumed.Services)
	}
	if resumed.Services[0].Last == nil || resumed.Services[0].Last.TS != 200 {
		t.Errorf("恢复后 Last 应为暂停前最新结果: %+v", resumed.Services[0].Last)
	}
	if resumed.Services[0].UptimePct != 100.0 {
		t.Errorf("恢复后 uptime 应与暂停前一致: %v", resumed.Services[0].UptimePct)
	}
	// 恢复后应记录一个已闭合的暂停区间。
	if pauses := resumed.Services[0].Pauses; len(pauses) != 1 {
		t.Errorf("恢复后应有 1 个暂停区间，got %d", len(pauses))
	} else if pauses[0].From == 0 || pauses[0].To == 0 || pauses[0].To < pauses[0].From {
		t.Errorf("暂停区间应已闭合且合法: %+v", pauses[0])
	}

	// lastProbe 被清零，恢复后第一次调度应立即触发。
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		calls.Add(1)
		return prober.Result{OK: true, LatencyMS: 3}
	}
	s.checkDue()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("恢复后应立即调度一次，实际 %d", calls.Load())
	}
}

// 从配置移除服务仍应彻底删除持久化历史，并在飞的旧探测不能回写。
func TestReloadRemoveServiceDeletesHistoryAndDropsInFlight(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int64
	s := New(st, nil)
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return prober.Result{OK: false, Error: "removed lifecycle"}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	if err := st.AppendResult(ctx, "s1", model.ProbeResult{OK: true, TS: 10, LatencyMS: 5}); err != nil {
		t.Fatalf("预置历史失败: %v", err)
	}

	s.checkDue()
	<-started

	// 服务从配置中移除：历史应被删除。
	s.Reload(nil, defaultPage())
	close(release)
	<-finished

	if snap := s.Snapshot(); len(snap.Services) != 0 {
		t.Errorf("移除后不应再展示服务: %+v", snap.Services)
	}
	if hist, err := st.LoadHistory(ctx, "s1", 10); err != nil || len(hist) != 0 {
		t.Errorf("移除服务应清空持久化历史: hist=%+v err=%v", hist, err)
	}
}

// 在飞的手动探测（ProbeNow）在恢复后不能写回新生命周期。
func TestProbeNowInFlightDroppedAfterPauseResume(t *testing.T) {
	s := New(nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return prober.Result{OK: false, Error: "stale manual probe"}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	s.record("s1", model.ProbeResult{OK: true, TS: 1, LatencyMS: 10})

	var probeResult *model.ProbeResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := s.ProbeNow("s1")
		if err == nil {
			probeResult = r
		}
	}()
	<-started

	// 手动探测在飞行期间执行暂停 → 恢复。
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	close(release)
	<-finished
	<-done

	// ProbeNow 仍可把测连结果返回给调用方，但其结果不应污染当前生命周期。
	snap := s.Snapshot()
	if len(snap.Services) != 1 || len(snap.Services[0].History) != 1 ||
		snap.Services[0].Last == nil || !snap.Services[0].Last.OK {
		t.Errorf("旧手动探测不应写回: %+v", snap.Services)
	}
	if probeResult == nil {
		t.Error("ProbeNow 应返回自身结果")
	}
}

// 连续多次暂停/恢复应记录多个独立暂停区间，且全部已闭合。
func TestReloadMultiplePausesRecorded(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	// 第一轮暂停 → 恢复
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	// 第二轮暂停 → 恢复
	s.Reload([]model.Service{testSvc("s1", false)}, defaultPage())
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	snap := s.Snapshot()
	if len(snap.Services) != 1 {
		t.Fatalf("服务应可见: %+v", snap.Services)
	}
	pauses := snap.Services[0].Pauses
	if len(pauses) != 2 {
		t.Fatalf("应记录 2 个暂停区间，got %d: %+v", len(pauses), pauses)
	}
	for i, p := range pauses {
		if p.From == 0 || p.To == 0 || p.To < p.From {
			t.Errorf("暂停区间 %d 应已闭合且合法: %+v", i, p)
		}
	}
	if pauses[1].From < pauses[0].To {
		t.Errorf("第二个暂停应在第一个之后: %+v", pauses)
	}
}
