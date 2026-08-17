package model

import "testing"

func TestCalculateDailyStatsDistinguishesUnobservedFromHealthy(t *testing.T) {
	if stats := CalculateDailyStats(nil, 100, 200); stats.ObservedSec() != 0 || stats.UptimePct() != 0 {
		t.Fatalf("无样本统计错误: %+v", stats)
	}
	stats := CalculateDailyStats([]ProbeResult{{TS: 50, OK: true}}, 100, 200)
	if stats.UpSec != 100 || stats.DownSec != 0 || stats.UptimePct() != 100 {
		t.Fatalf("全天正常统计错误: %+v", stats)
	}
	firstSuccess := CalculateDailyStats([]ProbeResult{{TS: 200, OK: true}}, 100, 200)
	if firstSuccess.ObservedSec() != 0 || firstSuccess.UptimePct() != 100 {
		t.Fatalf("首次成功探测应保留 100%% 语义: %+v", firstSuccess)
	}
}
