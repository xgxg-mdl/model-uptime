package notification

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryOutboxKeepsPerSubscriptionOrder(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	now := time.Now()
	if err := outbox.Enqueue(context.Background(), []Delivery{
		{DedupeKey: "a-1", SubscriptionID: "a", Text: "down", AvailableAt: now.Add(time.Minute)},
		{DedupeKey: "a-2", SubscriptionID: "a", Text: "recovered", AvailableAt: now},
		{DedupeKey: "b-1", SubscriptionID: "b", Text: "other", AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	delivery, err := outbox.Claim(context.Background(), now, now.Add(time.Minute))
	if err != nil || delivery == nil || delivery.SubscriptionID != "b" {
		t.Fatalf("其他订阅不应被 a 的旧消息阻塞: delivery=%+v err=%v", delivery, err)
	}
	if err := outbox.MarkSent(context.Background(), delivery.ID, delivery.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if delivery, err = outbox.Claim(context.Background(), now, now.Add(time.Minute)); err != nil || delivery != nil {
		t.Fatalf("同订阅新消息越过了未到期旧消息: delivery=%+v err=%v", delivery, err)
	}
}

func TestMemoryOutboxRejectsStaleAcknowledgement(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	now := time.Now()
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "one", SubscriptionID: "a", Text: "message", AvailableAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := outbox.Claim(context.Background(), now, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := outbox.Claim(context.Background(), now.Add(time.Second), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseToken == second.LeaseToken {
		t.Fatal("重新领取必须生成新的租约 token")
	}
	if err := outbox.MarkSent(context.Background(), first.ID, first.LeaseToken); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("过期 worker 的确认应被拒绝: %v", err)
	}
	if err := outbox.MarkSent(context.Background(), second.ID, second.LeaseToken); err != nil {
		t.Fatal(err)
	}
}
