package monitor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor/probe"
)

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

func TestHistoryWindowTruncatesByTimeInsteadOfSampleCount(t *testing.T) {
	s := New(nil, nil)
	page := defaultPage()
	page.HistoryLen = 3
	s.Reload([]model.Service{testSvc("s1", true)}, page)

	for i := int64(1); i <= 10; i++ {
		timestamp := i * 60
		s.record("s1", model.ProbeResult{OK: true, TS: timestamp, LatencyMS: i})
	}
	snap := s.Snapshot()
	if len(snap.Services[0].History) != 3 {
		t.Errorf("历史应截断到 3 条，got %d", len(snap.Services[0].History))
	}
	if snap.Services[0].History[0].TS != 480 {
		t.Errorf("应保留最新 3 条: %+v", snap.Services[0].History)
	}

	// 同一时间桶内的额外手动探测不能挤掉窗口内的其他桶。
	for i := int64(0); i < 5; i++ {
		s.record("s1", model.ProbeResult{OK: true, TS: 601 + i, StartedAt: 601, LatencyMS: i})
	}
	if got := len(s.Snapshot().Services[0].History); got != 8 {
		t.Fatalf("时间窗应保留同桶额外样本，got %d", got)
	}
}

func TestReloadPauseAndResumePreservesHistoryAndDropsOldProbe(t *testing.T) {
	s := New(nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return probe.Result{OK: false, Error: "old lifecycle"}
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
func TestReloadChangedServiceDropsInFlightAndUsesModelID(t *testing.T) {
	store := openTestStore(t)
	s := New(store, nil)
	service := testSvc("s1", true)
	service.Name = "Production endpoint"
	service.Protocol = model.ProtocolChat
	service.Model = "old-model"
	s.Reload([]model.Service{service}, defaultPage())
	s.record("s1", model.ProbeResult{OK: true, TS: 1})

	started := make(chan struct{})
	release := make(chan struct{})
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		close(started)
		<-release
		return probe.Result{OK: false, Error: "old endpoint failed"}
	}
	s.checkDue()
	<-started

	updated := service
	updated.BaseURL = "http://new.example.com"
	updated.Model = "new-model"
	s.Reload([]model.Service{updated}, defaultPage())
	close(release)
	s.wg.Wait()
	if batch := claimTransitionBatch(t, store); batch != nil {
		t.Fatalf("旧端点的在途结果不应持久化状态变化: %+v", batch)
	}

	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		return probe.Result{OK: false, Error: "new endpoint failed"}
	}
	s.checkDue()
	s.wg.Wait()
	batch := claimTransitionBatch(t, store)
	if batch == nil || len(batch.Changes) != 1 || batch.Changes[0].Model != "new-model" {
		t.Fatalf("状态变化应使用真实模型 ID: %+v", batch)
	}
}

// 暂停/恢复必须保留持久化历史，并允许恢复后立即重新调度。
func TestReloadPauseResumePersistsHistoryAcrossRestart(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	now := time.Now().Unix()

	// 预置持久化历史，模拟暂停前已有的观测记录。
	if err := st.AppendResult(ctx, "s1", model.ProbeResult{OK: true, TS: now - 120, LatencyMS: 12}); err != nil {
		t.Fatalf("预置历史失败: %v", err)
	}
	if err := st.AppendResult(ctx, "s1", model.ProbeResult{OK: true, TS: now - 60, LatencyMS: 8}); err != nil {
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
	if resumed.Services[0].Last == nil || resumed.Services[0].Last.TS != now-60 {
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
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		calls.Add(1)
		return probe.Result{OK: true, LatencyMS: 3}
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
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return probe.Result{OK: false, Error: "removed lifecycle"}
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
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return probe.Result{OK: false, Error: "stale manual probe"}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	s.record("s1", model.ProbeResult{OK: true, TS: 1, LatencyMS: 10})

	var probeResult *model.ProbeResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := s.ProbeNow(context.Background(), "s1")
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

func TestReloadFailurePreservesPreviousRuntimeState(t *testing.T) {
	st := openTestStore(t)
	s := New(st, nil)
	services := []model.Service{testSvc("s1", true), testSvc("s2", true)}
	if err := s.Reload(services, defaultPage()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(nil, defaultPage()); err == nil {
		t.Fatal("持久化历史删除失败时 Reload 应失败")
	}
	snapshot := s.Snapshot()
	if len(snapshot.Services) != 2 || snapshot.Services[0].ID != "s1" || snapshot.Services[1].ID != "s2" {
		t.Fatalf("Reload 失败后应保留原运行状态: %+v", snapshot.Services)
	}
}

func TestReloadPublishesStateAfterCommittedDeletionDuringStop(t *testing.T) {
	store := openTestStore(t)
	repository := &deleteHookRepository{Repository: store}
	s := New(repository, nil)
	service := testSvc("s1", true)
	if err := s.Reload([]model.Service{service}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	s.record("s1", model.ProbeResult{OK: true, TS: 1})

	root, cancel := context.WithCancel(context.Background())
	if err := s.Start(root); err != nil {
		t.Fatal(err)
	}
	repository.afterDelete = cancel
	if err := s.Reload(nil, defaultPage()); err != nil {
		t.Fatalf("删除已提交后必须发布对应运行状态: %v", err)
	}
	if services := s.Snapshot().Services; len(services) != 0 {
		t.Fatalf("删除已提交后仍保留运行状态: %+v", services)
	}
	history, err := store.LoadHistory(context.Background(), "s1", 10)
	if err != nil || len(history) != 0 {
		t.Fatalf("服务历史未删除: history=%+v err=%v", history, err)
	}

	ctx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
