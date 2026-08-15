package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
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

func TestCalculateDailyStatsAndBeijingBoundary(t *testing.T) {
	stats := calculateDailyStats([]model.ProbeResult{
		{OK: true, TS: 50},
		{OK: false, TS: 200},
		{OK: true, TS: 500},
		{OK: false, TS: 800},
		{OK: true, TS: 1000},
	}, 100, 1000)
	if stats.upSec != 400 || stats.downSec != 500 || stats.downCount != 2 {
		t.Fatalf("今日时间统计错误: %+v", stats)
	}
	if stats.uptimePct < 44.44 || stats.uptimePct > 44.45 {
		t.Fatalf("今日可用率错误: %.4f", stats.uptimePct)
	}
	if got := failureStartFromResults([]model.ProbeResult{{OK: true, TS: 100}, {OK: false, TS: 200}, {OK: false, TS: 300}, {OK: true, TS: 400}}, 400); got != 200 {
		t.Fatalf("异常起点 = %d，期望 200", got)
	}
	beijingTime := time.Date(2026, 8, 15, 10, 50, 2, 0, beijingLocation)
	wantStart := time.Date(2026, 8, 15, 0, 0, 0, 0, beijingLocation).Unix()
	if got := beijingDayStart(beijingTime.Unix()); got != wantStart {
		t.Fatalf("北京时间零点 = %d，期望 %d", got, wantStart)
	}
}

func TestRecordChangeUsesPersistedDailyStats(t *testing.T) {
	s := New(openTestStore(t), nil)
	page := defaultPage()
	page.HistoryLen = 2
	s.Reload([]model.Service{testSvc("s1", true)}, page)
	generation := s.states["s1"].generation
	dayStart := time.Date(2026, 8, 15, 0, 0, 0, 0, beijingLocation).Unix()
	results := []model.ProbeResult{
		{OK: true, TS: dayStart},
		{OK: false, TS: dayStart + 3600},
		{OK: false, TS: dayStart + 4200},
		{OK: true, TS: dayStart + 7200},
	}
	var change *notifier.Change
	for _, result := range results {
		change = s.recordGeneration("s1", generation, result)
	}
	if change == nil || change.Status != "up" {
		t.Fatalf("恢复探测应产生状态变化: %+v", change)
	}
	if change.OutageDurationSec != 3600 || change.TodayUpSec != 3600 || change.TodayDownSec != 3600 || change.TodayDownCount != 1 || change.TodayUptimePct != 50 {
		t.Fatalf("通知统计未使用完整持久化历史: %+v", change)
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

type collectingNotifier struct {
	mu      sync.Mutex
	batches []notifier.Batch
	ready   chan struct{}
}

func (n *collectingNotifier) Notify(batch notifier.Batch) error {
	n.mu.Lock()
	n.batches = append(n.batches, batch)
	n.mu.Unlock()
	select {
	case n.ready <- struct{}{}:
	default:
	}
	return nil
}

func (n *collectingNotifier) snapshot() []notifier.Batch {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifier.Batch(nil), n.batches...)
}

func TestCheckDueAggregatesMixedChangesIntoOneBatch(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("down", true), testSvc("recovered", true)}, defaultPage())
	s.record("down", model.ProbeResult{OK: true, TS: 1})
	s.record("recovered", model.ProbeResult{OK: false, TS: 1, Error: "old failure"})

	collector := &collectingNotifier{ready: make(chan struct{}, 1)}
	s.SetNotifier(collector)
	s.probeFn = func(_ context.Context, svc *model.Service) prober.Result {
		if svc.ID == "down" {
			return prober.Result{OK: false, Error: "timeout"}
		}
		return prober.Result{OK: true, LatencyMS: 42}
	}

	s.checkDue()
	select {
	case <-collector.ready:
	case <-time.After(time.Second):
		t.Fatal("等待聚合通知超时")
	}
	batches := collector.snapshot()
	if len(batches) != 1 || len(batches[0].Changes) != 2 {
		t.Fatalf("同轮变化应合并成一批: %+v", batches)
	}
	statuses := map[string]string{}
	for _, change := range batches[0].Changes {
		statuses[change.ServiceID] = change.Status
	}
	if statuses["down"] != "down" || statuses["recovered"] != "up" {
		t.Fatalf("聚合状态错误: %v", statuses)
	}
}

func TestFirstAndContinuousFailureDoNotNotify(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	collector := &collectingNotifier{ready: make(chan struct{}, 1)}
	s.SetNotifier(collector)
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		return prober.Result{OK: false, Error: "still down"}
	}

	s.checkDue()
	s.wg.Wait()
	if batches := collector.snapshot(); len(batches) != 0 {
		t.Fatalf("首次异常只应建立基线: %+v", batches)
	}
	s.mu.Lock()
	s.states["s1"].lastProbe = time.Time{}
	s.mu.Unlock()
	s.checkDue()
	s.wg.Wait()
	if batches := collector.snapshot(); len(batches) != 0 {
		t.Fatalf("持续异常不应重复通知: %+v", batches)
	}

	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		return prober.Result{OK: true, LatencyMS: 12}
	}
	s.mu.Lock()
	s.states["s1"].lastProbe = time.Time{}
	s.mu.Unlock()
	s.checkDue()
	s.wg.Wait()
	batches := collector.snapshot()
	if len(batches) != 1 || len(batches[0].Changes) != 1 || batches[0].Changes[0].Status != "up" {
		t.Fatalf("异常恢复应发送一次通知: %+v", batches)
	}
}

func TestInvalidatedProbeStillCompletesCycle(t *testing.T) {
	s := New(nil, nil)
	s.Reload([]model.Service{testSvc("kept", true), testSvc("removed", true)}, defaultPage())
	s.record("kept", model.ProbeResult{OK: true, TS: 1})
	s.record("removed", model.ProbeResult{OK: true, TS: 1})

	started := make(chan string, 2)
	release := make(chan struct{})
	s.probeFn = func(_ context.Context, svc *model.Service) prober.Result {
		started <- svc.ID
		<-release
		return prober.Result{OK: false, Error: "failed"}
	}
	collector := &collectingNotifier{ready: make(chan struct{}, 1)}
	s.SetNotifier(collector)
	s.checkDue()
	<-started
	<-started
	s.Reload([]model.Service{testSvc("kept", true)}, defaultPage())
	close(release)

	select {
	case <-collector.ready:
	case <-time.After(time.Second):
		t.Fatal("失效探测不应阻塞聚合批次完成")
	}
	batches := collector.snapshot()
	if len(batches) != 1 || len(batches[0].Changes) != 1 || batches[0].Changes[0].ServiceID != "kept" {
		t.Fatalf("批次应只包含仍有效的服务: %+v", batches)
	}
}

func TestReloadChangedServiceDropsInFlightAndUsesModelID(t *testing.T) {
	s := New(nil, nil)
	service := testSvc("s1", true)
	service.Name = "Production endpoint"
	service.Protocol = model.ProtocolChat
	service.Model = "old-model"
	s.Reload([]model.Service{service}, defaultPage())
	s.record("s1", model.ProbeResult{OK: true, TS: 1})

	started := make(chan struct{})
	release := make(chan struct{})
	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		close(started)
		<-release
		return prober.Result{OK: false, Error: "old endpoint failed"}
	}
	collector := &collectingNotifier{ready: make(chan struct{}, 1)}
	s.SetNotifier(collector)
	s.checkDue()
	<-started

	updated := service
	updated.BaseURL = "http://new.example.com"
	updated.Model = "new-model"
	s.Reload([]model.Service{updated}, defaultPage())
	close(release)
	s.wg.Wait()
	if batches := collector.snapshot(); len(batches) != 0 {
		t.Fatalf("旧端点的在途结果不应触发通知: %+v", batches)
	}

	s.probeFn = func(_ context.Context, _ *model.Service) prober.Result {
		return prober.Result{OK: false, Error: "new endpoint failed"}
	}
	s.checkDue()
	s.wg.Wait()
	batches := collector.snapshot()
	if len(batches) != 1 || len(batches[0].Changes) != 1 || batches[0].Changes[0].Model != "new-model" {
		t.Fatalf("通知应使用真实模型 ID: %+v", batches)
	}
}

func TestManualDebouncePreservesFirstPreviousStatus(t *testing.T) {
	s := New(nil, nil)
	s.manualDebounce = time.Hour
	s.queueManualChange(notifier.Change{ServiceID: "s1", PreviousStatus: "up", Status: "down"})
	s.queueManualChange(notifier.Change{ServiceID: "s1", PreviousStatus: "down", Status: "up", OK: true})
	s.mu.Lock()
	change := s.manualChanges["s1"]
	if s.manualTimer != nil {
		s.manualTimer.Stop()
	}
	s.manualChanges = make(map[string]notifier.Change)
	s.mu.Unlock()
	if change.PreviousStatus != "up" || change.Status != "up" {
		t.Fatalf("防抖窗口应保留首次旧状态和最终新状态: %+v", change)
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
