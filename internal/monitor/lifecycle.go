package monitor

import (
	"context"
	"errors"
	"time"
)

const (
	tickInterval  = time.Second
	purgeInterval = time.Hour
	retention     = 30 * 24 * time.Hour
)

var (
	ErrAlreadyStarted = errors.New("monitor has already been started")
	ErrStopped        = errors.New("monitor is stopping or has stopped")
)

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleRunning
	lifecycleStopping
	lifecycleStopped
)

// Start 绑定根 context 并启动调度循环。Scheduler 生命周期只能启动一次。
func (s *Scheduler) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	s.lifecycleMu.Lock()
	if s.lifecycle != lifecycleNew {
		s.lifecycleMu.Unlock()
		return ErrAlreadyStarted
	}
	s.rootCtx, s.cancel = context.WithCancel(parent)
	s.lifecycle = lifecycleRunning
	root := s.rootCtx
	s.runWG.Add(1)
	s.lifecycleMu.Unlock()

	go func() {
		s.run(root)
		s.runWG.Done()
		s.beginFinalization()
	}()
	return nil
}

// Stop 取消根 context，并等待调度循环及全部在途探测退出。
// ctx 到期时明确返回错误；后台清理仍会在探测最终退出后完成。
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	cancelPreStartFlights := false
	if s.lifecycle == lifecycleStopped {
		s.lifecycleMu.Unlock()
		return nil
	}
	if s.lifecycle == lifecycleNew {
		s.lifecycle = lifecycleStopping
		cancelPreStartFlights = true
	} else if s.lifecycle == lifecycleRunning {
		s.lifecycle = lifecycleStopping
		if s.cancel != nil {
			s.cancel()
		}
	}
	s.lifecycleMu.Unlock()
	if cancelPreStartFlights {
		s.cancelActiveManualFlights()
	}
	s.beginFinalization()

	select {
	case <-s.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) cancelActiveManualFlights() {
	var cancels []context.CancelFunc
	s.mu.Lock()
	for flight := range s.activeFlights {
		if flight.cancel == nil {
			continue
		}
		cancels = append(cancels, flight.cancel)
		flight.cancel = nil
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Scheduler) beginFinalization() {
	s.finalizeOnce.Do(func() {
		s.lifecycleMu.Lock()
		if s.lifecycle == lifecycleRunning {
			s.lifecycle = lifecycleStopping
		}
		s.lifecycleMu.Unlock()
		go func() {
			s.runWG.Wait()
			s.operationWG.Wait()
			s.wg.Wait()
			s.lifecycleMu.Lock()
			s.lifecycle = lifecycleStopped
			s.lifecycleMu.Unlock()
			close(s.stopped)
		}()
	})
}

func (s *Scheduler) beginOperation() (context.Context, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	var ctx context.Context
	switch s.lifecycle {
	case lifecycleNew:
		ctx = context.Background()
	case lifecycleRunning:
		if s.rootCtx == nil || s.rootCtx.Err() != nil {
			return nil, ErrStopped
		}
		ctx = s.rootCtx
	default:
		return nil, ErrStopped
	}
	s.operationWG.Add(1)
	return ctx, nil
}

func (s *Scheduler) run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	lastPurge := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkDue()
			if time.Since(lastPurge) >= purgeInterval {
				s.purge(ctx)
				lastPurge = time.Now()
			}
		}
	}
}

func (s *Scheduler) purge(ctx context.Context) {
	if s.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if n, err := s.store.PurgeBefore(ctx, time.Now().Add(-retention)); err != nil {
		if contextError(ctx) != nil {
			return
		}
		s.logger.Warn("清理历史失败", "err", err)
	} else if n > 0 {
		s.logger.Debug("清理历史", "rows", n)
	}
}
