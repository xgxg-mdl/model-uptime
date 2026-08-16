package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor/probe"
	"github.com/xgxg-mdl/model-uptime/internal/storage/sqlite"
)

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
	var change *model.StatusChange
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

func claimTransitionBatch(t *testing.T, store *sqlite.Store) *model.TransitionBatch {
	t.Helper()
	now := time.Now().Add(transitionAggregationDelay + time.Second)
	batch, _, err := store.ClaimTransitions(context.Background(), now, now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestCheckDuePersistsMixedChangesForOneDeliveryBatch(t *testing.T) {
	store := openTestStore(t)
	s := New(store, nil)
	page := defaultPage()
	page.PublicURL = "https://status.example.com/"
	s.Reload([]model.Service{testSvc("down", true), testSvc("recovered", true)}, page)
	s.record("down", model.ProbeResult{OK: true, TS: 1})
	s.record("recovered", model.ProbeResult{OK: false, TS: 1, Error: "old failure"})

	s.probeFn = func(_ context.Context, svc *model.Service) probe.Result {
		if svc.ID == "down" {
			return probe.Result{OK: false, Error: "timeout"}
		}
		return probe.Result{OK: true, LatencyMS: 42}
	}

	s.checkDue()
	s.wg.Wait()
	batch := claimTransitionBatch(t, store)
	if batch == nil || len(batch.Changes) != 2 {
		t.Fatalf("相邻变化应持久化为一个投递批次: %+v", batch)
	}
	if batch.StatusPageURL != page.PublicURL {
		t.Fatalf("持久化批次未携带探针页地址: %+v", batch)
	}
	statuses := map[string]string{}
	for _, change := range batch.Changes {
		statuses[change.ServiceID] = change.Status
	}
	if statuses["down"] != "down" || statuses["recovered"] != "up" {
		t.Fatalf("聚合状态错误: %v", statuses)
	}
}

func TestFirstAndContinuousFailureDoNotNotify(t *testing.T) {
	store := openTestStore(t)
	s := New(store, nil)
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())
	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		return probe.Result{OK: false, Error: "still down"}
	}

	s.checkDue()
	s.wg.Wait()
	if batch := claimTransitionBatch(t, store); batch != nil {
		t.Fatalf("首次异常只应建立基线: %+v", batch)
	}
	s.mu.Lock()
	s.states["s1"].lastProbe = time.Time{}
	s.mu.Unlock()
	s.checkDue()
	s.wg.Wait()
	if batch := claimTransitionBatch(t, store); batch != nil {
		t.Fatalf("持续异常不应重复通知: %+v", batch)
	}

	s.probeFn = func(_ context.Context, _ *model.Service) probe.Result {
		return probe.Result{OK: true, LatencyMS: 12}
	}
	s.mu.Lock()
	s.states["s1"].lastProbe = time.Time{}
	s.mu.Unlock()
	s.checkDue()
	s.wg.Wait()
	batch := claimTransitionBatch(t, store)
	if batch == nil || len(batch.Changes) != 1 || batch.Changes[0].Status != "up" {
		t.Fatalf("异常恢复应持久化一次状态变化: %+v", batch)
	}
}

func TestInvalidatedProbeDoesNotPersistTransition(t *testing.T) {
	store := openTestStore(t)
	s := New(store, nil)
	s.Reload([]model.Service{testSvc("kept", true), testSvc("removed", true)}, defaultPage())
	s.record("kept", model.ProbeResult{OK: true, TS: 1})
	s.record("removed", model.ProbeResult{OK: true, TS: 1})

	started := make(chan string, 2)
	release := make(chan struct{})
	s.probeFn = func(_ context.Context, svc *model.Service) probe.Result {
		started <- svc.ID
		<-release
		return probe.Result{OK: false, Error: "failed"}
	}
	s.checkDue()
	<-started
	<-started
	s.Reload([]model.Service{testSvc("kept", true)}, defaultPage())
	close(release)
	s.wg.Wait()
	batch := claimTransitionBatch(t, store)
	if batch == nil || len(batch.Changes) != 1 || batch.Changes[0].ServiceID != "kept" {
		t.Fatalf("只应持久化仍有效服务的变化: %+v", batch)
	}
}

func TestProbePersistenceFailureDoesNotAdvanceMemory(t *testing.T) {
	st := openTestStore(t)
	s := New(st, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	s.probeFn = func(context.Context, *model.Service) probe.Result {
		return probe.Result{OK: true, LatencyMS: 7}
	}

	result, err := s.ProbeNow(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.OK {
		t.Fatalf("探测结果应正常返回: %+v", result)
	}
	snapshot := s.Snapshot()
	if snapshot.Services[0].Last != nil || len(snapshot.Services[0].History) != 0 {
		t.Fatalf("持久化失败时不得推进内存状态: %+v", snapshot.Services[0])
	}
}
