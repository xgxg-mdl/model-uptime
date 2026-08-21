// Package timeline 将原始探测证据投影为完整的 Status Timeline Slots。
package timeline

import (
	"sort"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

const (
	defaultIntervalSec = 60
	defaultWarningSec  = 30
)

// Window 是按探测 interval 对齐的半开完整时间窗。
type Window struct {
	Start int64
	End   int64
}

// Input 是构造一个服务的 Status Timeline Slots 所需的全部观测证据。
// AsOf 显式传入，使 module 内部不读取时钟，并让 interface 成为唯一测试面。
type Input struct {
	AsOf           int64
	IntervalSec    int
	SlotCount      int
	WarningSec     int
	ObservedSince  int64
	ProbeStartedAt int64
	History        []model.ProbeResult
	Pauses         []model.PauseSpan
}

// Projection 是状态页消费的权威时间线及同一完整窗口内的样本可用率。
type Projection struct {
	Slots     []model.StatusTimelineSlot
	UptimePct float64
}

// CompletedWindow 返回 AsOf 所在 partial interval 之前的固定窗口。
func CompletedWindow(asOf int64, intervalSec, slotCount int) Window {
	interval := normalizedInterval(intervalSec)
	end := asOf - floorRemainder(asOf, int64(interval))
	if slotCount <= 0 {
		return Window{Start: end, End: end}
	}
	return Window{Start: end - int64(slotCount)*int64(interval), End: end}
}

// Project 将观测结果、暂停区间和在途探测聚合为固定数量的完整时间桶。
func Project(input Input) Projection {
	window := CompletedWindow(input.AsOf, input.IntervalSec, input.SlotCount)
	if input.SlotCount <= 0 {
		return Projection{Slots: []model.StatusTimelineSlot{}, UptimePct: 100}
	}

	interval := int64(normalizedInterval(input.IntervalSec))
	slots := make([]model.StatusTimelineSlot, input.SlotCount)
	for index := range slots {
		slots[index] = model.StatusTimelineSlot{
			StartTS: window.Start + int64(index)*interval,
			EndTS:   window.Start + int64(index+1)*interval,
		}
	}

	markPauses(slots, input.Pauses, input.AsOf)
	history := sortedHistory(input.History)
	projectResults(
		slots,
		history,
		input.Pauses,
		input.ProbeStartedAt,
		input.AsOf,
		interval,
		normalizedWarning(input.WarningSec),
	)
	markActiveProbe(slots, input.ProbeStartedAt, input.AsOf, window.End)
	markMissing(slots, input.ObservedSince)

	return Projection{
		Slots:     slots,
		UptimePct: uptimePct(history, window),
	}
}

func markPauses(slots []model.StatusTimelineSlot, pauses []model.PauseSpan, asOf int64) {
	for _, pause := range pauses {
		end := pause.To
		if end == 0 {
			end = asOf
		}
		if end <= pause.From {
			continue
		}
		for index := range slots {
			if overlaps(pause.From, end, slots[index].StartTS, slots[index].EndTS) {
				slots[index].Status = model.StatusTimelinePaused
			}
		}
	}
}

func projectResults(
	slots []model.StatusTimelineSlot,
	history []model.ProbeResult,
	pauses []model.PauseSpan,
	probeStartedAt, asOf, interval int64,
	warningSec int,
) {
	nextStarts := nextDistinctStarts(history)
	for resultIndex, result := range history {
		coverageStart := resultStartedAt(result)
		coverageEnd := max(coverageStart+interval, result.TS)
		if nextStarts[resultIndex] > coverageStart {
			coverageEnd = min(coverageEnd, nextStarts[resultIndex])
		}
		if probeStartedAt > coverageStart {
			coverageEnd = min(coverageEnd, probeStartedAt)
		}
		coverageEnd = truncateAtPause(coverageStart, coverageEnd, pauses, asOf)
		state := resultState(result, warningSec)

		for slotIndex := range slots {
			slot := &slots[slotIndex]
			if slot.Status == model.StatusTimelinePaused ||
				!overlaps(coverageStart, coverageEnd, slot.StartTS, slot.EndTS) {
				continue
			}
			slot.ObservationCount++
			if shouldReplace(slot, state, result) {
				copy := result
				slot.Status = state
				slot.Result = &copy
			}
		}
	}
}

func nextDistinctStarts(history []model.ProbeResult) []int64 {
	nextStarts := make([]int64, len(history))
	for index := len(history) - 2; index >= 0; index-- {
		current := resultStartedAt(history[index])
		next := resultStartedAt(history[index+1])
		if next > current {
			nextStarts[index] = next
		} else {
			nextStarts[index] = nextStarts[index+1]
		}
	}
	return nextStarts
}

func truncateAtPause(start, end int64, pauses []model.PauseSpan, asOf int64) int64 {
	for _, pause := range pauses {
		pauseEnd := pause.To
		if pauseEnd == 0 {
			pauseEnd = asOf
		}
		if pauseEnd <= pause.From {
			continue
		}
		if overlaps(start, end, pause.From, pauseEnd) {
			end = min(end, max(start, pause.From))
		}
	}
	return end
}

func markActiveProbe(slots []model.StatusTimelineSlot, startedAt, asOf, windowEnd int64) {
	if startedAt <= 0 || startedAt >= windowEnd {
		return
	}
	for index := range slots {
		slot := &slots[index]
		if slot.Status == model.StatusTimelinePaused ||
			!overlaps(startedAt, asOf, slot.StartTS, slot.EndTS) {
			continue
		}
		slot.Status = model.StatusTimelineProbing
		slot.ProbeStartedAt = startedAt
	}
}

func markMissing(slots []model.StatusTimelineSlot, observedSince int64) {
	for index := range slots {
		if slots[index].Status != "" {
			continue
		}
		if observedSince > 0 && slots[index].StartTS < observedSince {
			slots[index].Status = model.StatusTimelineNotStarted
		} else {
			slots[index].Status = model.StatusTimelineUnobserved
		}
	}
}

func sortedHistory(history []model.ProbeResult) []model.ProbeResult {
	ordered := append([]model.ProbeResult(nil), history...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftStart, rightStart := resultStartedAt(ordered[left]), resultStartedAt(ordered[right])
		if leftStart != rightStart {
			return leftStart < rightStart
		}
		return ordered[left].TS < ordered[right].TS
	})
	return ordered
}

func shouldReplace(slot *model.StatusTimelineSlot, state model.StatusTimelineState, result model.ProbeResult) bool {
	if slot.Result == nil {
		return true
	}
	currentSeverity, nextSeverity := severity(slot.Status), severity(state)
	return nextSeverity > currentSeverity ||
		(nextSeverity == currentSeverity && result.TS >= slot.Result.TS)
}

func resultState(result model.ProbeResult, warningSec int) model.StatusTimelineState {
	if !result.OK {
		return model.StatusTimelineFailing
	}
	if result.LatencyMS > int64(warningSec)*1000 {
		return model.StatusTimelineSlow
	}
	return model.StatusTimelineHealthy
}

func severity(state model.StatusTimelineState) int {
	switch state {
	case model.StatusTimelineHealthy:
		return 1
	case model.StatusTimelineSlow:
		return 2
	case model.StatusTimelineFailing:
		return 3
	default:
		return 0
	}
}

func uptimePct(history []model.ProbeResult, window Window) float64 {
	samples, successful := 0, 0
	for _, result := range history {
		startedAt := resultStartedAt(result)
		if startedAt < window.Start || startedAt >= window.End {
			continue
		}
		samples++
		if result.OK {
			successful++
		}
	}
	if samples == 0 {
		return 100
	}
	return float64(successful) / float64(samples) * 100
}

func resultStartedAt(result model.ProbeResult) int64 {
	if result.StartedAt > 0 {
		return result.StartedAt
	}
	return result.TS
}

func overlaps(leftStart, leftEnd, rightStart, rightEnd int64) bool {
	return leftStart < rightEnd && leftEnd > rightStart
}

func normalizedInterval(intervalSec int) int {
	if intervalSec <= 0 {
		return defaultIntervalSec
	}
	return intervalSec
}

func normalizedWarning(warningSec int) int {
	if warningSec <= 0 {
		return defaultWarningSec
	}
	return warningSec
}

func floorRemainder(value, divisor int64) int64 {
	remainder := value % divisor
	if remainder < 0 {
		return remainder + divisor
	}
	return remainder
}
