// Package heatmap aggregates raw probe history into public health heatmaps.
package heatmap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

const (
	RangeDay   = "day"
	RangeWeek  = "week"
	RangeMonth = "month"

	StatusHealthy      = "healthy"
	StatusWarning      = "warning"
	StatusFailing      = "failing"
	StatusUnobserved   = "unobserved"
	StatusInsufficient = "insufficient"

	minimumCoverage = 0.5
	severityRatio   = 0.2
)

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var ErrInvalidRange = errors.New("range 仅支持 day、week 或 month")

type Repository interface {
	LoadResultsBetween(context.Context, string, int64, int64) ([]model.ProbeResult, error)
}

type StatusProvider interface {
	Snapshot() model.StatusResponse
}

type Service struct {
	repository Repository
	status     StatusProvider
	now        func() time.Time
	buildMu    sync.Mutex
	cache      map[string]cacheEntry
}

type cacheEntry struct {
	key       string
	createdAt time.Time
	response  Response
}

const cacheTTL = 30 * time.Second

func New(repository Repository, status StatusProvider) (*Service, error) {
	if repository == nil {
		return nil, errors.New("heatmap repository is required")
	}
	if status == nil {
		return nil, errors.New("heatmap status provider is required")
	}
	return &Service{repository: repository, status: status, now: time.Now, cache: make(map[string]cacheEntry)}, nil
}

type Response struct {
	GeneratedAt int64             `json:"generated_at"`
	Range       string            `json:"range"`
	Timezone    string            `json:"timezone"`
	BucketSec   int64             `json:"bucket_sec"`
	Rows        []string          `json:"rows"`
	Columns     []string          `json:"columns"`
	Page        *model.PageConfig `json:"page,omitempty"`
	Services    []ServiceView     `json:"services"`
}

type ServiceView struct {
	ID             string  `json:"id"`
	Model          string  `json:"model"`
	Provider       string  `json:"provider,omitempty"`
	Status         string  `json:"status"`
	Samples        int     `json:"samples"`
	LatencySamples int     `json:"latency_samples"`
	UptimePct      float64 `json:"uptime_pct"`
	P95LatencyMS   int64   `json:"p95_latency_ms"`
	Cells          []Cell  `json:"cells"`
}

type Cell struct {
	StartTS         int64   `json:"start_ts"`
	EndTS           int64   `json:"end_ts"`
	Status          string  `json:"status"`
	Intensity       int     `json:"intensity"`
	CoveragePct     float64 `json:"coverage_pct"`
	ActualSamples   int     `json:"actual_samples"`
	ExpectedSamples int     `json:"expected_samples"`
	HealthySamples  int     `json:"healthy_samples"`
	WarningSamples  int     `json:"warning_samples"`
	FailedSamples   int     `json:"failed_samples"`
	UptimePct       float64 `json:"uptime_pct"`
	AvgLatencyMS    int64   `json:"avg_latency_ms"`
	P95LatencyMS    int64   `json:"p95_latency_ms"`
}

type gridSpec struct {
	rangeName string
	bucket    time.Duration
	rows      []string
	columns   []string
	slots     []timeSlot
	queryFrom time.Time
	queryTo   time.Time
}

type timeSlot struct {
	start time.Time
	end   time.Time
}

func (s *Service) Build(ctx context.Context, rangeName string) (Response, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	now := s.now().In(beijingLocation)
	spec, err := makeGridSpec(rangeName, now)
	if err != nil {
		return Response{}, err
	}
	snapshot := s.status.Snapshot()
	services := append([]model.ServiceView(nil), snapshot.Services...)
	sort.SliceStable(services, func(i, j int) bool { return services[i].SortOrder < services[j].SortOrder })
	key := buildCacheKey(rangeName, snapshot.Page, services)
	if cached, ok := s.cache[rangeName]; ok && cached.key == key && now.Sub(cached.createdAt) < cacheTTL {
		return cached.response, nil
	}
	response := Response{
		GeneratedAt: now.Unix(), Range: rangeName, Timezone: "Asia/Shanghai",
		BucketSec: int64(spec.bucket / time.Second), Rows: spec.rows, Columns: spec.columns,
		Page: snapshot.Page, Services: make([]ServiceView, 0, len(services)),
	}
	for _, service := range services {
		results, err := s.repository.LoadResultsBetween(ctx, service.ID, spec.queryFrom.Unix(), spec.queryTo.Unix())
		if err != nil {
			return Response{}, fmt.Errorf("构建服务 %q 热力图失败: %w", service.ID, err)
		}
		response.Services = append(response.Services, buildServiceView(service, results, spec, now))
	}
	s.cache[rangeName] = cacheEntry{key: key, createdAt: now, response: response}
	return response, nil
}

func makeGridSpec(rangeName string, now time.Time) (gridSpec, error) {
	now = now.In(beijingLocation)
	columns := make([]string, 24)
	for hour := range columns {
		columns[hour] = fmt.Sprintf("%02d", hour)
	}
	switch rangeName {
	case RangeDay:
		bucket := 15 * time.Minute
		end := now
		start := end.Add(-24 * time.Hour)
		spec := gridSpec{
			rangeName: rangeName, bucket: bucket,
			rows: make([]string, 4), columns: columns,
			slots: make([]timeSlot, 4*24), queryFrom: start, queryTo: end,
		}
		for bucketIndex, cursor := 0, start; cursor.Before(end); bucketIndex, cursor = bucketIndex+1, cursor.Add(bucket) {
			row := bucketIndex % 4
			if bucketIndex < 4 {
				spec.rows[row] = fmt.Sprintf("%02d", cursor.Minute())
			}
			index := row*24 + cursor.Hour()
			spec.slots[index] = timeSlot{start: cursor, end: cursor.Add(bucket)}
		}
		return spec, nil
	case RangeWeek, RangeMonth:
		days := 7
		if rangeName == RangeMonth {
			days = 30
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, beijingLocation)
		start := today.AddDate(0, 0, -(days - 1))
		spec := gridSpec{
			rangeName: rangeName, bucket: time.Hour,
			rows: make([]string, days), columns: columns,
			slots: make([]timeSlot, days*24), queryFrom: start, queryTo: now.Add(time.Second),
		}
		for day := 0; day < days; day++ {
			date := start.AddDate(0, 0, day)
			spec.rows[day] = date.Format("01-02")
			for hour := 0; hour < 24; hour++ {
				cellStart := date.Add(time.Duration(hour) * time.Hour)
				spec.slots[day*24+hour] = timeSlot{start: cellStart, end: cellStart.Add(time.Hour)}
			}
		}
		return spec, nil
	default:
		return gridSpec{}, ErrInvalidRange
	}
}

func buildServiceView(service model.ServiceView, results []model.ProbeResult, spec gridSpec, now time.Time) ServiceView {
	cells := make([]Cell, len(spec.slots))
	buckets := make([][]model.ProbeResult, len(spec.slots))
	for _, result := range results {
		index := slotIndex(spec, time.Unix(result.TS, 0).In(beijingLocation))
		if index >= 0 {
			buckets[index] = append(buckets[index], result)
		}
	}
	allSuccessfulLatencies := make([]int64, 0, len(results))
	successful := 0
	for _, result := range results {
		if result.OK {
			successful++
			allSuccessfulLatencies = append(allSuccessfulLatencies, result.LatencyMS)
		}
	}
	for index, slot := range spec.slots {
		cells[index] = aggregateCell(slot, buckets[index], service.IntervalSec, service.WarningSec, now)
	}
	uptime := 0.0
	if len(results) > 0 {
		uptime = float64(successful) / float64(len(results)) * 100
	}
	return ServiceView{
		ID: service.ID, Model: service.Model, Provider: service.Provider,
		Status: currentStatus(service.Last, service.WarningSec), Samples: len(results), UptimePct: uptime,
		LatencySamples: len(allSuccessfulLatencies), P95LatencyMS: percentile95(allSuccessfulLatencies), Cells: cells,
	}
}

func slotIndex(spec gridSpec, timestamp time.Time) int {
	if timestamp.Before(spec.queryFrom) || !timestamp.Before(spec.queryTo) {
		return -1
	}
	if spec.rangeName == RangeDay {
		bucketIndex := int(timestamp.Sub(spec.queryFrom) / spec.bucket)
		if bucketIndex < 0 || bucketIndex >= len(spec.slots) {
			return -1
		}
		bucketStart := spec.queryFrom.Add(time.Duration(bucketIndex) * spec.bucket)
		return (bucketIndex%4)*24 + bucketStart.Hour()
	}
	day := int(timestamp.Sub(spec.queryFrom) / (24 * time.Hour))
	if day < 0 || day >= len(spec.rows) {
		return -1
	}
	return day*24 + timestamp.Hour()
}

func buildCacheKey(rangeName string, page *model.PageConfig, services []model.ServiceView) string {
	var key strings.Builder
	fmt.Fprintf(&key, "%s|%#v", rangeName, page)
	for _, service := range services {
		fmt.Fprintf(&key, "|%s|%s|%s|%d|%d|%d", service.ID, service.Model, service.Provider, service.SortOrder, service.IntervalSec, service.WarningSec)
		if service.Last != nil {
			fmt.Fprintf(&key, "|%d|%t|%d", service.Last.TS, service.Last.OK, service.Last.LatencyMS)
		}
	}
	return key.String()
}

func aggregateCell(slot timeSlot, results []model.ProbeResult, intervalSec, warningSec int, now time.Time) Cell {
	cell := Cell{StartTS: slot.start.Unix(), EndTS: slot.end.Unix(), Status: StatusUnobserved}
	observedEnd := slot.end
	if now.Before(observedEnd) {
		observedEnd = now
	}
	if !observedEnd.After(slot.start) {
		return cell
	}
	if intervalSec <= 0 {
		intervalSec = 60
	}
	if warningSec <= 0 {
		warningSec = 30
	}
	elapsedSec := observedEnd.Unix() - slot.start.Unix()
	cell.ExpectedSamples = int((elapsedSec + int64(intervalSec) - 1) / int64(intervalSec))
	cell.ActualSamples = len(results)
	if cell.ExpectedSamples > 0 {
		cell.CoveragePct = math.Min(100, float64(cell.ActualSamples)/float64(cell.ExpectedSamples)*100)
	}
	successfulLatencies := make([]int64, 0, len(results))
	latencySum := int64(0)
	for _, result := range results {
		switch {
		case !result.OK:
			cell.FailedSamples++
		case result.LatencyMS > int64(warningSec)*1000:
			cell.WarningSamples++
			successfulLatencies = append(successfulLatencies, result.LatencyMS)
			latencySum += result.LatencyMS
		default:
			cell.HealthySamples++
			successfulLatencies = append(successfulLatencies, result.LatencyMS)
			latencySum += result.LatencyMS
		}
	}
	if cell.ActualSamples > 0 {
		cell.UptimePct = float64(cell.HealthySamples+cell.WarningSamples) / float64(cell.ActualSamples) * 100
	}
	if len(successfulLatencies) > 0 {
		cell.AvgLatencyMS = latencySum / int64(len(successfulLatencies))
		cell.P95LatencyMS = percentile95(successfulLatencies)
	}
	if cell.ExpectedSamples == 0 || cell.ActualSamples == 0 {
		return cell
	}
	if cell.CoveragePct < minimumCoverage*100 {
		cell.Status = StatusInsufficient
		return cell
	}
	failureRatio := float64(cell.FailedSamples) / float64(cell.ActualSamples)
	warningRatio := float64(cell.WarningSamples) / float64(cell.ActualSamples)
	healthyRatio := float64(cell.HealthySamples) / float64(cell.ActualSamples)
	switch {
	case failureRatio >= severityRatio:
		cell.Status = StatusFailing
		cell.Intensity = intensityLevel(failureRatio)
	case cell.FailedSamples > 0 || warningRatio >= severityRatio:
		cell.Status = StatusWarning
		cell.Intensity = intensityLevel(math.Max(failureRatio, warningRatio))
	default:
		cell.Status = StatusHealthy
		cell.Intensity = intensityLevel(healthyRatio)
	}
	return cell
}

func currentStatus(result *model.ProbeResult, warningSec int) string {
	if result == nil {
		return "pending"
	}
	if !result.OK {
		return StatusFailing
	}
	if warningSec <= 0 {
		warningSec = 30
	}
	if result.LatencyMS > int64(warningSec)*1000 {
		return StatusWarning
	}
	return StatusHealthy
}

func intensityLevel(ratio float64) int {
	switch {
	case ratio >= 0.8:
		return 5
	case ratio >= 0.5:
		return 4
	case ratio >= 0.2:
		return 3
	case ratio >= 0.05:
		return 2
	default:
		return 1
	}
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
