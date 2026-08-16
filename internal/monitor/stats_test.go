package monitor

import (
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestUptimePct(t *testing.T) {
	cases := []struct {
		name    string
		history []model.ProbeResult
		want    float64
	}{
		{"空窗口", nil, 100.0},
		{"全成功", []model.ProbeResult{{OK: true}, {OK: true}}, 100.0},
		{"半失败", []model.ProbeResult{{OK: true}, {OK: false}}, 50.0},
		{"一失败三成功", []model.ProbeResult{{OK: true}, {OK: false}, {OK: true}, {OK: true}}, 75.0},
	}
	for _, tc := range cases {
		if got := uptimePct(tc.history); got != tc.want {
			t.Errorf("%s: uptimePct = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}

func TestCalculateDailyStatsAndBeijingBoundary(t *testing.T) {
	stats := calculateDailyStats([]model.ProbeResult{
		{OK: true, TS: 50},
		{OK: false, TS: 200},
		{OK: true, TS: 500},
		{OK: false, TS: 800},
		{OK: true, TS: 1000},
	}, 100, 1000)
	if stats.upSec != 400 || stats.downSec != 500 || stats.downCount != 2 {
		t.Fatalf("今日时间统计错误: %+v", stats)
	}
	if stats.uptimePct < 44.44 || stats.uptimePct > 44.45 {
		t.Fatalf("今日可用率错误: %.4f", stats.uptimePct)
	}
	if got := failureStartFromResults([]model.ProbeResult{{OK: true, TS: 100}, {OK: false, TS: 200}, {OK: false, TS: 300}, {OK: true, TS: 400}}, 400); got != 200 {
		t.Fatalf("异常起点 = %d，期望 200", got)
	}
	beijingTime := time.Date(2026, 8, 15, 10, 50, 2, 0, beijingLocation)
	wantStart := time.Date(2026, 8, 15, 0, 0, 0, 0, beijingLocation).Unix()
	if got := beijingDayStart(beijingTime.Unix()); got != wantStart {
		t.Fatalf("北京时间零点 = %d，期望 %d", got, wantStart)
	}
}
