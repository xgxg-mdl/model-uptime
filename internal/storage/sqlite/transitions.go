package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

// ErrTransitionLeaseLost 为现有 SQLite 调用方保留错误名称。
var ErrTransitionLeaseLost = notification.ErrTransitionLeaseLost

// RecordProbeResult 在同一事务中写入探测结果与可选的状态变化事件。
func (s *Store) RecordProbeResult(
	ctx context.Context,
	serviceID string,
	result model.ProbeResult,
	transition *model.StatusTransition,
) error {
	if result.StartedAt == 0 {
		result.StartedAt = result.TS
	}
	var payload []byte
	if transition != nil {
		normalized := *transition
		if normalized.Change.ServiceID == "" {
			normalized.Change.ServiceID = serviceID
		}
		if normalized.Change.ServiceID != serviceID {
			return fmt.Errorf(
				"状态变化服务 ID %q 与探测结果服务 ID %q 不一致",
				normalized.Change.ServiceID,
				serviceID,
			)
		}
		if normalized.ChangedAt.IsZero() {
			normalized.ChangedAt = time.Unix(result.TS, 0)
		}
		if normalized.AvailableAt.IsZero() {
			normalized.AvailableAt = normalized.ChangedAt
		}
		if normalized.AvailableAt.Before(normalized.ChangedAt) {
			return fmt.Errorf("状态变化可用时间不能早于变化时间")
		}
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return fmt.Errorf("序列化状态变化失败: %w", err)
		}
		payload = encoded
		transition = &normalized
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始写入探测结果事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO probe_results (service_id, ts, started_at, ok, latency_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		serviceID, result.TS, result.StartedAt, boolInt(result.OK), result.LatencyMS, nullableStr(result.Error),
	); err != nil {
		return fmt.Errorf("写入探测结果失败: %w", err)
	}
	if transition != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO status_transitions(
				service_id, payload_json, changed_at_ms, available_at_ms
			) VALUES (?, ?, ?, ?)`,
			serviceID, string(payload), transition.ChangedAt.UnixMilli(), transition.AvailableAt.UnixMilli(),
		); err != nil {
			return fmt.Errorf("写入状态变化失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交探测结果事务失败: %w", err)
	}
	return nil
}

type claimedTransition struct {
	id            int64
	serviceID     string
	payload       []byte
	changedAtMS   int64
	deliveryGroup string
	leaseToken    string
	transition    model.StatusTransition
}

// ClaimTransitions 原子领取一个稳定事件组。
// 新事件按最早到期事件的 ChangedAt..AvailableAt 固定窗口成组，避免离线
// 积压跨窗口合并；已有 delivery_group 的事件始终整组重领。
// limit 只限制首次成组的事件数。
func (s *Store) ClaimTransitions(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
	limit int,
) (*model.TransitionBatch, string, error) {
	if limit <= 0 {
		return nil, "", nil
	}
	leaseToken, err := newLeaseToken()
	if err != nil {
		return nil, "", fmt.Errorf("生成状态变化租约失败: %w", err)
	}
	newGroup, err := newLeaseToken()
	if err != nil {
		return nil, "", fmt.Errorf("生成状态变化事件组标识失败: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH due_group(group_key) AS MATERIALIZED (
			SELECT delivery_group
			FROM status_transitions
			WHERE delivery_group <> '' AND available_at_ms <= ?
			ORDER BY available_at_ms, id
			LIMIT 1
		),
		new_window(start_ms, end_ms) AS MATERIALIZED (
			SELECT changed_at_ms, available_at_ms
			FROM status_transitions
			WHERE delivery_group = ''
			  AND available_at_ms <= ?
			  AND NOT EXISTS (SELECT 1 FROM due_group)
			ORDER BY available_at_ms, changed_at_ms, id
			LIMIT 1
		),
		claim_ids(id) AS MATERIALIZED (
			SELECT id
			FROM status_transitions
			WHERE delivery_group <> ''
			  AND delivery_group = (SELECT group_key FROM due_group)
			UNION ALL
			SELECT id
			FROM (
				SELECT id
				FROM status_transitions
				WHERE delivery_group = ''
				  AND changed_at_ms >= (SELECT start_ms FROM new_window)
				  AND changed_at_ms <= (SELECT end_ms FROM new_window)
				ORDER BY changed_at_ms, id
				LIMIT ?
			)
		)
		UPDATE status_transitions
		SET available_at_ms = ?,
		    lease_token = ?,
		    delivery_group = CASE
				WHEN delivery_group = '' THEN ?
				ELSE delivery_group
			END
		WHERE id IN (SELECT id FROM claim_ids)
		RETURNING id, service_id, payload_json, changed_at_ms, delivery_group, lease_token`,
		now.UnixMilli(), now.UnixMilli(), limit,
		leaseUntil.UnixMilli(), leaseToken, newGroup,
	)
	if err != nil {
		return nil, "", fmt.Errorf("领取状态变化事件失败: %w", err)
	}
	defer rows.Close()

	claimed := make([]claimedTransition, 0, limit)
	for rows.Next() {
		var item claimedTransition
		if err := rows.Scan(
			&item.id,
			&item.serviceID,
			&item.payload,
			&item.changedAtMS,
			&item.deliveryGroup,
			&item.leaseToken,
		); err != nil {
			return nil, "", fmt.Errorf("扫描状态变化事件失败: %w", err)
		}
		if err := json.Unmarshal(item.payload, &item.transition); err != nil {
			return nil, "", fmt.Errorf("解析状态变化事件 %d 失败: %w", item.id, err)
		}
		if item.transition.Change.ServiceID == "" {
			item.transition.Change.ServiceID = item.serviceID
		}
		if item.transition.Change.ServiceID != item.serviceID {
			return nil, "", fmt.Errorf(
				"状态变化事件 %d 的服务 ID %q 与索引服务 ID %q 不一致",
				item.id,
				item.transition.Change.ServiceID,
				item.serviceID,
			)
		}
		if item.transition.ChangedAt.IsZero() {
			item.transition.ChangedAt = time.UnixMilli(item.changedAtMS)
		}
		claimed = append(claimed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("读取状态变化事件失败: %w", err)
	}
	if len(claimed) == 0 {
		return nil, "", nil
	}

	sort.Slice(claimed, func(i, j int) bool {
		if claimed[i].changedAtMS != claimed[j].changedAtMS {
			return claimed[i].changedAtMS < claimed[j].changedAtMS
		}
		return claimed[i].id < claimed[j].id
	})
	groupKey := claimed[0].deliveryGroup
	for _, item := range claimed {
		if item.deliveryGroup != groupKey || item.leaseToken != leaseToken {
			return nil, "", fmt.Errorf("领取到不一致的状态变化事件组")
		}
	}
	latest := claimed[len(claimed)-1].transition
	batch := &model.TransitionBatch{
		Key:           groupKey,
		ChangedAt:     latest.ChangedAt,
		StatusPageURL: latest.StatusPageURL,
		Changes:       make([]model.StatusChange, 0, len(claimed)),
	}
	for _, item := range claimed {
		batch.Changes = append(batch.Changes, item.transition.Change)
	}
	return batch, leaseToken, nil
}

// RenewTransitions 延长当前消费者对整个事件组的租约。
func (s *Store) RenewTransitions(
	ctx context.Context,
	groupKey string,
	leaseToken string,
	leaseUntil time.Time,
) error {
	if strings.TrimSpace(groupKey) == "" || strings.TrimSpace(leaseToken) == "" {
		return ErrTransitionLeaseLost
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE status_transitions
		SET available_at_ms = ?
		WHERE delivery_group = ? AND lease_token = ?`,
		leaseUntil.UnixMilli(), groupKey, leaseToken,
	)
	if err != nil {
		return fmt.Errorf("续租状态变化事件组失败: %w", err)
	}
	return requireTransitionLease(result)
}

func requireTransitionLease(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取状态变化事件组影响行数失败: %w", err)
	}
	if rows == 0 {
		return ErrTransitionLeaseLost
	}
	return nil
}
