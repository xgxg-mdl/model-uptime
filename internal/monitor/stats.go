package monitor

import (
	"context"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func (s *Scheduler) buildChange(ctx context.Context, id string, service model.Service, previousOK bool, history []model.ProbeResult, r model.ProbeResult) *model.StatusChange {
	previousStatus, status := "down", "up"
	if previousOK {
		previousStatus, status = "up", "down"
	}
	statsHistory := history
	dayStart := beijingDayStart(r.TS)
	if s.store != nil {
		persisted, err := s.store.LoadResultsSinceWithPrevious(ctx, id, dayStart, r.TS)
		if err != nil {
			s.logger.Warn("查询通知今日统计失败", "svc", id, "err", err)
		} else {
			statsHistory = append(persisted, r)
		}
	}
	today := model.CalculateDailyStats(statsHistory, dayStart, r.TS)
	outageDuration := int64(0)
	if status == "up" {
		failureStart := failureStartFromResults(statsHistory, r.TS)
		if s.store != nil {
			persistedStart, err := s.store.LoadFailureStart(ctx, id, r.TS)
			if err != nil {
				s.logger.Warn("查询通知异常持续时间失败", "svc", id, "err", err)
			} else if persistedStart > 0 {
				failureStart = persistedStart
			}
		}
		if failureStart > 0 && failureStart < r.TS {
			outageDuration = r.TS - failureStart
		}
	}
	modelName := service.Model
	if modelName == "" {
		modelName = service.Name
	}
	return &model.StatusChange{
		ServiceID: id, SortOrder: service.SortOrder, Model: modelName, Provider: service.Provider, Protocol: service.Protocol,
		OK: r.OK, LatencyMS: r.LatencyMS, Error: r.Error, UptimePct: uptimePct(history),
		Samples: len(history), PreviousStatus: previousStatus, Status: status, LastTS: r.TS,
		OutageDurationSec: outageDuration, TodayUpSec: today.UpSec, TodayDownSec: today.DownSec,
		TodayDownCount: today.DownCount, TodayUptimePct: today.UptimePct(),
	}
}

func uptimePct(history []model.ProbeResult) float64 {
	if len(history) == 0 {
		return 100
	}
	ok := 0
	for _, r := range history {
		if r.OK {
			ok++
		}
	}
	return float64(ok) / float64(len(history)) * 100
}

func beijingDayStart(timestamp int64) int64 {
	current := time.Unix(timestamp, 0).In(beijingLocation)
	return time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, beijingLocation).Unix()
}

func failureStartFromResults(results []model.ProbeResult, recoveredAt int64) int64 {
	startedAt := int64(0)
	for i := len(results) - 1; i >= 0; i-- {
		result := results[i]
		if result.TS >= recoveredAt {
			continue
		}
		if result.OK {
			break
		}
		startedAt = result.TS
	}
	return startedAt
}
