package heatmap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

type repositoryStub struct {
	results map[string][]model.ProbeResult
	err     error
}

func (r repositoryStub) LoadResultsBetween(_ context.Context, id string, _, _ int64) ([]model.ProbeResult, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]model.ProbeResult(nil), r.results[id]...), nil
}

type statusStub struct{ response model.StatusResponse }

func (s statusStub) Snapshot() model.StatusResponse { return s.response }

type countingRepository struct {
	results map[string][]model.ProbeResult
	calls   int
}

func (r *countingRepository) LoadResultsBetween(_ context.Context, id string, _, _ int64) ([]model.ProbeResult, error) {
	r.calls++
	return append([]model.ProbeResult(nil), r.results[id]...), nil
}

func TestGridDimensionsAndBeijingBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	tests := []struct {
		rangeName string
		rows      int
		cells     int
		bucket    time.Duration
	}{
		{RangeDay, 4, 96, 15 * time.Minute},
		{RangeWeek, 7, 168, time.Hour},
		{RangeMonth, 30, 720, time.Hour},
	}
	for _, test := range tests {
		t.Run(test.rangeName, func(t *testing.T) {
			spec, err := makeGridSpec(test.rangeName, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(spec.rows) != test.rows || len(spec.columns) != 24 || len(spec.slots) != test.cells || spec.bucket != test.bucket {
				t.Fatalf("网格规格错误: rows=%d columns=%d cells=%d bucket=%s", len(spec.rows), len(spec.columns), len(spec.slots), spec.bucket)
			}
		})
	}
	week, _ := makeGridSpec(RangeWeek, now)
	if got := week.queryFrom.Format("2006-01-02 15:04"); got != "2026-08-12 00:00" {
		t.Fatalf("周视图起点 = %s", got)
	}
	month, _ := makeGridSpec(RangeMonth, now)
	if got := month.queryFrom.Format("2006-01-02 15:04"); got != "2026-07-20 00:00" {
		t.Fatalf("月视图起点 = %s", got)
	}
	if _, err := makeGridSpec("year", now); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("非法范围错误 = %v", err)
	}
	day, _ := makeGridSpec(RangeDay, now)
	if day.queryTo.Sub(day.queryFrom) != 24*time.Hour || !day.queryTo.Equal(now) {
		t.Fatalf("日视图范围应为截止当前时刻的精确 24 小时: %s - %s", day.queryFrom, day.queryTo)
	}
	for _, slot := range day.slots {
		if slot.start.IsZero() || !slot.end.After(slot.start) || slot.end.After(now) {
			t.Fatalf("日视图包含无效或未来时间桶: %+v", slot)
		}
	}
	crossHour := day.queryFrom.Add(55 * time.Minute)
	index := slotIndex(day, crossHour)
	if index < 0 || crossHour.Before(day.slots[index].start) || !crossHour.Before(day.slots[index].end) {
		t.Fatalf("跨小时样本映射到错误时间桶: timestamp=%s index=%d slot=%+v", crossHour, index, day.slots[index])
	}
}

func TestAggregateCellCoverageAndSeverity(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, beijingLocation)
	slot := timeSlot{start: start, end: start.Add(time.Hour)}
	result := func(ok bool, latency int64) model.ProbeResult { return model.ProbeResult{OK: ok, LatencyMS: latency} }
	tests := []struct {
		name    string
		results []model.ProbeResult
		status  string
	}{
		{name: "unobserved", status: StatusUnobserved},
		{name: "insufficient", results: []model.ProbeResult{result(true, 10)}, status: StatusInsufficient},
		{name: "healthy tolerates slow samples", results: append(repeat(result(true, 10), 9), result(true, 31_000)), status: StatusHealthy},
		{name: "warning on twenty percent slow", results: append(repeat(result(true, 10), 8), repeat(result(true, 31_000), 2)...), status: StatusWarning},
		{name: "warning on isolated failure", results: append(repeat(result(true, 10), 9), result(false, 5)), status: StatusWarning},
		{name: "failing on twenty percent failures", results: append(repeat(result(true, 10), 8), repeat(result(false, 5), 2)...), status: StatusFailing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := aggregateCell(slot, test.results, 360, 30, slot.end)
			if cell.Status != test.status {
				t.Fatalf("状态 = %s，期望 %s: %+v", cell.Status, test.status, cell)
			}
		})
	}
}

func TestCurrentCellUsesElapsedDurationForCoverage(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, beijingLocation)
	cell := aggregateCell(
		timeSlot{start: start, end: start.Add(time.Hour)},
		[]model.ProbeResult{{OK: true, LatencyMS: 20}},
		60,
		30,
		start.Add(90*time.Second),
	)
	if cell.ExpectedSamples != 2 || cell.CoveragePct != 50 || cell.Status != StatusHealthy {
		t.Fatalf("进行中单元格覆盖率错误: %+v", cell)
	}
}

func TestBuildUsesCurrentServiceThreshold(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	last := model.ProbeResult{OK: true, TS: now.Unix(), LatencyMS: 1500}
	service, err := New(repositoryStub{results: map[string][]model.ProbeResult{
		"slow": {{OK: true, TS: now.Add(-time.Minute).Unix(), LatencyMS: 1500}},
	}}, statusStub{response: model.StatusResponse{Services: []model.ServiceView{
		{ID: "slow", Model: "Slow", IntervalSec: 60, WarningSec: 1, Last: &last},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	response, err := service.Build(context.Background(), RangeDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Services) != 1 || response.Services[0].Status != StatusWarning || len(response.Services[0].Cells) != 96 {
		t.Fatalf("热力图响应错误: %+v", response)
	}
}

func TestBuildInvalidatesCacheForConfigAndServiceChanges(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	currentTime := now
	last := model.ProbeResult{OK: true, TS: now.Add(-time.Minute).Unix(), LatencyMS: 1500}
	repository := &countingRepository{results: map[string][]model.ProbeResult{
		"slow":  {{OK: true, TS: last.TS, LatencyMS: last.LatencyMS}},
		"other": nil,
	}}
	status := &statusStub{response: model.StatusResponse{Services: []model.ServiceView{
		{ID: "other", Model: "Other", SortOrder: 20, IntervalSec: 60, WarningSec: 30},
		{ID: "slow", Model: "Slow", SortOrder: 10, IntervalSec: 60, WarningSec: 30, Last: &last},
	}}}
	service, err := New(repository, status)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return currentTime }

	first, err := service.Build(context.Background(), RangeDay)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 || len(first.Services) != 2 || first.Services[0].ID != "slow" || first.Services[0].Status != StatusHealthy {
		t.Fatalf("首次构建未按 sort_order 排序: calls=%d response=%+v", repository.calls, first.Services)
	}
	currentTime = currentTime.Add(time.Second)
	cached, err := service.Build(context.Background(), RangeDay)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 || cached.GeneratedAt != first.GeneratedAt {
		t.Fatalf("滚动窗口边界变化但 TTL 未到时应命中缓存: calls=%d generated_at=%d", repository.calls, cached.GeneratedAt)
	}

	status.response.Services[1].WarningSec = 1
	changed, err := service.Build(context.Background(), RangeDay)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 4 || changed.Services[0].Status != StatusWarning {
		t.Fatalf("warning_sec 变化后缓存未失效: calls=%d response=%+v", repository.calls, changed.Services)
	}

	status.response.Services = status.response.Services[:1]
	withoutDisabled, err := service.Build(context.Background(), RangeDay)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 5 || len(withoutDisabled.Services) != 1 || withoutDisabled.Services[0].ID != "other" {
		t.Fatalf("禁用服务从快照移除后仍出现在缓存中: calls=%d response=%+v", repository.calls, withoutDisabled.Services)
	}
}

func repeat(result model.ProbeResult, count int) []model.ProbeResult {
	results := make([]model.ProbeResult, count)
	for index := range results {
		results[index] = result
	}
	return results
}
