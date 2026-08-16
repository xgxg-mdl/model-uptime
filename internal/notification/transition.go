package notification

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

const transitionClaimLimit = 256

var ErrTransitionLeaseLost = errors.New("状态变化事件组租约已失效")

func (n *Notifier) ingestTransition() (bool, error) {
	now := time.Now()
	batch, leaseToken, err := n.repository.ClaimTransitions(
		n.ctx, now, now.Add(n.deliveryLease), transitionClaimLimit,
	)
	if err != nil || batch == nil {
		return false, err
	}
	if strings.TrimSpace(batch.Key) == "" || strings.TrimSpace(leaseToken) == "" {
		return true, errors.New("状态变化批次缺少稳定 key 或 lease token")
	}
	return true, n.processTransition(batch, leaseToken)
}

func (n *Notifier) processTransition(batch *model.TransitionBatch, leaseToken string) error {
	leaseCtx, cancelLease := context.WithCancel(n.ctx)
	renewed := make(chan error, 1)
	go func() {
		err := n.renewTransitionLease(leaseCtx, batch.Key, leaseToken)
		if err != nil {
			cancelLease()
		}
		renewed <- err
	}()

	config := n.configSnapshot()
	deliveries, processErr := buildDeliveries(config, deliveryBatch{
		ChangedAt: batch.ChangedAt, Changes: batch.Changes, StatusPageURL: batch.StatusPageURL,
	}, func(subscriptionID string, _ time.Time, shardIndex int, changes []model.StatusChange) string {
		return transitionDeliveryDedupeKey(batch.Key, subscriptionID, shardIndex, changes)
	})
	if processErr != nil {
		processErr = fmt.Errorf("渲染状态变化批次 %q: %w", batch.Key, processErr)
	} else {
		processErr = n.commitTransition(
			leaseCtx, cancelLease, batch.Key, leaseToken, deliveries,
		)
	}

	cancelLease()
	renewErr := <-renewed
	// Commit 成功即代表 transition 已删除；此时并发中的最后一次 Renew 可能因
	// 找不到租约而失败，该结果不应覆盖已经持久化成功的事实。
	if processErr == nil {
		return nil
	}
	if renewErr != nil && errors.Is(processErr, context.Canceled) {
		return renewErr
	}
	return errors.Join(processErr, renewErr)
}

func (n *Notifier) commitTransition(
	ctx context.Context,
	cancel context.CancelFunc,
	groupKey string,
	leaseToken string,
	deliveries []Delivery,
) error {
	operation := fmt.Sprintf("提交状态变化批次 %q", groupKey)
	err := n.retryPersistentOperation(ctx, operation, func(ctx context.Context) error {
		err := n.repository.CommitTransitions(ctx, groupKey, leaseToken, deliveries)
		if err == nil {
			cancel()
		}
		return err
	})
	if err == nil && len(deliveries) > 0 {
		n.signalWorker()
	}
	return err
}

func (n *Notifier) renewTransitionLease(
	ctx context.Context,
	groupKey string,
	leaseToken string,
) error {
	interval := n.deliveryLease / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			err := n.repository.RenewTransitions(
				ctx, groupKey, leaseToken, now.Add(n.deliveryLease),
			)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("续租状态变化批次 %q: %w", groupKey, err)
			}
		}
	}
}

func transitionDeliveryDedupeKey(
	batchKey string,
	subscriptionID string,
	shardIndex int,
	changes []model.StatusChange,
) string {
	canonical := append([]model.StatusChange(nil), changes...)
	sortChangesForDelivery(canonical)
	identity := struct {
		BatchKey       string
		SubscriptionID string
		ShardIndex     int
		Changes        []model.StatusChange
	}{
		BatchKey: batchKey, SubscriptionID: subscriptionID,
		ShardIndex: shardIndex, Changes: canonical,
	}
	encoded, _ := json.Marshal(identity) // 字段类型固定，编码不会失败。
	return fmt.Sprintf("transition:%x", sha256.Sum256(encoded))
}

// MemoryOutbox 没有 transition ledger；空领取使它可作为只验证投递行为的
// 内存 adapter。其余两个方法只有违反领取协议时才会被调用。
func (o *MemoryOutbox) ClaimTransitions(
	ctx context.Context,
	_, _ time.Time,
	_ int,
) (*model.TransitionBatch, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", nil
}

func (o *MemoryOutbox) RenewTransitions(context.Context, string, string, time.Time) error {
	return ErrTransitionLeaseLost
}

func (o *MemoryOutbox) CommitTransitions(context.Context, string, string, []Delivery) error {
	return ErrTransitionLeaseLost
}
