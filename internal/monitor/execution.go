package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func (s *Scheduler) checkDue() {
	s.reloadGate.RLock()
	s.lifecycleMu.Lock()
	var probeCtx context.Context
	switch s.lifecycle {
	case lifecycleNew:
		// 包内同步测试和启动前的显式触发仍可使用调度入口。
		probeCtx = context.Background()
	case lifecycleRunning:
		probeCtx = s.rootCtx
		if probeCtx == nil || probeCtx.Err() != nil {
			s.lifecycleMu.Unlock()
			s.reloadGate.RUnlock()
			return
		}
	default:
		s.lifecycleMu.Unlock()
		s.reloadGate.RUnlock()
		return
	}

	now := time.Now()
	s.mu.Lock()
	var due []probeJob
	for _, id := range s.order {
		st := s.states[id]
		if st == nil || !st.svc.IsEnabled() || st.flight != nil ||
			now.Sub(st.lastProbe) < time.Duration(st.svc.IntervalSec)*time.Second {
			continue
		}
		select {
		case s.probeSlots <- struct{}{}:
		default:
			continue
		}
		flight := &probeFlight{done: make(chan struct{})}
		st.lastProbe = now
		st.flight = flight
		s.activeFlights[flight] = struct{}{}
		due = append(due, probeJob{
			svc: st.svc, generation: st.generation, flight: flight, ctx: probeCtx,
		})
	}
	if len(due) > 0 {
		s.wg.Add(len(due))
	}
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	s.reloadGate.RUnlock()

	for _, job := range due {
		go s.executeProbe(job)
	}
}

func (s *Scheduler) executeProbe(job probeJob) {
	defer s.wg.Done()

	ctx := job.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if job.manual {
		select {
		case s.probeSlots <- struct{}{}:
		case <-ctx.Done():
			s.finishFlight(job, nil, contextError(ctx))
			return
		}
	}
	defer func() { <-s.probeSlots }()
	if err := contextError(ctx); err != nil {
		s.finishFlight(job, nil, err)
		return
	}

	startedAt := time.Now().Unix()
	probed := s.probeFn(ctx, &job.svc)
	result := &model.ProbeResult{
		OK: probed.OK, TS: time.Now().Unix(), StartedAt: startedAt,
		LatencyMS: probed.LatencyMS, Error: probed.Error,
	}
	var (
		probeErr  error
		flightRes = result
	)
	if err := contextError(ctx); err != nil {
		flightRes = nil
		probeErr = err
	} else {
		s.recordGenerationContext(ctx, job.svc.ID, job.generation, *result)
	}

	s.finishFlight(job, flightRes, probeErr)
}

// finishFlight 与 Reload 共享读写门闩，防止 Reload 复制到已经完成但尚未
// 从 serviceState 清除的 flight。
func (s *Scheduler) finishFlight(job probeJob, result *model.ProbeResult, err error) {
	s.reloadGate.RLock()
	s.mu.Lock()
	job.flight.result = result
	job.flight.err = err
	cancel := job.flight.cancel
	job.flight.cancel = nil
	delete(s.activeFlights, job.flight)
	if state := s.states[job.svc.ID]; state != nil &&
		state.generation == job.generation && state.flight == job.flight {
		state.flight = nil
	}
	close(job.flight.done)
	s.mu.Unlock()
	s.reloadGate.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Scheduler) ProbeNow(ctx context.Context, id string) (*model.ProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	flight, root, job, err := s.installManualProbe(ctx, id)
	if err != nil {
		return nil, err
	}
	waitCtx, cancelWait := mergeContexts(ctx, root)
	if job != nil {
		go s.executeProbe(*job)
	}
	result, waitErr := waitForFlight(waitCtx, flight)
	cancelWait()
	s.releaseFlightWaiter(flight)
	return result, waitErr
}

// installManualProbe 在等待全局槽位前先登记 flight，使同服务的并发调用都等待
// 同一个 owner。job 为 nil 表示已有 owner，调用方只需等待现有 flight。
func (s *Scheduler) installManualProbe(ctx context.Context, id string) (*probeFlight, context.Context, *probeJob, error) {
	s.reloadGate.RLock()
	defer s.reloadGate.RUnlock()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	var root context.Context
	switch s.lifecycle {
	case lifecycleNew:
	case lifecycleRunning:
		root = s.rootCtx
	default:
		return nil, nil, nil, ErrStopped
	}
	if err := contextError(ctx); err != nil {
		return nil, nil, nil, err
	}
	if root != nil {
		if err := contextError(root); err != nil {
			return nil, nil, nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[id]
	if state == nil {
		return nil, nil, nil, fmt.Errorf("服务不存在: %s", id)
	}
	if state.flight != nil {
		state.flight.waiters++
		return state.flight, root, nil, nil
	}
	probeParent := root
	if probeParent == nil {
		probeParent = context.Background()
	}
	probeCtx, cancelProbe := context.WithCancel(probeParent)
	flight := &probeFlight{done: make(chan struct{}), waiters: 1, cancel: cancelProbe}
	state.flight = flight
	s.activeFlights[flight] = struct{}{}
	state.lastProbe = time.Now()
	job := &probeJob{
		svc: state.svc, generation: state.generation, flight: flight,
		ctx: probeCtx, manual: true,
	}
	s.wg.Add(1)
	return flight, root, job, nil
}

func (s *Scheduler) releaseFlightWaiter(flight *probeFlight) {
	var cancel context.CancelFunc
	s.mu.Lock()
	if flight.waiters > 0 {
		flight.waiters--
	}
	if flight.waiters == 0 && flight.cancel != nil {
		cancel = flight.cancel
		flight.cancel = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func mergeContexts(ctx, root context.Context) (context.Context, context.CancelFunc) {
	if root == nil || root == ctx {
		return context.WithCancel(ctx)
	}
	merged, cancel := context.WithCancelCause(ctx)
	stopRoot := context.AfterFunc(root, func() {
		cancel(context.Cause(root))
	})
	return merged, func() {
		stopRoot()
		cancel(context.Canceled)
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func waitForFlight(ctx context.Context, flight *probeFlight) (*model.ProbeResult, error) {
	readResult := func() (*model.ProbeResult, error) {
		if flight.result == nil {
			return nil, flight.err
		}
		result := *flight.result
		return &result, flight.err
	}
	select {
	case <-flight.done:
		return readResult()
	default:
	}
	select {
	case <-flight.done:
		return readResult()
	case <-ctx.Done():
		// 完成与取消同时发生时优先交付已经完成的共享结果。
		select {
		case <-flight.done:
			return readResult()
		default:
			return nil, contextError(ctx)
		}
	}
}
