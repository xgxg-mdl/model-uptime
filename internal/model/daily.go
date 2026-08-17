package model

// DailyStats 是一个时间窗内按上一次已知状态累计的可用性统计。
type DailyStats struct {
	UpSec     int64
	DownSec   int64
	DownCount int
	known     bool
	lastOK    bool
}

// ObservedSec 返回在时间窗内拥有已知状态的总秒数。
func (s DailyStats) ObservedSec() int64 { return s.UpSec + s.DownSec }

// UptimePct 返回基于已观测时长的可用率。没有样本时返回 0，调用方可据此
// 区分“未监测”和“全天正常”。
func (s DailyStats) UptimePct() float64 {
	if s.ObservedSec() == 0 {
		if s.known && s.lastOK {
			return 100
		}
		return 0
	}
	return float64(s.UpSec) / float64(s.ObservedSec()) * 100
}

// CalculateDailyStats 将相邻探测之间的时长归属于前一次已知状态；范围起点前
// 的最后一个样本只用于确定零点状态，不会将统计范围扩展到前一天。
func CalculateDailyStats(results []ProbeResult, since, until int64) DailyStats {
	var stats DailyStats
	var known, statusOK bool
	var cursor int64
	addDuration := func(seconds int64) {
		if seconds <= 0 {
			return
		}
		if statusOK {
			stats.UpSec += seconds
		} else {
			stats.DownSec += seconds
		}
	}
	for _, result := range results {
		if result.TS > until {
			break
		}
		if result.TS < since {
			statusOK, known, cursor = result.OK, true, since
			stats.known, stats.lastOK = true, result.OK
			continue
		}
		if !known {
			statusOK, known, cursor = result.OK, true, result.TS
			if !result.OK {
				stats.DownCount++
			}
			stats.known, stats.lastOK = true, result.OK
			continue
		}
		addDuration(result.TS - cursor)
		if statusOK && !result.OK {
			stats.DownCount++
		}
		statusOK, cursor = result.OK, result.TS
		stats.known, stats.lastOK = true, result.OK
	}
	if known {
		addDuration(until - cursor)
	}
	return stats
}
