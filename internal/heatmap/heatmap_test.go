package heatmap

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	mu      sync.Mutex
	calls   int
}

type blockingRepository struct {
	started chan string
	release chan struct{}
}

func (r blockingRepository) LoadResultsBetween(_ context.Context, id string, _, _ int64) ([]model.ProbeResult, error) {
	r.started <- id
	<-r.release
	return nil, nil
}

func (r *countingRepository) LoadResultsBetween(_ context.Context, id string, _, _ int64) ([]model.ProbeResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return append([]model.ProbeResult(nil), r.results[id]...), nil
}

func TestGridUsesCompleteBeijingCalendarDays(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	tests := []struct {
		rangeName string
		start     string
		rows      int
		cells     int
		bucket    time.Duration
	}{
		{Range1D, "2026-08-18 00:00", 4, 96, 15 * time.Minute},
		{Range7D, "2026-08-12 00:00", 7, 168, time.Hour},
		{Range30D, "2026-07-20 00:00", 30, 720, time.Hour},
	}
	for _, test := range tests {
		t.Run(test.rangeName, func(t *testing.T) {
			spec, err := makeGridSpec(test.rangeName, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := spec.queryFrom.Format("2006-01-02 15:04"); got != test.start {
				t.Fatalf("范围起点 = %s，期望 %s", got, test.start)
			}
			if got := spec.slots[len(spec.slots)-1].end.Format("2006-01-02 15:04"); got != "2026-08-19 00:00" {
				t.Fatalf("范围终点 = %s，期望明日零点", got)
			}
			if len(spec.rows) != test.rows || len(spec.columns) != 24 || len(spec.slots) != test.cells || spec.bucket != test.bucket {
				t.Fatalf("网格 = %d 行 %d 格，期望 %d 行", len(spec.rows), len(spec.slots), test.rows)
			}
			if test.rangeName == Range1D {
				if got := spec.rows; got[0] != "00" || got[1] != "15" || got[2] != "30" || got[3] != "45" {
					t.Fatalf("1d 分钟行错误: %v", got)
				}
			} else if spec.rows[0] != spec.queryFrom.Format("01-02") || spec.rows[len(spec.rows)-1] != "08-18" {
				t.Fatalf("行日期未覆盖完整自然日范围: %v", spec.rows)
			}
			if !spec.queryTo.Equal(now.Add(time.Second)) {
				t.Fatalf("查询终点 = %s，期望 %s", spec.queryTo, now.Add(time.Second))
			}
			expectedIndex := (test.rows-1)*24 + 17
			if test.rangeName == Range1D {
				expectedIndex = 17
			}
			if index := slotIndex(spec, now.Add(-time.Minute)); index != expectedIndex {
				t.Fatalf("当前样本映射 = %d，期望 %d", index, expectedIndex)
			}
			if index := slotIndex(spec, now.Add(2*time.Second)); index != -1 {
				t.Fatalf("当前查询终点之后的样本不应进入网格: index=%d", index)
			}
		})
	}
	for _, oldRange := range []string{"day", "week", "month", "year"} {
		if _, err := makeGridSpec(oldRange, now); !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("旧范围 %q 应被拒绝，错误 = %v", oldRange, err)
		}
	}
	utcSpec, err := makeGridSpec(Range1D, now.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := utcSpec.queryFrom.Format("2006-01-02 15:04 -0700"); got != "2026-08-18 00:00 +0800" {
		t.Fatalf("UTC 输入未按北京时间取自然日边界: %s", got)
	}
	complete1D, err := makeGridSpec(Range1D, now)
	if err != nil {
		t.Fatal(err)
	}
	complete1D.queryTo = complete1D.slots[len(complete1D.slots)-1].end
	for _, test := range []struct {
		offset time.Duration
		index  int
	}{
		{0, 0},
		{14 * time.Minute, 0},
		{15 * time.Minute, 24},
		{23*time.Hour + 59*time.Minute, 95},
	} {
		if got := slotIndex(complete1D, complete1D.queryFrom.Add(test.offset)); got != test.index {
			t.Fatalf("1d 时间桶索引 = %d，期望 %d（offset=%s）", got, test.index, test.offset)
		}
	}
}

func TestBuildAggregatesServicesConcurrentlyAndKeepsSortOrder(t *testing.T) {
	repository := blockingRepository{started: make(chan string, buildConcurrency), release: make(chan struct{})}
	services := make([]model.ServiceView, buildConcurrency)
	for index := range services {
		uid := fmt.Sprintf("svc-%d", index)
		services[index] = model.ServiceView{ServiceUID: uid, Model: uid, SortOrder: buildConcurrency - index, IntervalSec: 60, WarningSec: 30}
	}
	builder, err := New(repository, statusStub{response: model.StatusResponse{Services: services}})
	if err != nil {
		t.Fatal(err)
	}
	builder.now = func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, beijingLocation) }
	done := make(chan struct{})
	var response Response
	var buildErr error
	go func() {
		response, buildErr = builder.Build(context.Background(), Range1D)
		close(done)
	}()

	for range buildConcurrency {
		select {
		case <-repository.started:
		case <-time.After(time.Second):
			t.Fatal("模型查询未并行启动")
		}
	}
	close(repository.release)
	<-done
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	for index, service := range response.Services {
		if service.Model != fmt.Sprintf("svc-%d", buildConcurrency-1-index) {
			t.Fatalf("并行聚合打乱 sort_order: %+v", response.Services)
		}
	}
}

func TestFutureCellsRemainUnobserved(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	for _, rangeName := range []string{Range1D, Range7D, Range30D} {
		t.Run(rangeName, func(t *testing.T) {
			spec, err := makeGridSpec(rangeName, now)
			if err != nil {
				t.Fatal(err)
			}
			view := buildServiceView(model.ServiceView{ServiceUID: "svc", Model: "svc", IntervalSec: 60, WarningSec: 30}, nil, spec)
			last := view.Cells[len(view.Cells)-1]
			if last.Status != StatusUnobserved || last.ExpectedSamples != 0 || last.ActualSamples != 0 {
				t.Fatalf("未来最后一格应无观测: %+v", last)
			}
		})
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
		{name: "insufficient overrides failure", results: []model.ProbeResult{result(false, 10)}, status: StatusInsufficient},
		{name: "healthy at latency threshold", results: repeat(result(true, 30_000), 10), status: StatusHealthy},
		{name: "healthy tolerates isolated fluctuation", results: append(repeat(result(true, 10), 59), result(false, 5)), status: StatusHealthy},
		{name: "healthy below five percent impact", results: append(repeat(result(true, 10), 96), repeat(result(false, 5), 4)...), status: StatusHealthy},
		{name: "warning when p95 exceeds latency threshold", results: append(repeat(result(true, 10), 9), result(true, 31_000)), status: StatusWarning},
		{name: "warning at five percent impacted samples", results: append(repeat(result(true, 10), 95), repeat(result(false, 5), 5)...), status: StatusWarning},
		{name: "warning combines failures and slow responses", results: append(append(repeat(result(true, 10), 95), repeat(result(true, 31_000), 2)...), repeat(result(false, 5), 3)...), status: StatusWarning},
		{name: "warning below twenty percent failures", results: append(repeat(result(true, 10), 81), repeat(result(false, 5), 19)...), status: StatusWarning},
		{name: "failing at twenty percent failures", results: append(repeat(result(true, 10), 80), repeat(result(false, 5), 20)...), status: StatusFailing},
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

func TestSampleAtCurrentBucketStartIsObserved(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, beijingLocation)
	spec, err := makeGridSpec(Range7D, now)
	if err != nil {
		t.Fatal(err)
	}
	view := buildServiceView(
		model.ServiceView{ServiceUID: "svc", Model: "svc", IntervalSec: 60, WarningSec: 30},
		[]model.ProbeResult{{OK: true, TS: now.Unix(), LatencyMS: 20}},
		spec,
	)
	cell := view.Cells[(len(spec.rows)-1)*24+now.Hour()]
	if cell.Status != StatusHealthy || cell.ActualSamples != 1 || cell.ExpectedSamples != 1 || cell.CoveragePct != 100 {
		t.Fatalf("桶起点样本未按查询截止点聚合: %+v", cell)
	}
}

func TestBuildUsesCurrentServiceThreshold(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 7, 0, 0, beijingLocation)
	last := model.ProbeResult{OK: true, TS: now.Unix(), LatencyMS: 1500}
	service, err := New(repositoryStub{results: map[string][]model.ProbeResult{
		"slow": {{OK: true, TS: now.Add(-time.Minute).Unix(), LatencyMS: 1500}},
	}}, statusStub{response: model.StatusResponse{Services: []model.ServiceView{
		{ServiceUID: "slow", Name: "Slow", Model: "slow-model", IntervalSec: 60, WarningSec: 1, Last: &last},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	response, err := service.Build(context.Background(), Range1D)
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
		{ServiceUID: "other", Name: "Other", Model: "other-model", SortOrder: 20, IntervalSec: 60, WarningSec: 30},
		{ServiceUID: "slow", Name: "Slow", Model: "slow-model", SortOrder: 10, IntervalSec: 60, WarningSec: 30, Last: &last},
	}}}
	service, err := New(repository, status)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return currentTime }

	first, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 || len(first.Services) != 2 || first.Services[0].Model != "slow-model" || first.Services[0].Status != StatusHealthy {
		t.Fatalf("首次构建未按 sort_order 排序: calls=%d response=%+v", repository.calls, first.Services)
	}
	currentTime = currentTime.Add(time.Second)
	cached, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 || cached.GeneratedAt != first.GeneratedAt {
		t.Fatalf("同一自然日内 TTL 未到时应命中缓存: calls=%d generated_at=%d", repository.calls, cached.GeneratedAt)
	}

	status.response.Services[1].WarningSec = 1
	changed, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 4 || changed.Services[0].Status != StatusWarning {
		t.Fatalf("warning_sec 变化后缓存未失效: calls=%d response=%+v", repository.calls, changed.Services)
	}

	// 删除后重建同名同 model 服务时，内部 UID 变化也必须使旧历史缓存失效。
	status.response.Services[1].ServiceUID = "slow-recreated"
	repository.results["slow-recreated"] = nil
	recreated, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 6 || recreated.Services[0].Samples != 0 {
		t.Fatalf("服务 UID 变化后缓存未失效: calls=%d response=%+v", repository.calls, recreated.Services)
	}

	status.response.Services = status.response.Services[:1]
	withoutDisabled, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 7 || len(withoutDisabled.Services) != 1 || withoutDisabled.Services[0].Model != "other-model" {
		t.Fatalf("禁用服务从快照移除后仍出现在缓存中: calls=%d response=%+v", repository.calls, withoutDisabled.Services)
	}
}

func TestBuildInvalidatesRangeCacheAtBeijingMidnight(t *testing.T) {
	currentTime := time.Date(2026, 8, 18, 23, 59, 59, 0, beijingLocation)
	repository := &countingRepository{results: map[string][]model.ProbeResult{"svc": nil}}
	service, err := New(repository, statusStub{response: model.StatusResponse{Services: []model.ServiceView{
		{ServiceUID: "svc", Name: "Service", Model: "service-model", IntervalSec: 60, WarningSec: 30},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return currentTime }

	beforeMidnight, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(2 * time.Second)
	afterMidnight, err := service.Build(context.Background(), Range7D)
	if err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 {
		t.Fatalf("跨午夜仍复用了旧缓存: calls=%d", repository.calls)
	}
	if beforeMidnight.Rows[0] != "08-12" || beforeMidnight.Rows[6] != "08-18" {
		t.Fatalf("午夜前日期行 = %v", beforeMidnight.Rows)
	}
	if afterMidnight.Rows[0] != "08-13" || afterMidnight.Rows[6] != "08-19" {
		t.Fatalf("午夜后日期行 = %v", afterMidnight.Rows)
	}
}

func repeat(result model.ProbeResult, count int) []model.ProbeResult {
	results := make([]model.ProbeResult, count)
	for index := range results {
		results[index] = result
	}
	return results
}
