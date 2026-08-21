package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor/probe"
)

func TestCheckDueTriggersOnce(t *testing.T) {
	s := New(nil, nil)
	var calls atomic.Int64
	s.probeFn = func(_ context.Context, svc *model.Service) probe.Result {
		_ = svc
		calls.Add(1)
		return probe.Result{OK: true, LatencyMS: 5}
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
	s.probeFn = func(_ context.Context, svc *model.Service) probe.Result {
		_ = svc
		calls.Add(1)
		return probe.Result{OK: true}
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
	s.probeFn = func(_ context.Context, svc *model.Service) probe.Result {
		if svc.Protocol == model.ProtocolHTTP {
			return probe.Result{OK: false, Error: "boom"}
		}
		return probe.Result{OK: true}
	}
	s.Reload([]model.Service{testSvc("s1", true)}, defaultPage())

	r, err := s.ProbeNow(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Error != "boom" {
		t.Errorf("ProbeNow 结果 = %+v", r)
	}
	if r.StartedAt == 0 || r.StartedAt > r.TS {
		t.Errorf("ProbeNow 未记录合法探测开始时间: %+v", r)
	}
	// 结果计入历史
	snap := s.Snapshot()
	if snap.Services[0].Last == nil || snap.Services[0].Last.OK {
		t.Errorf("ProbeNow 结果未记录: %+v", snap.Services[0])
	}

	if _, err := s.ProbeNow(context.Background(), "nope"); err == nil {
		t.Error("探测不存在的服务应报错")
	}
}

func TestProbeNowRespectsCallerCancellation(t *testing.T) {
	s := New(nil, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	s.probeFn = func(ctx context.Context, _ *model.Service) probe.Result {
		<-ctx.Done()
		return probe.Result{OK: false, Error: ctx.Err().Error()}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ProbeNow(ctx, "s1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeNow 应返回调用方取消: %v", err)
	}
	if snapshot := s.Snapshot(); snapshot.Services[0].Last != nil {
		t.Fatalf("取消的手动探测不应进入历史: %+v", snapshot.Services[0].Last)
	}
}

func TestScheduledAndManualProbeShareOneInFlightRequest(t *testing.T) {
	s := New(nil, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	s.probeFn = func(context.Context, *model.Service) probe.Result {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return probe.Result{OK: true, LatencyMS: 9}
	}
	s.checkDue()
	<-started
	if probing := s.Snapshot().Services[0].ProbeStartedAt; probing == 0 {
		t.Fatal("在途探测应在状态快照中携带开始时间")
	}

	result := make(chan *model.ProbeResult, 1)
	errs := make(chan error, 1)
	go func() {
		probeResult, err := s.ProbeNow(context.Background(), "s1")
		result <- probeResult
		errs <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("手动探测不应与定时探测重叠，调用数 = %d", got)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if probeResult := <-result; probeResult == nil || !probeResult.OK {
		t.Fatalf("手动调用应共享定时探测结果: %+v", probeResult)
	}
	s.wg.Wait()
	snapshot := s.Snapshot().Services[0]
	if history := snapshot.History; len(history) != 1 {
		t.Fatalf("共享探测只能记录一次: %+v", history)
	}
	if snapshot.ProbeStartedAt != 0 {
		t.Fatalf("探测完成后不应继续暴露在途状态: %+v", snapshot)
	}
}

func TestConcurrentManualProbesShareFlightWhileWaitingForSlot(t *testing.T) {
	s := New(nil, nil, Options{MaxConcurrentProbes: 1})
	if err := s.Reload([]model.Service{
		testSvc("blocker", true),
		testSvc("target", true),
	}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	var targetCalls atomic.Int64
	s.probeFn = func(_ context.Context, service *model.Service) probe.Result {
		if service.UID == "blocker" {
			close(blockerStarted)
			<-releaseBlocker
			return probe.Result{OK: true}
		}
		targetCalls.Add(1)
		return probe.Result{OK: true}
	}
	s.checkDue()
	<-blockerStarted

	const callers = 16
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			_, err := s.ProbeNow(context.Background(), "target")
			errCh <- err
		}()
	}
	ready.Wait()
	close(start)
	// 让所有调用都观察到 target 尚无 flight、但全局槽位已占满的状态。
	time.Sleep(20 * time.Millisecond)
	close(releaseBlocker)
	for range callers {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	s.wg.Wait()
	if calls := targetCalls.Load(); calls != 1 {
		t.Fatalf("并发手动探测应共享等待槽位的 flight，调用数 = %d", calls)
	}
}

func TestCancelingOneManualWaiterDoesNotCancelSharedProbe(t *testing.T) {
	s := New(nil, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	s.probeFn = func(ctx context.Context, _ *model.Service) probe.Result {
		calls.Add(1)
		close(started)
		select {
		case <-ctx.Done():
			return probe.Result{OK: false, Error: ctx.Err().Error()}
		case <-release:
			return probe.Result{OK: true}
		}
	}

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerErr := make(chan error, 1)
	go func() {
		_, err := s.ProbeNow(ownerCtx, "s1")
		ownerErr <- err
	}()
	<-started

	joined := make(chan struct{})
	joinedResult := make(chan *model.ProbeResult, 1)
	joinedErr := make(chan error, 1)
	go func() {
		close(joined)
		result, err := s.ProbeNow(context.Background(), "s1")
		joinedResult <- result
		joinedErr <- err
	}()
	<-joined
	time.Sleep(10 * time.Millisecond)
	cancelOwner()
	if err := <-ownerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的调用方应停止等待: %v", err)
	}
	select {
	case err := <-joinedErr:
		t.Fatalf("仍有效的等待者不应被首调用方取消: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-joinedErr; err != nil {
		t.Fatal(err)
	}
	if result := <-joinedResult; result == nil || !result.OK {
		t.Fatalf("仍有效的等待者应收到共享结果: %+v", result)
	}
	s.wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("共享 flight 的探针调用数 = %d", calls.Load())
	}
}

func TestProbeConcurrencyIsBounded(t *testing.T) {
	const limit = 2
	s := New(nil, nil, Options{MaxConcurrentProbes: limit})
	services := make([]model.Service, 6)
	for index := range services {
		services[index] = testSvc(fmt.Sprintf("s%d", index), true)
	}
	if err := s.Reload(services, defaultPage()); err != nil {
		t.Fatal(err)
	}
	var active, maximum, calls atomic.Int64
	s.probeFn = func(context.Context, *model.Service) probe.Result {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return probe.Result{OK: true}
	}

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < int64(len(services)) && time.Now().Before(deadline) {
		s.checkDue()
		time.Sleep(time.Millisecond)
	}
	s.wg.Wait()
	if calls.Load() != int64(len(services)) {
		t.Fatalf("期望全部服务最终被探测，调用数 = %d", calls.Load())
	}
	if maximum.Load() > limit {
		t.Fatalf("并发探测峰值 = %d，限制 = %d", maximum.Load(), limit)
	}
}
