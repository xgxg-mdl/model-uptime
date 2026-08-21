package timeline_test

import (
	"reflect"
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/timeline"
)

func TestProjectUsesFixedCompletedIntervals(t *testing.T) {
	history := make([]model.ProbeResult, 0, 5)
	for _, startedAt := range []int64{145, 205, 265, 325, 385} {
		history = append(history, result(startedAt, startedAt+5, true, 10))
	}

	projection := timeline.Project(timeline.Input{
		AsOf: 420, IntervalSec: 60, SlotCount: 5, WarningSec: 30,
		ObservedSince: 145, History: history,
	})

	assertStates(t, projection.Slots, []model.StatusTimelineState{
		model.StatusTimelineHealthy,
		model.StatusTimelineHealthy,
		model.StatusTimelineHealthy,
		model.StatusTimelineHealthy,
		model.StatusTimelineHealthy,
	})
	wantRanges := [][2]int64{{120, 180}, {180, 240}, {240, 300}, {300, 360}, {360, 420}}
	for index, slot := range projection.Slots {
		if got := [2]int64{slot.StartTS, slot.EndTS}; got != wantRanges[index] {
			t.Errorf("slot %d 时间范围 = %v，期望 %v", index, got, wantRanges[index])
		}
	}

	early := timeline.CompletedWindow(421, 60, 5)
	late := timeline.CompletedWindow(479, 60, 5)
	if early != late {
		t.Fatalf("同一 partial interval 内窗口发生移动: early=%+v late=%+v", early, late)
	}
}

func TestProjectExcludesCurrentPartialIntervalUntilItCompletes(t *testing.T) {
	input := timeline.Input{
		AsOf: 479, IntervalSec: 60, SlotCount: 1, WarningSec: 30, ObservedSince: 1,
		History:        []model.ProbeResult{result(420, 425, true, 5_000)},
		ProbeStartedAt: 420,
	}
	partial := timeline.Project(input)
	if slot := partial.Slots[0]; slot.StartTS != 360 || slot.Status != model.StatusTimelineUnobserved {
		t.Fatalf("当前 partial interval 不应进入时间线: %+v", slot)
	}

	input.AsOf = 480
	input.ProbeStartedAt = 0
	completed := timeline.Project(input)
	if slot := completed.Slots[0]; slot.StartTS != 420 || slot.Status != model.StatusTimelineHealthy {
		t.Fatalf("interval 完成后应推进并投影结果: %+v", slot)
	}
}

func TestProjectAggregatesLifecycleAndWorstObservation(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 420, IntervalSec: 60, SlotCount: 5, WarningSec: 30, ObservedSince: 170,
		History: []model.ProbeResult{
			result(190, 195, true, 10),
			result(250, 255, true, 20),
			result(250, 255, false, 20),
		},
		Pauses: []model.PauseSpan{{From: 300, To: 360}},
	})

	assertStates(t, projection.Slots, []model.StatusTimelineState{
		model.StatusTimelineNotStarted,
		model.StatusTimelineHealthy,
		model.StatusTimelineFailing,
		model.StatusTimelinePaused,
		model.StatusTimelineUnobserved,
	})
	selected := projection.Slots[2]
	if selected.ObservationCount != 3 || selected.Result == nil || selected.Result.OK {
		t.Fatalf("同桶应保留 3 个覆盖周期并选择失败结果: %+v", selected)
	}
}

func TestProjectMarksElapsedActiveProbeSlots(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 480, IntervalSec: 60, SlotCount: 5, WarningSec: 30, ObservedSince: 100,
		History:        []model.ProbeResult{result(359, 421, true, 62_000)},
		ProbeStartedAt: 421,
	})

	last := projection.Slots[len(projection.Slots)-1]
	if last.Status != model.StatusTimelineProbing || last.ProbeStartedAt != 421 {
		t.Fatalf("在途探测未覆盖已完整的时间桶: %+v", last)
	}
	if last.ObservationCount != 1 || last.Result == nil {
		t.Fatalf("probing 应保留底层已完成周期证据: %+v", last)
	}
}

func TestProjectUsesActiveProbeAsNextCycleBoundary(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 240, IntervalSec: 60, SlotCount: 1, WarningSec: 30, ObservedSince: 1,
		History:        []model.ProbeResult{result(170, 175, false, 5_000)},
		ProbeStartedAt: 180,
	})

	slot := projection.Slots[0]
	if slot.Status != model.StatusTimelineProbing || slot.ProbeStartedAt != 180 {
		t.Fatalf("active probe 应覆盖已完整时间桶: %+v", slot)
	}
	if slot.ObservationCount != 0 || slot.Result != nil {
		t.Fatalf("上一周期不得越过 active probe 启动边界: %+v", slot)
	}
}

func TestProjectExpandsCyclesWithoutHidingRealGaps(t *testing.T) {
	tests := []struct {
		name       string
		history    []model.ProbeResult
		wantStates []model.StatusTimelineState
	}{
		{
			name: "连续慢周期覆盖请求耗时和正常调度间隔",
			history: []model.ProbeResult{
				result(120, 190, true, 70_000),
				result(191, 260, true, 69_000),
				result(261, 330, true, 69_000),
				result(331, 400, true, 69_000),
			},
			wantStates: []model.StatusTimelineState{
				model.StatusTimelineSlow,
				model.StatusTimelineSlow,
				model.StatusTimelineSlow,
				model.StatusTimelineSlow,
				model.StatusTimelineSlow,
			},
		},
		{
			name: "没有周期覆盖的完整时间桶仍是缺失",
			history: []model.ProbeResult{
				result(120, 130, true, 10_000),
				result(300, 310, true, 10_000),
			},
			wantStates: []model.StatusTimelineState{
				model.StatusTimelineHealthy,
				model.StatusTimelineUnobserved,
				model.StatusTimelineUnobserved,
				model.StatusTimelineHealthy,
				model.StatusTimelineUnobserved,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := timeline.Project(timeline.Input{
				AsOf: 420, IntervalSec: 60, SlotCount: 5, WarningSec: 30,
				ObservedSince: 120, History: test.history,
			})
			assertStates(t, projection.Slots, test.wantStates)
		})
	}
}

func TestProjectUsesStrictWarningThresholdAndLatestTieBreaker(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 180, IntervalSec: 60, SlotCount: 1, WarningSec: 30, ObservedSince: 120,
		History: []model.ProbeResult{
			result(120, 130, true, 30_000),
			result(120, 131, true, 30_001),
			result(120, 132, true, 31_000),
		},
	})

	slot := projection.Slots[0]
	if slot.Status != model.StatusTimelineSlow || slot.ObservationCount != 3 {
		t.Fatalf("阈值或聚合结果错误: %+v", slot)
	}
	if slot.Result == nil || slot.Result.TS != 132 {
		t.Fatalf("同级状态应选择完成时间最新的结果: %+v", slot.Result)
	}
}

func TestProjectTruncatesCoverageAtNextCycleAndPause(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 420, IntervalSec: 60, SlotCount: 5, WarningSec: 30, ObservedSince: 120,
		History: []model.ProbeResult{
			result(120, 400, true, 280_000),
			result(181, 190, false, 9_000),
		},
		Pauses: []model.PauseSpan{{From: 250, To: 300}},
	})

	assertStates(t, projection.Slots, []model.StatusTimelineState{
		model.StatusTimelineSlow,
		model.StatusTimelineFailing,
		model.StatusTimelinePaused,
		model.StatusTimelineUnobserved,
		model.StatusTimelineUnobserved,
	})
}

func TestProjectTruncatesEverySameStartResultAtNextDistinctCycle(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 180, IntervalSec: 60, SlotCount: 1, WarningSec: 30, ObservedSince: 1,
		History: []model.ProbeResult{
			result(100, 105, false, 5_000),
			result(100, 110, true, 10_000),
			result(120, 125, true, 5_000),
		},
	})

	slot := projection.Slots[0]
	if slot.Status != model.StatusTimelineHealthy || slot.ObservationCount != 1 {
		t.Fatalf("同启动时间的旧周期越过下一次不同启动时间: %+v", slot)
	}
}

func TestProjectIgnoresZeroLengthAndInvalidPauses(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 300, IntervalSec: 60, SlotCount: 3, WarningSec: 30, ObservedSince: 1,
		Pauses: []model.PauseSpan{
			{From: 200, To: 200},
			{From: 260, To: 250},
		},
	})

	assertStates(t, projection.Slots, []model.StatusTimelineState{
		model.StatusTimelineUnobserved,
		model.StatusTimelineUnobserved,
		model.StatusTimelineUnobserved,
	})
}

func TestProjectTruncatesResultsThatStartAtOrInsidePause(t *testing.T) {
	for _, startedAt := range []int64{100, 110} {
		projection := timeline.Project(timeline.Input{
			AsOf: 180, IntervalSec: 60, SlotCount: 1, WarningSec: 30, ObservedSince: 1,
			History: []model.ProbeResult{result(startedAt, startedAt+5, true, 5_000)},
			Pauses:  []model.PauseSpan{{From: 100, To: 120}},
		})

		slot := projection.Slots[0]
		if slot.Status != model.StatusTimelineUnobserved || slot.ObservationCount != 0 || slot.Result != nil {
			t.Fatalf("暂停起点附近的结果不应穿透恢复边界，started_at=%d slot=%+v", startedAt, slot)
		}
	}
}

func TestProjectCalculatesUptimeFromCompletedWindowSamples(t *testing.T) {
	projection := timeline.Project(timeline.Input{
		AsOf: 421, IntervalSec: 60, SlotCount: 2, WarningSec: 30, ObservedSince: 1,
		History: []model.ProbeResult{
			result(239, 240, false, 1), // 窗口起点之前的延续证据
			result(240, 245, false, 1), // 完整窗口起点，计入
			result(359, 365, true, 1),
			result(360, 361, false, 1), // 当前 partial interval，排除
		},
	})

	if projection.UptimePct != 50 {
		t.Fatalf("完整窗口样本可用率 = %v，期望 50", projection.UptimePct)
	}
}

func TestProjectSupportsLegacyResultsAndDoesNotMutateInput(t *testing.T) {
	history := []model.ProbeResult{
		{OK: false, TS: 175, LatencyMS: 1},
		{OK: true, TS: 125, LatencyMS: 1},
	}
	original := append([]model.ProbeResult(nil), history...)
	projection := timeline.Project(timeline.Input{
		AsOf: 180, IntervalSec: 60, SlotCount: 1, WarningSec: 30,
		ObservedSince: 120, History: history,
	})

	if projection.Slots[0].Status != model.StatusTimelineFailing {
		t.Fatalf("旧记录应以完成时间作为开始时间: %+v", projection.Slots[0])
	}
	if !reflect.DeepEqual(history, original) {
		t.Fatalf("Project 修改了调用方历史: got=%+v want=%+v", history, original)
	}
}

func TestProjectHandlesEmptyWindow(t *testing.T) {
	projection := timeline.Project(timeline.Input{AsOf: 420, SlotCount: 0})
	if projection.Slots == nil || len(projection.Slots) != 0 || projection.UptimePct != 100 {
		t.Fatalf("空窗口投影应返回稳定零值: %+v", projection)
	}
}

func result(startedAt, completedAt int64, ok bool, latencyMS int64) model.ProbeResult {
	return model.ProbeResult{
		StartedAt: startedAt,
		TS:        completedAt,
		OK:        ok,
		LatencyMS: latencyMS,
	}
}

func assertStates(t *testing.T, slots []model.StatusTimelineSlot, want []model.StatusTimelineState) {
	t.Helper()
	got := make([]model.StatusTimelineState, len(slots))
	for index, slot := range slots {
		got[index] = slot.Status
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("时间线状态 = %v，期望 %v", got, want)
	}
}
