// Package sqlite 提供探测历史、状态变化 ledger 与通知 outbox 的 SQLite 持久化。
// 选择 SQLite 而非独立数据库：单文件、无网络依赖，Docker 部署时挂载一个卷即可。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 实现，无 cgo，交叉编译无痛

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

// Store 封装 SQLite 数据库访问。
type Store struct {
	db *sql.DB
}

const currentSchemaVersion = 9

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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始数据库迁移失败: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("数据库版本 %d 高于程序支持的版本 %d", version, currentSchemaVersion)
	}

	hasTable, hasID, err := probeResultsSchema(ctx, tx)
	if err != nil {
		return err
	}
	if hasTable && !hasID {
		if err := migrateLegacyProbeResults(ctx, tx); err != nil {
			return err
		}
	}
	// 每次启动都执行幂等建表，使新增的独立表能随 schema 版本升级落地。
	if err := createCurrentSchema(ctx, tx); err != nil {
		return err
	}
	if err := migrateProbeResultsStartedAt(ctx, tx); err != nil {
		return err
	}
	if err := migrateNotificationOutbox(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, currentSchemaVersion)); err != nil {
		return fmt.Errorf("更新数据库版本失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交数据库迁移失败: %w", err)
	}
	return nil
}

func probeResultsSchema(ctx context.Context, tx *sql.Tx) (exists, hasID bool, err error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='probe_results'`).Scan(&count); err != nil {
		return false, false, fmt.Errorf("检查探测历史表失败: %w", err)
	}
	if count == 0 {
		return false, false, nil
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(probe_results)`)
	if err != nil {
		return false, false, fmt.Errorf("读取探测历史表结构失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, false, fmt.Errorf("扫描探测历史表结构失败: %w", err)
		}
		if name == "id" {
			hasID = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("读取探测历史表结构失败: %w", err)
	}
	return true, hasID, nil
}

func createCurrentSchema(ctx context.Context, tx *sql.Tx) error {
	const schema = `
CREATE TABLE IF NOT EXISTS probe_results (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id TEXT NOT NULL,
    ts         INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
    ok         INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error      TEXT
);
CREATE INDEX IF NOT EXISTS idx_probe_results_service_time ON probe_results(service_id, ts, id);
CREATE INDEX IF NOT EXISTS idx_probe_results_ts ON probe_results(ts);
CREATE TABLE IF NOT EXISTS notification_outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    dedupe_key      TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL,
	message         TEXT NOT NULL,
	payload_json    TEXT NOT NULL DEFAULT '',
	status_page_url TEXT NOT NULL DEFAULT '',
	created_at_ms   INTEGER NOT NULL,
	available_at_ms INTEGER NOT NULL,
	attempts        INTEGER NOT NULL DEFAULT 0,
	permanent_fails INTEGER NOT NULL DEFAULT 0,
	failure_config_fingerprint TEXT NOT NULL DEFAULT '',
	last_error      TEXT NOT NULL DEFAULT '',
	lease_token     TEXT NOT NULL DEFAULT '',
	quarantined     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_notification_outbox_available
	ON notification_outbox(available_at_ms, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_outbox_dedupe
    ON notification_outbox(dedupe_key) WHERE dedupe_key <> '';
CREATE TABLE IF NOT EXISTS status_transitions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id     TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    changed_at_ms  INTEGER NOT NULL,
    available_at_ms INTEGER NOT NULL,
    delivery_group TEXT NOT NULL DEFAULT '',
    lease_token    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_status_transitions_unassigned_claim
    ON status_transitions(available_at_ms, changed_at_ms, id)
    WHERE delivery_group = '';
CREATE INDEX IF NOT EXISTS idx_status_transitions_assigned_due
    ON status_transitions(available_at_ms, id, delivery_group)
    WHERE delivery_group <> '';
CREATE INDEX IF NOT EXISTS idx_status_transitions_delivery_group
	ON status_transitions(delivery_group, id)
	WHERE delivery_group <> '';
CREATE TABLE IF NOT EXISTS daily_report_runs (
    report_date     TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    PRIMARY KEY (report_date, subscription_id)
);`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("初始化数据库表失败: %w", err)
	}
	return nil
}

func migrateProbeResultsStartedAt(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(probe_results)`)
	if err != nil {
		return fmt.Errorf("读取探测历史表结构失败: %w", err)
	}
	hasStartedAt := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("扫描探测历史表结构失败: %w", err)
		}
		if name == "started_at" {
			hasStartedAt = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取探测历史表结构失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭探测历史表结构查询失败: %w", err)
	}
	if !hasStartedAt {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE probe_results ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("迁移探测开始时间列失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE probe_results SET started_at = ts WHERE started_at = 0`); err != nil {
			return fmt.Errorf("回填探测开始时间失败: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_probe_results_service_started ON probe_results(service_id, started_at, id)`); err != nil {
		return fmt.Errorf("创建探测开始时间索引失败: %w", err)
	}
	return nil
}

func migrateNotificationOutbox(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(notification_outbox)`)
	if err != nil {
		return fmt.Errorf("读取通知 outbox 表结构失败: %w", err)
	}
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("扫描通知 outbox 表结构失败: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取通知 outbox 表结构失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭通知 outbox 表结构查询失败: %w", err)
	}
	alterations := []struct {
		column string
		sql    string
	}{
		{column: "payload_json", sql: `ALTER TABLE notification_outbox ADD COLUMN payload_json TEXT NOT NULL DEFAULT ''`},
		{column: "status_page_url", sql: `ALTER TABLE notification_outbox ADD COLUMN status_page_url TEXT NOT NULL DEFAULT ''`},
		{column: "permanent_fails", sql: `ALTER TABLE notification_outbox ADD COLUMN permanent_fails INTEGER NOT NULL DEFAULT 0`},
		{column: "quarantined", sql: `ALTER TABLE notification_outbox ADD COLUMN quarantined INTEGER NOT NULL DEFAULT 0`},
		{column: "failure_config_fingerprint", sql: `ALTER TABLE notification_outbox ADD COLUMN failure_config_fingerprint TEXT NOT NULL DEFAULT ''`},
	}
	for _, alteration := range alterations {
		if _, exists := columns[alteration.column]; exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, alteration.sql); err != nil {
			return fmt.Errorf("迁移通知 outbox 列 %q 失败: %w", alteration.column, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_notification_outbox_active_available
		ON notification_outbox(available_at_ms, id) WHERE quarantined = 0`); err != nil {
		return fmt.Errorf("创建通知 outbox 活跃索引失败: %w", err)
	}
	return nil
}

func migrateLegacyProbeResults(ctx context.Context, tx *sql.Tx) error {
	const migration = `
ALTER TABLE probe_results RENAME TO probe_results_v1;
CREATE TABLE probe_results (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id TEXT NOT NULL,
    ts         INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
    ok         INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error      TEXT
);
INSERT INTO probe_results(service_id, ts, started_at, ok, latency_ms, error)
SELECT service_id, ts, ts, ok, latency_ms, error
FROM probe_results_v1
ORDER BY ts, service_id;
DROP TABLE probe_results_v1;
CREATE INDEX idx_probe_results_service_time ON probe_results(service_id, ts, id);
CREATE INDEX idx_probe_results_service_started ON probe_results(service_id, started_at, id);
CREATE INDEX idx_probe_results_ts ON probe_results(ts);`
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return fmt.Errorf("迁移探测历史表到版本 %d 失败: %w", currentSchemaVersion, err)
	}
	return nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// AppendResult 写入一条不产生状态变化的探测结果。
// 每次调用都创建独立记录，同一秒的并发结果不会互相覆盖。
func (s *Store) AppendResult(ctx context.Context, svcID string, r model.ProbeResult) error {
	return s.RecordProbeResult(ctx, svcID, r, nil)
}

// LoadHistory 按时间升序返回某服务最近 limit 条结果（用于启动后恢复历史）。
func (s *Store) LoadHistory(ctx context.Context, svcID string, limit int) ([]model.ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, started_at, ok, latency_ms, error FROM probe_results WHERE service_id=? ORDER BY ts DESC, id DESC LIMIT ?`,
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
		if err := rows.Scan(&r.TS, &r.StartedAt, &ok, &r.LatencyMS, &errText); err != nil {
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

// LoadResultsStartedBetween 返回 (since, until] 内的结果，并携带起点前最后一次结果用于延续观测周期。
func (s *Store) LoadResultsStartedBetween(ctx context.Context, svcID string, since, until int64) ([]model.ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, started_at, ok, latency_ms, error
		 FROM probe_results
		 WHERE service_id=? AND started_at<=? AND (
			started_at>? OR id=(
				SELECT id FROM probe_results
				WHERE service_id=? AND started_at<=?
				ORDER BY started_at DESC, id DESC LIMIT 1
			)
		 )
		 ORDER BY started_at, id`,
		svcID, until, since, svcID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("查询状态页时间窗失败: %w", err)
	}
	defer rows.Close()
	results := make([]model.ProbeResult, 0)
	for rows.Next() {
		var result model.ProbeResult
		var ok int
		var errText sql.NullString
		if err := rows.Scan(&result.TS, &result.StartedAt, &ok, &result.LatencyMS, &errText); err != nil {
			return nil, fmt.Errorf("扫描状态页时间窗失败: %w", err)
		}
		result.OK = ok != 0
		result.Error = errText.String
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取状态页时间窗失败: %w", err)
	}
	return results, nil
}

// LoadObservationStart 返回服务保留历史中最早的探测开始时间。
func (s *Store) LoadObservationStart(ctx context.Context, svcID string) (int64, error) {
	var startedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(started_at) FROM probe_results WHERE service_id=?`, svcID,
	).Scan(&startedAt); err != nil {
		return 0, fmt.Errorf("查询观测起点失败: %w", err)
	}
	if !startedAt.Valid {
		return 0, nil
	}
	return startedAt.Int64, nil
}

// LoadResultsBetween 按时间升序返回服务在 [since, until) 内的探测结果。
func (s *Store) LoadResultsBetween(ctx context.Context, svcID string, since, until int64) ([]model.ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, ok, latency_ms
		 FROM probe_results
		 WHERE service_id=? AND ts>=? AND ts<?
		 ORDER BY ts, id`,
		svcID, since, until,
	)
	if err != nil {
		return nil, fmt.Errorf("查询时间范围历史失败: %w", err)
	}
	defer rows.Close()
	out := make([]model.ProbeResult, 0)
	for rows.Next() {
		var result model.ProbeResult
		var ok int
		if err := rows.Scan(&result.TS, &ok, &result.LatencyMS); err != nil {
			return nil, fmt.Errorf("扫描时间范围历史失败: %w", err)
		}
		result.OK = ok != 0
		out = append(out, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取时间范围历史失败: %w", err)
	}
	return out, nil
}

// LoadResultsSinceWithPrevious 返回时间范围内的结果，并额外携带范围起点前最后一条状态。
// 额外状态用于从北京时间零点开始积分，避免把当天首次探测前的已知状态丢失。
func (s *Store) LoadResultsSinceWithPrevious(ctx context.Context, svcID string, since, until int64) ([]model.ProbeResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, ok, latency_ms, error
		 FROM probe_results
		 WHERE service_id=? AND ts<=? AND (
			ts>=? OR id=(
				SELECT id FROM probe_results
				WHERE service_id=? AND ts<?
				ORDER BY ts DESC, id DESC LIMIT 1
			)
		 )
		 ORDER BY ts ASC, id ASC`,
		svcID, until, since, svcID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("查询统计时间窗失败: %w", err)
	}
	defer rows.Close()
	var out []model.ProbeResult
	for rows.Next() {
		var result model.ProbeResult
		var ok int
		var errText sql.NullString
		if err := rows.Scan(&result.TS, &ok, &result.LatencyMS, &errText); err != nil {
			return nil, fmt.Errorf("扫描统计时间窗失败: %w", err)
		}
		result.OK = ok != 0
		result.Error = errText.String
		out = append(out, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadFailureStart 返回 recoveredAt 之前当前连续失败区间的首次失败时间。
func (s *Store) LoadFailureStart(ctx context.Context, svcID string, recoveredAt int64) (int64, error) {
	var startedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(ts)
		 FROM probe_results
		 WHERE service_id=? AND ok=0 AND ts<?
		   AND ts>COALESCE((
			SELECT MAX(ts) FROM probe_results WHERE service_id=? AND ok=1 AND ts<?
		   ), 0)`,
		svcID, recoveredAt, svcID, recoveredAt,
	).Scan(&startedAt)
	if err != nil {
		return 0, fmt.Errorf("查询异常起点失败: %w", err)
	}
	if !startedAt.Valid {
		return 0, nil
	}
	return startedAt.Int64, nil
}

// DeleteHistory 删除某个服务的全部历史，用于终止观测生命周期。
// 返回删除行数。
func (s *Store) DeleteHistory(ctx context.Context, svcID string) (int64, error) {
	return s.DeleteHistories(ctx, []string{svcID})
}

// DeleteHistories 在单个事务中删除多个服务的全部历史。
// 任意服务删除失败时整笔事务回滚，调用方不会观察到部分删除。
func (s *Store) DeleteHistories(ctx context.Context, svcIDs []string) (int64, error) {
	if len(svcIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始删除服务历史事务失败: %w", err)
	}
	defer tx.Rollback()
	var total int64
	for _, svcID := range svcIDs {
		// 已领取事件组的成员必须保持稳定；仅删除尚未分组的变化。
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM status_transitions
			WHERE service_id = ? AND delivery_group = ''`, svcID); err != nil {
			return 0, fmt.Errorf("删除服务 %q 待处理状态变化失败: %w", svcID, err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM probe_results WHERE service_id = ?`, svcID)
		if err != nil {
			return 0, fmt.Errorf("删除服务 %q 历史失败: %w", svcID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("读取服务 %q 删除行数失败: %w", svcID, err)
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交删除服务历史事务失败: %w", err)
	}
	return total, nil
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
