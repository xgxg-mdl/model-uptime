// Package store 提供探测历史的 SQLite 持久化。
// 选择 SQLite 而非独立数据库：单文件、无网络依赖，Docker 部署时挂载一个卷即可。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 实现，无 cgo，交叉编译无痛

	"github.com/lefachao/model-uptime/internal/model"
)

// Store 封装 SQLite 数据库访问。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并初始化表结构。
func Open(path string) (*Store, error) {
	// modernc DSN 参数：WAL 提升并发读，busy_timeout 避免并发写冲突直接失败。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	s := &Store{db: db}
	if err := s.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS probe_results (
    service_id TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error      TEXT,
    PRIMARY KEY (service_id, ts)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_probe_results_ts ON probe_results(ts);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("初始化数据库表失败: %w", err)
	}
	return nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// AppendResult 写入一条探测结果；同一 (service_id, ts) 幂等覆盖。
func (s *Store) AppendResult(ctx context.Context, svcID string, r model.ProbeResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO probe_results (service_id, ts, ok, latency_ms, error)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(service_id, ts) DO UPDATE SET ok=excluded.ok, latency_ms=excluded.latency_ms, error=excluded.error`,
		svcID, r.TS, boolInt(r.OK), r.LatencyMS, nullableStr(r.Error),
	)
	if err != nil {
		return fmt.Errorf("写入探测结果失败: %w", err)
	}
	return nil
}

// LoadHistory 按时间升序返回某服务最近 limit 条结果（用于启动后恢复历史）。
func (s *Store) LoadHistory(ctx context.Context, svcID string, limit int) ([]model.ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, ok, latency_ms, error FROM probe_results WHERE service_id=? ORDER BY ts DESC LIMIT ?`,
		svcID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询历史失败: %w", err)
	}
	defer rows.Close()
	var out []model.ProbeResult
	for rows.Next() {
		var r model.ProbeResult
		var ok int
		var errText sql.NullString
		if err := rows.Scan(&r.TS, &ok, &r.LatencyMS, &errText); err != nil {
			return nil, fmt.Errorf("扫描历史失败: %w", err)
		}
		r.OK = ok != 0
		r.Error = errText.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 降序查询后反转成升序（时间轴左旧右新）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// DeleteHistory 删除某个服务的全部历史，用于终止观测生命周期。
// 返回删除行数。
func (s *Store) DeleteHistory(ctx context.Context, svcID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM probe_results WHERE service_id = ?`, svcID)
	if err != nil {
		return 0, fmt.Errorf("删除服务历史失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeBefore 删除早于截止时间的记录，用于历史保留窗口清理。
// 返回删除行数。
func (s *Store) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM probe_results WHERE ts < ?`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("清理历史失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
