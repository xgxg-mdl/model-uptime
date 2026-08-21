package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

const maxOutboxErrorLength = 2048

// Enqueue 在单个事务中保存一批通知，避免同一状态变化只持久化部分订阅。
func (s *Store) Enqueue(ctx context.Context, deliveries []notification.Delivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始通知 outbox 事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := enqueueDeliveries(ctx, tx, deliveries); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交通知 outbox 事务失败: %w", err)
	}
	return nil
}

// EnqueueDailyReports 把一个订阅某日的完整日报与“已入箱”账本原子保存。
// 账本不随 outbox 成功发送而删除，因此进程重启或多个实例不能重复日报。
func (s *Store) EnqueueDailyReports(ctx context.Context, reportDate string, deliveries []notification.Delivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	subscriptionID := deliveries[0].SubscriptionID
	if subscriptionID == "" {
		return fmt.Errorf("日报缺少订阅 ID")
	}
	for _, delivery := range deliveries {
		if delivery.SubscriptionID != subscriptionID {
			return fmt.Errorf("日报投递不能跨订阅")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始日报事务失败: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO daily_report_runs(report_date, subscription_id) VALUES (?, ?)
		ON CONFLICT(report_date, subscription_id) DO NOTHING`, reportDate, subscriptionID)
	if err != nil {
		return fmt.Errorf("记录日报账本失败: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取日报账本写入结果失败: %w", err)
	}
	if inserted == 0 {
		return tx.Commit()
	}
	if err := enqueueDeliveries(ctx, tx, deliveries); err != nil {
		return fmt.Errorf("保存日报通知投递: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交日报事务失败: %w", err)
	}
	return nil
}

func enqueueDeliveries(ctx context.Context, tx *sql.Tx, deliveries []notification.Delivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO notification_outbox(
			dedupe_key, subscription_id, message, payload_json, status_page_url, created_at_ms, available_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) WHERE dedupe_key <> '' DO NOTHING`)
	if err != nil {
		return fmt.Errorf("准备通知 outbox 写入失败: %w", err)
	}
	defer statement.Close()
	for _, delivery := range deliveries {
		createdAt := delivery.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		availableAt := delivery.AvailableAt
		if availableAt.IsZero() {
			availableAt = createdAt
		}
		payloadJSON := ""
		if delivery.RenderPayload != nil {
			encoded, err := json.Marshal(delivery.RenderPayload)
			if err != nil {
				return fmt.Errorf("序列化通知重渲染数据失败: %w", err)
			}
			payloadJSON = string(encoded)
		}
		if _, err := statement.ExecContext(ctx,
			delivery.DedupeKey, delivery.SubscriptionID, delivery.Text, payloadJSON, delivery.StatusPageURL,
			createdAt.UnixMilli(), availableAt.UnixMilli(),
		); err != nil {
			return fmt.Errorf("写入通知 outbox 失败: %w", err)
		}
	}
	return nil
}

// CommitTransitions 在同一事务中确认 transition 事件组并保存其全部通知投递。
// 任一步失败都会保留完整事件组，调用方可在租约到期后重新处理。
func (s *Store) CommitTransitions(
	ctx context.Context,
	groupKey string,
	leaseToken string,
	deliveries []notification.Delivery,
) error {
	if strings.TrimSpace(groupKey) == "" || strings.TrimSpace(leaseToken) == "" {
		return ErrTransitionLeaseLost
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始提交状态变化事务失败: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM status_transitions
		WHERE delivery_group = ? AND lease_token = ?`, groupKey, leaseToken)
	if err != nil {
		return fmt.Errorf("确认状态变化事件组失败: %w", err)
	}
	if err := requireTransitionLease(result); err != nil {
		return err
	}
	if err := enqueueDeliveries(ctx, tx, deliveries); err != nil {
		return fmt.Errorf("保存状态变化通知投递: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交状态变化通知事务失败: %w", err)
	}
	return nil
}

// Claim 原子领取最早到期的通知，并把它隐藏到租约结束。
// 发送进程在 MarkSent 前退出时，同一记录会在租约到期后重新出现。
func (s *Store) Claim(ctx context.Context, now, leaseUntil time.Time) (*notification.Delivery, error) {
	leaseToken, err := newLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("生成通知 outbox 租约失败: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE notification_outbox
		SET available_at_ms = ?, lease_token = ?
		WHERE id = (
			SELECT candidate.id FROM notification_outbox AS candidate
			WHERE candidate.available_at_ms <= ? AND candidate.quarantined = 0
			  AND NOT EXISTS (
				SELECT 1 FROM notification_outbox AS older
				WHERE older.subscription_id = candidate.subscription_id
				  AND older.id < candidate.id
				  AND older.quarantined = 0
			  )
			ORDER BY candidate.available_at_ms, candidate.id
			LIMIT 1
		)
		RETURNING id, dedupe_key, subscription_id, message, payload_json, status_page_url,
		          created_at_ms, available_at_ms, attempts, permanent_fails,
		          failure_config_fingerprint, last_error, lease_token, quarantined`,
		leaseUntil.UnixMilli(), leaseToken, now.UnixMilli(),
	)
	var delivery notification.Delivery
	var createdAtMillis, availableAtMillis int64
	var payloadJSON string
	var quarantined int
	if err := row.Scan(
		&delivery.ID, &delivery.DedupeKey, &delivery.SubscriptionID, &delivery.Text,
		&payloadJSON, &delivery.StatusPageURL, &createdAtMillis, &availableAtMillis, &delivery.Attempts,
		&delivery.PermanentFails, &delivery.FailureConfigFingerprint,
		&delivery.LastError, &delivery.LeaseToken, &quarantined,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("领取通知 outbox 记录失败: %w", err)
	}
	delivery.CreatedAt = time.UnixMilli(createdAtMillis)
	delivery.AvailableAt = time.UnixMilli(availableAtMillis)
	delivery.Quarantined = quarantined != 0
	if payloadJSON != "" {
		var payload notification.RenderPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err == nil &&
			!payload.ChangedAt.IsZero() && len(payload.Changes) > 0 {
			for index := range payload.Changes {
				if payload.Changes[index].ServiceUID == "" {
					payload.Changes[index].ServiceUID = payload.Changes[index].LegacyServiceID
				}
				payload.Changes[index].LegacyServiceID = ""
			}
			delivery.RenderPayload = &payload
		}
		// payload 是可选的重渲染增强数据。损坏或来自未知版本的数据不能
		// 变成永久阻塞 FIFO 的 poison row；此时继续发送已持久化的文本。
	}
	return &delivery, nil
}

func (s *Store) Renew(ctx context.Context, id int64, leaseToken string, leaseUntil time.Time) error {
	if leaseToken == "" {
		return notification.ErrLeaseLost
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET available_at_ms = ?
		WHERE id = ? AND lease_token = ?`, leaseUntil.UnixMilli(), id, leaseToken)
	if err != nil {
		return fmt.Errorf("续租通知 outbox 记录失败: %w", err)
	}
	return requireLease(result)
}

func (s *Store) MarkSent(ctx context.Context, id int64, leaseToken string) error {
	if leaseToken == "" {
		return notification.ErrLeaseLost
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM notification_outbox WHERE id = ? AND lease_token = ?`, id, leaseToken)
	if err != nil {
		return fmt.Errorf("确认通知 outbox 记录失败: %w", err)
	}
	return requireLease(result)
}

func (s *Store) MarkFailed(
	ctx context.Context,
	id int64,
	leaseToken string,
	failure notification.DeliveryFailure,
) error {
	if leaseToken == "" {
		return notification.ErrLeaseLost
	}
	lastError := truncateOutboxError(failure.Error)
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox SET available_at_ms = ?, attempts = attempts + 1,
		       permanent_fails = CASE WHEN ? <> 0 THEN permanent_fails + 1 ELSE 0 END,
		       failure_config_fingerprint = CASE WHEN ? <> 0 THEN ? ELSE '' END,
		       last_error = ?, lease_token = ''
		WHERE id = ? AND lease_token = ?`,
		failure.AvailableAt.UnixMilli(), boolInt(failure.Permanent),
		boolInt(failure.Permanent), failure.ConfigFingerprint, lastError, id, leaseToken)
	if err != nil {
		return fmt.Errorf("更新通知 outbox 重试状态失败: %w", err)
	}
	return requireLease(result)
}

func (s *Store) Quarantine(
	ctx context.Context,
	id int64,
	leaseToken string,
	failure notification.DeliveryFailure,
) error {
	if leaseToken == "" {
		return notification.ErrLeaseLost
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_outbox
		SET attempts = attempts + 1, permanent_fails = permanent_fails + 1,
		    failure_config_fingerprint = ?, last_error = ?, lease_token = '', quarantined = 1
		WHERE id = ? AND lease_token = ?`,
		failure.ConfigFingerprint, truncateOutboxError(failure.Error), id, leaseToken)
	if err != nil {
		return fmt.Errorf("隔离通知 outbox 记录失败: %w", err)
	}
	return requireLease(result)
}

func (s *Store) ReactivateFailures(
	ctx context.Context,
	configFingerprints map[string]string,
	availableAt time.Time,
) error {
	if len(configFingerprints) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始恢复通知 outbox 事务失败: %w", err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `
		UPDATE notification_outbox
		SET available_at_ms = ?, permanent_fails = 0,
		    failure_config_fingerprint = '', quarantined = 0
		WHERE subscription_id = ? AND lease_token = ''
		  AND (quarantined <> 0 OR permanent_fails > 0)
		  AND failure_config_fingerprint <> ?`)
	if err != nil {
		return fmt.Errorf("准备恢复通知 outbox 记录失败: %w", err)
	}
	defer statement.Close()
	for subscriptionID, fingerprint := range configFingerprints {
		if strings.TrimSpace(subscriptionID) == "" || strings.TrimSpace(fingerprint) == "" {
			continue
		}
		if _, err := statement.ExecContext(
			ctx, availableAt.UnixMilli(), subscriptionID, fingerprint,
		); err != nil {
			return fmt.Errorf("恢复订阅 %q 的通知 outbox 记录失败: %w", subscriptionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交恢复通知 outbox 事务失败: %w", err)
	}
	return nil
}

func truncateOutboxError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxOutboxErrorLength {
		return value
	}
	end := maxOutboxErrorLength
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func requireLease(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取通知 outbox 影响行数失败: %w", err)
	}
	if rows != 1 {
		return notification.ErrLeaseLost
	}
	return nil
}

func newLeaseToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

var _ notification.Repository = (*Store)(nil)
