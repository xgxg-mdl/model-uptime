package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open err = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAppendAndLoadHistory(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	// 乱序写入，验证查询按 ts 升序返回
	for _, r := range []model.ProbeResult{
		{OK: true, TS: 100, StartedAt: 90, LatencyMS: 50},
		{OK: false, TS: 200, LatencyMS: 0, Error: "boom"},
		{OK: true, TS: 150, LatencyMS: 30},
	} {
		if err := s.AppendResult(ctx, "svc-a", r); err != nil {
			t.Fatalf("AppendResult err = %v", err)
		}
	}

	hist, err := s.LoadHistory(ctx, "svc-a", 10)
	if err != nil {
		t.Fatalf("LoadHistory err = %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("历史条数 = %d，期望 3", len(hist))
	}
	if hist[0].TS != 100 || hist[2].TS != 200 {
		t.Errorf("历史应升序: %+v", hist)
	}
	if hist[0].StartedAt != 90 || hist[1].StartedAt != hist[1].TS {
		t.Errorf("探测开始时间未持久化或兼容回填: %+v", hist)
	}
	if hist[2].OK || hist[2].Error == "" {
		t.Errorf("最新一条应带失败信息: %+v", hist[2])
	}

	// 其他服务隔离
	other, err := s.LoadHistory(ctx, "svc-b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("svc-b 历史应为空")
	}
}

func TestLoadHistoryLimit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := int64(1); i <= 10; i++ {
		if err := s.AppendResult(ctx, "svc-a", model.ProbeResult{OK: true, TS: i}); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.LoadHistory(ctx, "svc-a", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Fatalf("limit=4 应返回 4 条，got %d", len(hist))
	}
	if hist[0].TS != 7 || hist[3].TS != 10 {
		t.Errorf("应返回最近 4 条升序: %+v", hist)
	}
}

func TestLoadResultsBetweenUsesHalfOpenRange(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, timestamp := range []int64{99, 100, 150, 200} {
		if err := s.AppendResult(ctx, "svc-a", model.ProbeResult{OK: true, TS: timestamp}); err != nil {
			t.Fatal(err)
		}
	}
	results, err := s.LoadResultsBetween(ctx, "svc-a", 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].TS != 100 || results[1].TS != 150 {
		t.Fatalf("半开时间范围结果错误: %+v", results)
	}
}

func TestLoadResultsStartedBetweenUsesCompletedWindowBoundaries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, startedAt := range []int64{40, 40, 100, 100, 160, 220, 280} {
		result := model.ProbeResult{OK: true, TS: startedAt + 5, StartedAt: startedAt}
		if err := s.AppendResult(ctx, "svc-a", result); err != nil {
			t.Fatal(err)
		}
	}
	results, err := s.LoadResultsStartedBetween(ctx, "svc-a", 100, 280)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 7 || results[0].StartedAt != 40 || results[1].StartedAt != 40 || results[2].StartedAt != 100 || results[3].StartedAt != 100 || results[6].StartedAt != 280 {
		t.Fatalf("完整时间窗边界错误: %+v", results)
	}
	observedSince, err := s.LoadObservationStart(ctx, "svc-a")
	if err != nil || observedSince != 40 {
		t.Fatalf("观测起点 = %d, err=%v", observedSince, err)
	}
}

func TestLoadResultsSinceWithPreviousAndFailureStart(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, result := range []model.ProbeResult{
		{OK: true, TS: 50},
		{OK: true, TS: 120},
		{OK: false, TS: 200},
		{OK: false, TS: 260},
		{OK: true, TS: 300},
		{OK: false, TS: 400},
		{OK: true, TS: 500},
	} {
		if err := s.AppendResult(ctx, "svc-a", result); err != nil {
			t.Fatal(err)
		}
	}
	window, err := s.LoadResultsSinceWithPrevious(ctx, "svc-a", 100, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 5 || window[0].TS != 50 || window[4].TS != 300 {
		t.Fatalf("统计时间窗应包含起点前最后状态并排除终点后数据: %+v", window)
	}
	startedAt, err := s.LoadFailureStart(ctx, "svc-a", 300)
	if err != nil {
		t.Fatal(err)
	}
	if startedAt != 200 {
		t.Fatalf("连续异常起点 = %d，期望 200", startedAt)
	}
	startedAt, err = s.LoadFailureStart(ctx, "svc-a", 500)
	if err != nil {
		t.Fatal(err)
	}
	if startedAt != 400 {
		t.Fatalf("第二次连续异常起点 = %d，期望 400", startedAt)
	}
	for _, result := range []model.ProbeResult{{OK: false, TS: 10}, {OK: false, TS: 20}, {OK: true, TS: 30}} {
		if err := s.AppendResult(ctx, "svc-b", result); err != nil {
			t.Fatal(err)
		}
	}
	startedAt, err = s.LoadFailureStart(ctx, "svc-b", 30)
	if err != nil || startedAt != 10 {
		t.Fatalf("没有成功基线时的异常起点 = %d, err=%v", startedAt, err)
	}
}

func TestDeleteHistoryOnlyAffectsRequestedService(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.AppendResult(ctx, "svc-a", model.ProbeResult{OK: true, TS: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendResult(ctx, "svc-b", model.ProbeResult{OK: true, TS: 2}); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteHistory(ctx, "svc-a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("删除行数 = %d，期望 1", n)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"svc-a", 0},
		{"svc-b", 1},
	} {
		hist, err := s.LoadHistory(ctx, tc.id, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) != tc.want {
			t.Errorf("%s 历史数 = %d，期望 %d", tc.id, len(hist), tc.want)
		}
	}
}

func TestPurgeBefore(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, ts := range []int64{100, 200, 300} {
		if err := s.AppendResult(ctx, "svc-a", model.ProbeResult{OK: true, TS: ts}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PurgeBefore(ctx, time.Unix(250, 0))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应清理 2 条，got %d", n)
	}
	hist, _ := s.LoadHistory(ctx, "svc-a", 10)
	if len(hist) != 1 || hist[0].TS != 300 {
		t.Errorf("应只剩 ts=300: %+v", hist)
	}
}

func TestSameTimestampResultsArePreservedInInsertionOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r1 := model.ProbeResult{OK: true, TS: 100, LatencyMS: 10}
	r2 := model.ProbeResult{OK: false, TS: 100, LatencyMS: 0, Error: "late"}
	if err := s.AppendResult(ctx, "svc-a", r1); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendResult(ctx, "svc-a", r2); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.LoadHistory(ctx, "svc-a", 10)
	if len(hist) != 2 {
		t.Fatalf("同一秒的两次探测都应保留: %+v", hist)
	}
	if !hist[0].OK || hist[0].LatencyMS != 10 || hist[1].OK || hist[1].Error != "late" {
		t.Errorf("同一秒结果应按写入顺序返回: %+v", hist)
	}
}

func TestOpenMigratesLegacySchemaWithoutLosingHistory(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		versionPragma string
	}{
		{name: "未声明版本", versionPragma: "PRAGMA user_version = 0;"},
		{name: "版本一", versionPragma: "PRAGMA user_version = 1;"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`
			CREATE TABLE probe_results (
				service_id TEXT NOT NULL,
				ts INTEGER NOT NULL,
			ok INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT,
				PRIMARY KEY (service_id, ts)
			) WITHOUT ROWID;
			CREATE INDEX idx_probe_results_ts ON probe_results(ts);` + testCase.versionPragma + `
			INSERT INTO probe_results(service_id, ts, ok, latency_ms, error)
			VALUES ('svc-a', 100, 1, 12, NULL), ('svc-a', 200, 0, 30, 'legacy failure');`)
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			s, err := Open(path)
			if err != nil {
				t.Fatalf("迁移旧数据库: %v", err)
			}
			history, err := s.LoadHistory(context.Background(), "svc-a", 10)
			if err != nil {
				s.Close()
				t.Fatal(err)
			}
			if len(history) != 2 || history[0].TS != 100 || history[0].StartedAt != 100 || history[1].Error != "legacy failure" {
				s.Close()
				t.Fatalf("迁移后历史不完整: %+v", history)
			}
			var version int
			if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				s.Close()
				t.Fatal(err)
			}
			if version != currentSchemaVersion {
				s.Close()
				t.Fatalf("schema version = %d，期望 %d", version, currentSchemaVersion)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// 已迁移数据库再次打开不得重复迁移或丢数据。
			s, err = Open(path)
			if err != nil {
				t.Fatalf("重复打开已迁移数据库: %v", err)
			}
			defer s.Close()
			if err := s.AppendResult(context.Background(), "svc-a", model.ProbeResult{OK: true, TS: 200, LatencyMS: 5}); err != nil {
				t.Fatal(err)
			}
			history, err = s.LoadHistory(context.Background(), "svc-a", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(history) != 3 || history[2].LatencyMS != 5 {
				t.Fatalf("迁移后同秒新结果应继续追加: %+v", history)
			}
		})
	}
}

func TestDeleteHistoriesRollsBackAllServicesOnFailure(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, id := range []string{"svc-a", "svc-b"} {
		if err := s.AppendResult(ctx, id, model.ProbeResult{OK: true, TS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`
		CREATE TRIGGER reject_svc_b_delete
		BEFORE DELETE ON probe_results
		WHEN OLD.service_id = 'svc-b'
		BEGIN
			SELECT RAISE(ABORT, 'reject svc-b delete');
		END;`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteHistories(ctx, []string{"svc-a", "svc-b"}); err == nil {
		t.Fatal("批量删除第二个服务失败时应返回错误")
	}
	for _, id := range []string{"svc-a", "svc-b"} {
		history, err := s.LoadHistory(ctx, id, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 {
			t.Fatalf("事务回滚后 %s 历史应保留: %+v", id, history)
		}
	}
}
