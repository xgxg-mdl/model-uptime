package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor/probe"
)

func TestStopCancelsInFlightScheduledProbe(t *testing.T) {
	s := New(nil, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	s.probeFn = func(ctx context.Context, _ *model.Service) probe.Result {
		close(started)
		<-ctx.Done()
		close(canceled)
		return probe.Result{OK: false, Error: ctx.Err().Error()}
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.checkDue()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 应等待已取消探测退出: %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("Stop 未把根 context 传递给在途探测")
	}
	if snapshot := s.Snapshot(); snapshot.Services[0].Last != nil {
		t.Fatalf("停机取消不应记录为服务故障: %+v", snapshot.Services[0].Last)
	}
}

func TestStopReportsDeadlineInsteadOfPretendingToBeGraceful(t *testing.T) {
	s := New(nil, nil)
	if err := s.Reload([]model.Service{testSvc("s1", true)}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	s.probeFn = func(context.Context, *model.Service) probe.Result {
		close(started)
		<-release
		return probe.Result{OK: true}
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.checkDue()
	<-started

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("无法及时停止时应返回 deadline，得到 %v", err)
	}
	close(release)
	long, cancelLong := context.WithTimeout(context.Background(), time.Second)
	defer cancelLong()
	if err := s.Stop(long); err != nil {
		t.Fatalf("在途任务退出后 Stop 应成功: %v", err)
	}
}

func TestStopCancelsPreStartManualProbeDetachedByReload(t *testing.T) {
	s := New(nil, nil)
	service := testSvc("s1", true)
	if err := s.Reload([]model.Service{service}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.probeFn = func(ctx context.Context, _ *model.Service) probe.Result {
		close(started)
		<-ctx.Done()
		return probe.Result{OK: false, Error: ctx.Err().Error()}
	}
	probeErr := make(chan error, 1)
	go func() {
		_, err := s.ProbeNow(context.Background(), "s1")
		probeErr <- err
	}()
	<-started

	updated := service
	updated.BaseURL = "http://new.example.com"
	if err := s.Reload([]model.Service{updated}, defaultPage()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop 应取消已被 Reload 摘除的手动 flight: %v", err)
	}
	if err := <-probeErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("旧手动探测应收到取消: %v", err)
	}
}
