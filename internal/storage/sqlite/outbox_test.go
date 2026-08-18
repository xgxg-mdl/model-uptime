package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

func TestOpenMigratesVersionTwoDatabaseWithWorkingOutbox(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "version-two.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE probe_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id TEXT NOT NULL,
			ts INTEGER NOT NULL,
			ok INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT
		);
		CREATE INDEX idx_probe_results_service_time ON probe_results(service_id, ts, id);
		CREATE INDEX idx_probe_results_ts ON probe_results(ts);
		INSERT INTO probe_results(service_id, ts, ok, latency_ms, error)
		VALUES ('svc-a', 100, 1, 12, NULL);
		PRAGMA user_version = 2;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开版本二数据库: %v", err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("迁移后 schema version = %d，期望 %d", version, currentSchemaVersion)
	}
	history, err := store.LoadHistory(context.Background(), "svc-a", 10)
	if err != nil || len(history) != 1 || history[0].TS != 100 || history[0].StartedAt != 100 {
		t.Fatalf("迁移后探测历史不完整: history=%+v err=%v", history, err)
	}

	now := time.Unix(1_700_000_000, 0)
	delivery := notification.Delivery{
		DedupeKey: "migration-event", SubscriptionID: "ops", Text: "persisted",
		CreatedAt: now, AvailableAt: now,
	}
	if err := store.Enqueue(context.Background(), []notification.Delivery{delivery, delivery}); err != nil {
		t.Fatalf("迁移后的 outbox 不可写: %v", err)
	}
	claimed, err := store.Claim(context.Background(), now, now.Add(time.Minute))
	if err != nil || claimed == nil || claimed.Text != "persisted" {
		t.Fatalf("迁移后的 outbox 不可领取: delivery=%+v err=%v", claimed, err)
	}
	if duplicate, err := store.Claim(context.Background(), now, now.Add(time.Minute)); err != nil || duplicate != nil {
		t.Fatalf("迁移后的唯一索引未去重: delivery=%+v err=%v", duplicate, err)
	}
}

func TestOpenMigratesVersionFiveDatabaseWithoutLosingQueueState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "version-five.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE probe_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id TEXT NOT NULL,
			ts INTEGER NOT NULL,
			ok INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT
		);
		CREATE TABLE notification_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dedupe_key TEXT NOT NULL DEFAULT '',
			subscription_id TEXT NOT NULL,
			message TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '',
			created_at_ms INTEGER NOT NULL,
			available_at_ms INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			permanent_fails INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			lease_token TEXT NOT NULL DEFAULT '',
			quarantined INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE status_transitions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			changed_at_ms INTEGER NOT NULL,
			available_at_ms INTEGER NOT NULL,
			delivery_group TEXT NOT NULL DEFAULT '',
			lease_token TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO notification_outbox(
			dedupe_key, subscription_id, message, created_at_ms, available_at_ms,
			attempts, permanent_fails, last_error, lease_token, quarantined
		) VALUES ('leased', 'ops', 'persisted', 1000, 2000, 7, 2, 'failure', 'lease', 0);
		INSERT INTO status_transitions(
			service_id, payload_json, changed_at_ms, available_at_ms, delivery_group, lease_token
		) VALUES ('svc-a', '{}', 1000, 2000, 'group', 'transition-lease');
		PRAGMA user_version = 5;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for openIndex := 0; openIndex < 2; openIndex++ {
		store, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次打开版本五数据库: %v", openIndex+1, err)
		}
		var version, attempts, permanentFails, outboxCount, transitionCount int
		var lastError, leaseToken, fingerprint string
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`
			SELECT attempts, permanent_fails, last_error, lease_token,
			       failure_config_fingerprint
			FROM notification_outbox WHERE dedupe_key = 'leased'`,
		).Scan(&attempts, &permanentFails, &lastError, &leaseToken, &fingerprint); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM notification_outbox`).Scan(&outboxCount); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM status_transitions`).Scan(&transitionCount); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if version != currentSchemaVersion || attempts != 7 || permanentFails != 2 ||
			lastError != "failure" || leaseToken != "lease" || fingerprint != "" ||
			outboxCount != 1 || transitionCount != 1 {
			store.Close()
			t.Fatalf("迁移破坏队列状态: version=%d attempts=%d permanent=%d error=%q lease=%q fingerprint=%q outbox=%d transitions=%d",
				version, attempts, permanentFails, lastError, leaseToken, fingerprint, outboxCount, transitionCount)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutboxLeaseAcknowledgementAndRetry(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	if err := store.Enqueue(ctx, []notification.Delivery{
		{SubscriptionID: "first", Text: "one", CreatedAt: now, AvailableAt: now},
		{SubscriptionID: "second", Text: "two", CreatedAt: now, AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || first == nil || first.SubscriptionID != "first" {
		t.Fatalf("首次领取 = %+v, err=%v", first, err)
	}
	second, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || second == nil || second.SubscriptionID != "second" {
		t.Fatalf("第二次领取 = %+v, err=%v", second, err)
	}
	if err := store.MarkSent(ctx, second.ID, second.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if delivery, err := store.Claim(ctx, now, now.Add(time.Minute)); err != nil || delivery != nil {
		t.Fatalf("租约内不应重复领取: delivery=%+v err=%v", delivery, err)
	}

	reclaimed, err := store.Claim(ctx, now.Add(time.Minute), now.Add(2*time.Minute))
	if err != nil || reclaimed == nil || reclaimed.ID != first.ID {
		t.Fatalf("租约到期后未重新领取: delivery=%+v err=%v", reclaimed, err)
	}
	if err := store.MarkFailed(ctx, reclaimed.ID, reclaimed.LeaseToken, notification.DeliveryFailure{
		AvailableAt: now.Add(3 * time.Minute), Error: "temporary failure",
	}); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Claim(ctx, now.Add(3*time.Minute), now.Add(4*time.Minute))
	if err != nil || retried == nil || retried.Attempts != 1 || retried.LastError != "temporary failure" {
		t.Fatalf("失败状态没有持久化: delivery=%+v err=%v", retried, err)
	}
	if err := store.MarkSent(ctx, retried.ID, retried.LeaseToken); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxQuarantinePreservesPayloadAndDoesNotBlockFIFO(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	payload := &notification.RenderPayload{
		ChangedAt: now.Add(123456789 * time.Nanosecond),
		Changes: []model.StatusChange{{
			ServiceID: "svc-a", Model: "alpha", Status: "down", PreviousStatus: "up",
		}},
		StatusPageURL: "https://status.example.com/",
	}
	if err := store.Enqueue(ctx, []notification.Delivery{
		{DedupeKey: "bad", SubscriptionID: "ops", Text: "bad", StatusPageURL: "https://status.example.com/", RenderPayload: payload, CreatedAt: now, AvailableAt: now},
		{DedupeKey: "next", SubscriptionID: "ops", Text: "next", CreatedAt: now, AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	bad, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || bad == nil || bad.DedupeKey != "bad" || bad.RenderPayload == nil {
		t.Fatalf("领取首条投递: delivery=%+v err=%v", bad, err)
	}
	if err := store.Quarantine(ctx, bad.ID, bad.LeaseToken, notification.DeliveryFailure{
		Error: "permanent failure", Permanent: true, ConfigFingerprint: "old-config",
	}); err != nil {
		t.Fatal(err)
	}
	next, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || next == nil || next.DedupeKey != "next" {
		t.Fatalf("隔离项阻塞了同订阅后续投递: delivery=%+v err=%v", next, err)
	}
	if err := store.MarkSent(ctx, next.ID, next.LeaseToken); err != nil {
		t.Fatal(err)
	}

	var quarantined, permanentFails int
	var failureFingerprint string
	if err := store.db.QueryRow(`
		SELECT quarantined, permanent_fails, failure_config_fingerprint
		FROM notification_outbox WHERE dedupe_key = 'bad'`,
	).Scan(&quarantined, &permanentFails, &failureFingerprint); err != nil {
		t.Fatal(err)
	}
	if quarantined != 1 || permanentFails != 1 || failureFingerprint != "old-config" {
		t.Fatalf("隔离状态未持久化: quarantined=%d permanent_fails=%d fingerprint=%q",
			quarantined, permanentFails, failureFingerprint)
	}
	if err := store.ReactivateFailures(ctx, map[string]string{"ops": "new-config"}, now); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || recovered == nil || recovered.DedupeKey != "bad" {
		t.Fatalf("配置更新后隔离项未恢复: delivery=%+v err=%v", recovered, err)
	}
	if recovered.Quarantined || recovered.PermanentFails != 0 || recovered.RenderPayload == nil ||
		len(recovered.RenderPayload.Changes) != 1 || recovered.RenderPayload.Changes[0].Model != "alpha" ||
		recovered.StatusPageURL != "https://status.example.com/" ||
		recovered.RenderPayload.StatusPageURL != payload.StatusPageURL ||
		!recovered.RenderPayload.ChangedAt.Equal(payload.ChangedAt) ||
		recovered.FailureConfigFingerprint != "" {
		t.Fatalf("恢复后的 payload 或失败计数错误: %+v", recovered)
	}
}

func TestReactivateFailuresPreservesTransientBackoffMatchingConfigAndLeases(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	future := now.Add(time.Hour)
	if err := store.Enqueue(ctx, []notification.Delivery{
		{DedupeKey: "transient", SubscriptionID: "transient", Text: "one", AvailableAt: now},
		{DedupeKey: "changed", SubscriptionID: "changed", Text: "two", AvailableAt: now},
		{DedupeKey: "same", SubscriptionID: "same", Text: "three", AvailableAt: now},
		{DedupeKey: "leased", SubscriptionID: "leased", Text: "four", AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	claim := func(want string) *notification.Delivery {
		t.Helper()
		delivery, err := store.Claim(ctx, now, future)
		if err != nil || delivery == nil || delivery.DedupeKey != want {
			t.Fatalf("领取 %q: delivery=%+v err=%v", want, delivery, err)
		}
		return delivery
	}
	transient := claim("transient")
	if err := store.MarkFailed(ctx, transient.ID, transient.LeaseToken, notification.DeliveryFailure{
		AvailableAt: future, Error: "server error",
	}); err != nil {
		t.Fatal(err)
	}
	changed := claim("changed")
	if err := store.MarkFailed(ctx, changed.ID, changed.LeaseToken, notification.DeliveryFailure{
		AvailableAt: future, Error: "bad config", Permanent: true, ConfigFingerprint: "old",
	}); err != nil {
		t.Fatal(err)
	}
	same := claim("same")
	if err := store.MarkFailed(ctx, same.ID, same.LeaseToken, notification.DeliveryFailure{
		AvailableAt: future, Error: "still bad", Permanent: true, ConfigFingerprint: "current",
	}); err != nil {
		t.Fatal(err)
	}
	leased := claim("leased")
	if _, err := store.db.Exec(`
		UPDATE notification_outbox
		SET permanent_fails = 1, failure_config_fingerprint = 'old'
		WHERE id = ?`, leased.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.ReactivateFailures(ctx, map[string]string{
		"transient": "new", "changed": "new", "same": "current", "leased": "new",
	}, now); err != nil {
		t.Fatal(err)
	}
	type state struct {
		availableAtMS int64
		permanent     int
		fingerprint   string
		leaseToken    string
	}
	load := func(key string) state {
		t.Helper()
		var result state
		if err := store.db.QueryRow(`
			SELECT available_at_ms, permanent_fails, failure_config_fingerprint, lease_token
			FROM notification_outbox WHERE dedupe_key = ?`, key,
		).Scan(&result.availableAtMS, &result.permanent, &result.fingerprint, &result.leaseToken); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := load("changed"); got.availableAtMS != now.UnixMilli() || got.permanent != 0 || got.fingerprint != "" {
		t.Fatalf("配置已变化的永久失败未恢复: %+v", got)
	}
	if got := load("transient"); got.availableAtMS != future.UnixMilli() || got.permanent != 0 {
		t.Fatalf("临时失败退避被配置更新破坏: %+v", got)
	}
	if got := load("same"); got.availableAtMS != future.UnixMilli() || got.permanent != 1 || got.fingerprint != "current" {
		t.Fatalf("相同配置的永久失败被错误恢复: %+v", got)
	}
	if got := load("leased"); got.leaseToken == "" || got.permanent != 1 || got.fingerprint != "old" {
		t.Fatalf("在途租约被配置更新破坏: %+v", got)
	}
}

func TestClaimFallsBackToPersistedTextForInvalidRenderPayload(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	for index, payload := range []string{"{broken", "{}"} {
		if _, err := store.db.Exec(`
			INSERT INTO notification_outbox(
				dedupe_key, subscription_id, message, payload_json, created_at_ms, available_at_ms
			) VALUES (?, 'ops', ?, ?, ?, ?)`,
			fmt.Sprintf("invalid-%d", index), fmt.Sprintf("fallback-%d", index), payload,
			now.UnixMilli(), now.UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Enqueue(ctx, []notification.Delivery{{
		DedupeKey: "next", SubscriptionID: "ops", Text: "next", AvailableAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		delivery, err := store.Claim(ctx, now, now.Add(time.Minute))
		if err != nil || delivery == nil || delivery.Text != fmt.Sprintf("fallback-%d", index) ||
			delivery.RenderPayload != nil {
			t.Fatalf("损坏 payload 未降级到持久化文本: delivery=%+v err=%v", delivery, err)
		}
		if err := store.MarkSent(ctx, delivery.ID, delivery.LeaseToken); err != nil {
			t.Fatal(err)
		}
	}
	next, err := store.Claim(ctx, now, now.Add(time.Minute))
	if err != nil || next == nil || next.DedupeKey != "next" {
		t.Fatalf("损坏 payload 阻塞了 FIFO: delivery=%+v err=%v", next, err)
	}
}

func TestTruncateOutboxErrorPreservesUTF8Boundary(t *testing.T) {
	t.Parallel()
	truncated := truncateOutboxError(strings.Repeat("界", maxOutboxErrorLength))
	if len(truncated) > maxOutboxErrorLength || !utf8.ValidString(truncated) {
		t.Fatalf("截断结果不是合法 UTF-8: bytes=%d valid=%v", len(truncated), utf8.ValidString(truncated))
	}
}

func TestOutboxRejectsStaleLeaseAndKeepsSubscriptionFIFO(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now()
	if err := store.Enqueue(ctx, []notification.Delivery{
		{DedupeKey: "a-1", SubscriptionID: "a", Text: "down", AvailableAt: now},
		{DedupeKey: "a-2", SubscriptionID: "a", Text: "recovered", AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, now, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if delivery, err := store.Claim(ctx, now, now.Add(time.Second)); err != nil || delivery != nil {
		t.Fatalf("同订阅新消息越过在途旧消息: delivery=%+v err=%v", delivery, err)
	}
	reclaimed, err := store.Claim(ctx, now.Add(time.Second), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSent(ctx, first.ID, first.LeaseToken); !errors.Is(err, notification.ErrLeaseLost) {
		t.Fatalf("过期租约确认应失败: %v", err)
	}
	if err := store.MarkFailed(ctx, first.ID, first.LeaseToken, notification.DeliveryFailure{
		AvailableAt: now.Add(time.Hour), Error: "stale",
	}); !errors.Is(err, notification.ErrLeaseLost) {
		t.Fatalf("过期租约重试更新应失败: %v", err)
	}
	if err := store.Quarantine(ctx, first.ID, first.LeaseToken, notification.DeliveryFailure{
		Error: "stale", Permanent: true, ConfigFingerprint: "old",
	}); !errors.Is(err, notification.ErrLeaseLost) {
		t.Fatalf("过期租约隔离应失败: %v", err)
	}
	if err := store.MarkSent(ctx, reclaimed.ID, reclaimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	next, err := store.Claim(ctx, now.Add(time.Second), now.Add(2*time.Second))
	if err != nil || next == nil || next.Text != "recovered" {
		t.Fatalf("旧消息确认后没有按序领取新消息: delivery=%+v err=%v", next, err)
	}
}

func TestOutboxConcurrentClaimIsAtomic(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now()
	const total = 40
	deliveries := make([]notification.Delivery, total)
	for index := range deliveries {
		deliveries[index] = notification.Delivery{
			DedupeKey:      fmt.Sprintf("delivery-%d", index),
			SubscriptionID: fmt.Sprintf("subscription-%d", index),
			Text:           "message", AvailableAt: now,
		}
	}
	if err := store.Enqueue(ctx, deliveries); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	claimed := make(chan int64, total)
	errorsChannel := make(chan error, total)
	for index := 0; index < total; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			delivery, err := store.Claim(ctx, now, now.Add(time.Minute))
			if err != nil {
				errorsChannel <- err
				return
			}
			if delivery != nil {
				claimed <- delivery.ID
			}
		}()
	}
	wg.Wait()
	close(claimed)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("并发领取: %v", err)
	}
	seen := make(map[int64]struct{}, total)
	for id := range claimed {
		if _, duplicate := seen[id]; duplicate {
			t.Errorf("同一 outbox 记录被重复领取: %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != total {
		t.Fatalf("并发领取数 = %d，期望 %d", len(seen), total)
	}
}

func TestOutboxSurvivesStoreRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "outbox.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := first.Enqueue(context.Background(), []notification.Delivery{{
		SubscriptionID: "ops", Text: "persisted", CreatedAt: now, AvailableAt: now,
	}}); err != nil {
		first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	delivery, err := second.Claim(context.Background(), now.Add(time.Second), now.Add(time.Minute))
	if err != nil || delivery == nil || delivery.Text != "persisted" {
		t.Fatalf("重启后投递 = %+v, err=%v", delivery, err)
	}
}

func TestOutboxBatchEnqueueRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_second_delivery
		BEFORE INSERT ON notification_outbox
		WHEN NEW.subscription_id = 'second'
		BEGIN
			SELECT RAISE(ABORT, 'reject second');
		END;`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err := store.Enqueue(context.Background(), []notification.Delivery{
		{SubscriptionID: "first", Text: "one", CreatedAt: now, AvailableAt: now},
		{SubscriptionID: "second", Text: "two", CreatedAt: now, AvailableAt: now},
	})
	if err == nil {
		t.Fatal("第二条写入失败时整批应回滚")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notification_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("事务回滚后仍有 %d 条 outbox 记录", count)
	}
}

func TestEnqueueDailyReportsKeepsPersistentPerSubscriptionDeduplication(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	now := time.Now()
	deliveries := []notification.Delivery{{
		DedupeKey: "daily:2026-08-16:ops:0", SubscriptionID: "ops", Text: "daily",
		CreatedAt: now, AvailableAt: now,
	}}
	if err := store.EnqueueDailyReports(context.Background(), "2026-08-16", deliveries); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background(), now.Add(time.Second), now.Add(time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("首次日报未入箱: delivery=%+v err=%v", claimed, err)
	}
	if err := store.MarkSent(context.Background(), claimed.ID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueDailyReports(context.Background(), "2026-08-16", deliveries); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Claim(context.Background(), now.Add(2*time.Second), now.Add(time.Minute))
	if err != nil || claimed != nil {
		t.Fatalf("已发送日报不应再次入箱: delivery=%+v err=%v", claimed, err)
	}
}
