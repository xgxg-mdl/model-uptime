package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

func TestOpenMigratesVersionThreeDatabaseWithTransitionLedger(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "version-three.db")
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
		CREATE TABLE notification_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dedupe_key TEXT NOT NULL DEFAULT '',
			subscription_id TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			available_at_ms INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			lease_token TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX idx_notification_outbox_available
			ON notification_outbox(available_at_ms, id);
		CREATE UNIQUE INDEX idx_notification_outbox_dedupe
			ON notification_outbox(dedupe_key) WHERE dedupe_key <> '';
		INSERT INTO probe_results(service_id, ts, ok, latency_ms, error)
		VALUES ('svc-a', 100, 1, 12, NULL);
		INSERT INTO notification_outbox(
			dedupe_key, subscription_id, message, created_at_ms, available_at_ms
		) VALUES ('existing', 'ops', 'persisted', 100000, 100000);
		PRAGMA user_version = 3;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开版本三数据库: %v", err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("迁移后 schema version = %d，期望 %d", version, currentSchemaVersion)
	}
	var outboxCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notification_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("迁移丢失已有 outbox 数据: %d", outboxCount)
	}

	now := time.Unix(1_700_000_000, 0)
	existing, err := store.Claim(context.Background(), now, now.Add(time.Minute))
	if err != nil || existing == nil {
		t.Fatalf("迁移后的已有 outbox 不可领取: delivery=%+v err=%v", existing, err)
	}
	if err := store.MarkSent(context.Background(), existing.ID, existing.LeaseToken); err != nil {
		t.Fatalf("迁移后的 outbox 不可确认: %v", err)
	}

	recordTransition(t, store, "svc-a", now, now, "https://status.example.com")
	batch, token, err := store.ClaimTransitions(context.Background(), now, now.Add(time.Minute), 10)
	if err != nil || batch == nil || token == "" || len(batch.Changes) != 1 {
		t.Fatalf("迁移后的 transition ledger 不可用: batch=%+v token=%q err=%v", batch, token, err)
	}
}

func TestCommitTransitionsAtomicallyEnqueuesAndAcknowledges(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	recordTransition(t, store, "svc-a", now, now, "")
	batch, token, err := store.ClaimTransitions(ctx, now, now.Add(time.Minute), 10)
	if err != nil || batch == nil {
		t.Fatalf("领取状态变化: batch=%+v err=%v", batch, err)
	}
	delivery := notification.Delivery{
		DedupeKey: "batch-a", SubscriptionID: "ops", Text: "down",
		CreatedAt: now, AvailableAt: now,
	}
	if err := store.CommitTransitions(ctx, batch.Key, token, []notification.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"status_transitions": 0, "notification_outbox": 1} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s 行数 = %d，期望 %d", table, count, want)
		}
	}
	if err := store.CommitTransitions(ctx, batch.Key, token, []notification.Delivery{{
		DedupeKey: "stale", SubscriptionID: "ops", Text: "duplicate", AvailableAt: now,
	}}); !errors.Is(err, ErrTransitionLeaseLost) {
		t.Fatalf("已确认租约重复提交应失败: %v", err)
	}
}

func TestCommitTransitionsRollsBackAcknowledgementWhenEnqueueFails(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	recordTransition(t, store, "svc-a", now, now, "")
	batch, token, err := store.ClaimTransitions(ctx, now, now.Add(time.Second), 10)
	if err != nil || batch == nil {
		t.Fatalf("领取状态变化: batch=%+v err=%v", batch, err)
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_transition_delivery
		BEFORE INSERT ON notification_outbox
		BEGIN
			SELECT RAISE(ABORT, 'reject delivery');
		END;`); err != nil {
		t.Fatal(err)
	}
	delivery := notification.Delivery{
		DedupeKey: "rollback", SubscriptionID: "ops", Text: "down", AvailableAt: now,
	}
	if err := store.CommitTransitions(ctx, batch.Key, token, []notification.Delivery{delivery}); err == nil {
		t.Fatal("投递写入失败时 transition 提交应失败")
	}
	for table, want := range map[string]int{"status_transitions": 1, "notification_outbox": 0} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("回滚后 %s 行数 = %d，期望 %d", table, count, want)
		}
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_transition_delivery`); err != nil {
		t.Fatal(err)
	}
	reclaimed, nextToken, err := store.ClaimTransitions(ctx, now.Add(time.Second), now.Add(2*time.Second), 10)
	if err != nil || reclaimed == nil || reclaimed.Key != batch.Key || nextToken == token {
		t.Fatalf("回滚后事件组不可重领: batch=%+v token=%q err=%v", reclaimed, nextToken, err)
	}
}

func TestRecordProbeResultRollsBackResultWhenTransitionInsertFails(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_transition
		BEFORE INSERT ON status_transitions
		BEGIN
			SELECT RAISE(ABORT, 'reject transition');
		END;`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err := store.RecordProbeResult(
		context.Background(),
		"svc-a",
		model.ProbeResult{OK: false, TS: now.Unix(), Error: "down"},
		&model.StatusTransition{
			Change:      model.StatusChange{ServiceID: "svc-a", Status: "down"},
			ChangedAt:   now,
			AvailableAt: now,
		},
	)
	if err == nil {
		t.Fatal("transition 写入失败时应返回错误")
	}
	for _, table := range []string{"probe_results", "status_transitions"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("事务回滚后 %s 仍有 %d 行", table, count)
		}
	}
}

func TestTransitionSurvivesStoreRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "transitions.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	recordTransition(t, first, "svc-a", now, now, "https://status.example.com")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	batch, token, err := second.ClaimTransitions(context.Background(), now, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || token == "" || batch.Changes[0].ServiceID != "svc-a" {
		t.Fatalf("重启后事件不完整: batch=%+v token=%q", batch, token)
	}
	if !batch.ChangedAt.Equal(now) || batch.StatusPageURL != "https://status.example.com" {
		t.Fatalf("重启后批次元数据不完整: %+v", batch)
	}
}

func TestTransitionGroupIsStableAcrossReclaimAndRejectsStaleAck(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	for index := 0; index < 3; index++ {
		changedAt := now.Add(time.Duration(index) * time.Millisecond)
		recordTransition(
			t,
			store,
			fmt.Sprintf("svc-%d", index),
			changedAt,
			changedAt.Add(3*time.Second),
			fmt.Sprintf("https://status.example.com/%d", index),
		)
	}

	claimAt := now.Add(4 * time.Second)
	first, firstToken, err := store.ClaimTransitions(ctx, claimAt, claimAt.Add(time.Second), 2)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || len(first.Changes) != 2 || first.Key == "" || firstToken == "" {
		t.Fatalf("首次领取不完整: batch=%+v token=%q", first, firstToken)
	}
	firstIDs := []string{first.Changes[0].ServiceID, first.Changes[1].ServiceID}
	reclaimed, secondToken, err := store.ClaimTransitions(ctx, claimAt.Add(time.Second), claimAt.Add(2*time.Second), 1)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed == nil || reclaimed.Key != first.Key || len(reclaimed.Changes) != 2 {
		t.Fatalf("重领改变了事件组: first=%+v reclaimed=%+v", first, reclaimed)
	}
	if reclaimed.Changes[0].ServiceID != firstIDs[0] || reclaimed.Changes[1].ServiceID != firstIDs[1] {
		t.Fatalf("重领改变了事件集合: first=%v reclaimed=%+v", firstIDs, reclaimed.Changes)
	}
	if secondToken == firstToken {
		t.Fatal("重领必须签发新的租约 token")
	}
	if err := store.CommitTransitions(ctx, first.Key, firstToken, nil); !errors.Is(err, ErrTransitionLeaseLost) {
		t.Fatalf("旧租约确认应失败: %v", err)
	}
	if err := store.RenewTransitions(ctx, first.Key, firstToken, now.Add(3*time.Second)); !errors.Is(err, ErrTransitionLeaseLost) {
		t.Fatalf("旧租约续租应失败: %v", err)
	}
	if err := store.CommitTransitions(ctx, reclaimed.Key, secondToken, nil); err != nil {
		t.Fatal(err)
	}

	remaining, _, err := store.ClaimTransitions(ctx, claimAt.Add(time.Second), claimAt.Add(2*time.Second), 10)
	if err != nil || remaining == nil || len(remaining.Changes) != 1 || remaining.Changes[0].ServiceID != "svc-2" {
		t.Fatalf("确认稳定组后剩余事件错误: batch=%+v err=%v", remaining, err)
	}
}

func TestTransitionClaimsUseFixedAggregationWindows(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	start := time.Now().Truncate(time.Millisecond)
	recordStatusTransition(t, store, "svc-a", "up", "down", start, start.Add(3*time.Second))
	recordStatusTransition(t, store, "svc-b", "up", "down", start.Add(2*time.Second), start.Add(5*time.Second))
	recordStatusTransition(t, store, "svc-c", "up", "down", start.Add(4*time.Second), start.Add(7*time.Second))

	first, token, err := store.ClaimTransitions(ctx, start.Add(3*time.Second), start.Add(time.Minute), 10)
	if err != nil || first == nil {
		t.Fatalf("领取首个聚合窗口: batch=%+v err=%v", first, err)
	}
	if len(first.Changes) != 2 || first.Changes[0].ServiceID != "svc-a" || first.Changes[1].ServiceID != "svc-b" {
		t.Fatalf("首个固定窗口内容错误: %+v", first.Changes)
	}
	if err := store.CommitTransitions(ctx, first.Key, token, nil); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.ClaimTransitions(ctx, start.Add(7*time.Second), start.Add(time.Minute), 10)
	if err != nil || second == nil || len(second.Changes) != 1 || second.Changes[0].ServiceID != "svc-c" {
		t.Fatalf("窗口外事件应留给下一批: batch=%+v err=%v", second, err)
	}
}

func TestOfflineBacklogKeepsSeparateIncidentWindows(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	start := time.Now().Truncate(time.Millisecond)
	recordStatusTransition(t, store, "svc-a", "up", "down", start, start.Add(3*time.Second))
	recoveredAt := start.Add(time.Minute)
	recordStatusTransition(t, store, "svc-a", "down", "up", recoveredAt, recoveredAt.Add(3*time.Second))
	restartedAt := start.Add(time.Hour)

	down, token, err := store.ClaimTransitions(ctx, restartedAt, restartedAt.Add(time.Minute), 10)
	if err != nil || down == nil || len(down.Changes) != 1 || down.Changes[0].Status != "down" {
		t.Fatalf("离线积压的异常批次错误: batch=%+v err=%v", down, err)
	}
	if err := store.CommitTransitions(ctx, down.Key, token, nil); err != nil {
		t.Fatal(err)
	}
	up, _, err := store.ClaimTransitions(ctx, restartedAt, restartedAt.Add(time.Minute), 10)
	if err != nil || up == nil || len(up.Changes) != 1 || up.Changes[0].Status != "up" || up.Key == down.Key {
		t.Fatalf("离线积压的恢复批次错误: batch=%+v err=%v", up, err)
	}
}

func TestConcurrentTransitionClaimsDoNotOverlap(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	const (
		total     = 40
		consumers = 8
		limit     = total / consumers
	)
	for index := 0; index < total; index++ {
		recordTransition(t, store, fmt.Sprintf("svc-%02d", index), now, now, "")
	}

	var waitGroup sync.WaitGroup
	batches := make(chan *model.TransitionBatch, consumers)
	errorsChannel := make(chan error, consumers)
	for index := 0; index < consumers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			batch, _, err := store.ClaimTransitions(ctx, now, now.Add(time.Minute), limit)
			if err != nil {
				errorsChannel <- err
				return
			}
			batches <- batch
		}()
	}
	waitGroup.Wait()
	close(batches)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("并发领取失败: %v", err)
	}

	seenChanges := make(map[string]struct{}, total)
	seenGroups := make(map[string]struct{}, consumers)
	for batch := range batches {
		if batch == nil {
			t.Error("并发消费者未领取到事件组")
			continue
		}
		if _, exists := seenGroups[batch.Key]; exists {
			t.Errorf("事件组被重复领取: %s", batch.Key)
		}
		seenGroups[batch.Key] = struct{}{}
		for _, change := range batch.Changes {
			if _, exists := seenChanges[change.ServiceID]; exists {
				t.Errorf("事件被重复领取: %s", change.ServiceID)
			}
			seenChanges[change.ServiceID] = struct{}{}
		}
	}
	if len(seenChanges) != total {
		t.Fatalf("并发领取事件数 = %d，期望 %d", len(seenChanges), total)
	}
}

func TestDeleteHistoryAlsoDeletesPendingTransitions(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	now := time.Now()
	for _, serviceID := range []string{"svc-a", "svc-b"} {
		recordTransition(t, store, serviceID, now, now, "")
	}
	if _, err := store.DeleteHistory(context.Background(), "svc-a"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM status_transitions WHERE service_id = 'svc-a'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("删除服务历史后仍有 %d 条待处理事件", count)
	}
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM status_transitions WHERE service_id = 'svc-b'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("删除 svc-a 不应影响 svc-b，剩余 %d 条", count)
	}
}

func TestDeleteHistoryPreservesAlreadyClaimedTransitionGroup(t *testing.T) {
	t.Parallel()
	store := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	for _, serviceID := range []string{"svc-a", "svc-b"} {
		recordTransition(t, store, serviceID, now, now, "")
	}
	first, firstToken, err := store.ClaimTransitions(ctx, now, now.Add(time.Second), 10)
	if err != nil || first == nil || len(first.Changes) != 2 {
		t.Fatalf("领取事件组: batch=%+v err=%v", first, err)
	}
	if _, err := store.DeleteHistory(ctx, "svc-a"); err != nil {
		t.Fatal(err)
	}
	reclaimed, nextToken, err := store.ClaimTransitions(ctx, now.Add(time.Second), now.Add(2*time.Second), 10)
	if err != nil || reclaimed == nil || reclaimed.Key != first.Key || len(reclaimed.Changes) != 2 {
		t.Fatalf("删除历史改变了已领取事件组: batch=%+v err=%v", reclaimed, err)
	}
	if nextToken == firstToken {
		t.Fatal("重领应签发新租约")
	}
}

func recordTransition(
	t *testing.T,
	store *Store,
	serviceID string,
	changedAt time.Time,
	availableAt time.Time,
	statusPageURL string,
) {
	t.Helper()
	recordStatusTransition(t, store, serviceID, "up", "down", changedAt, availableAt, statusPageURL)
}

func recordStatusTransition(
	t *testing.T,
	store *Store,
	serviceID string,
	previousStatus string,
	status string,
	changedAt time.Time,
	availableAt time.Time,
	statusPageURLs ...string,
) {
	t.Helper()
	statusPageURL := ""
	if len(statusPageURLs) > 0 {
		statusPageURL = statusPageURLs[0]
	}
	err := store.RecordProbeResult(
		context.Background(),
		serviceID,
		model.ProbeResult{OK: status == "up", TS: changedAt.Unix(), Error: "down"},
		&model.StatusTransition{
			Change: model.StatusChange{
				ServiceID:      serviceID,
				Model:          serviceID,
				OK:             status == "up",
				PreviousStatus: previousStatus,
				Status:         status,
				LastTS:         changedAt.Unix(),
			},
			ChangedAt:     changedAt,
			AvailableAt:   availableAt,
			StatusPageURL: statusPageURL,
		},
	)
	if err != nil {
		t.Fatalf("记录 %s 状态变化: %v", serviceID, err)
	}
}
