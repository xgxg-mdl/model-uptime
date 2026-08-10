package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
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
		{OK: true, TS: 100, LatencyMS: 50},
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

func TestDuplicateTSOverwrites(t *testing.T) {
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
	if len(hist) != 1 || hist[0].OK || hist[0].Error != "late" {
		t.Errorf("同 ts 应覆盖: %+v", hist)
	}
}
