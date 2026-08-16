package notification

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

// permanentFailureAttemptLimit 在给配置热修复留出三次重试机会后，
// 隔离无法恢复的 Telegram 4xx，避免永久阻塞同一订阅的后续消息。
const permanentFailureAttemptLimit = 4

type deliveryBatch struct {
	ChangedAt     time.Time
	Changes       []model.StatusChange
	StatusPageURL string
}

type deliveryKeyFunc func(
	subscriptionID string,
	changedAt time.Time,
	shardIndex int,
	changes []model.StatusChange,
) string

func buildDeliveries(config runtimeConfig, batch deliveryBatch, dedupeKey deliveryKeyFunc) ([]Delivery, error) {
	changes := finalChanges(batch.Changes)
	if len(changes) == 0 {
		return nil, nil
	}
	changedAt := batch.ChangedAt
	if changedAt.IsZero() {
		changedAt = time.Now()
	}

	var errs []error
	var deliveries []Delivery
	for _, subscription := range config.subscriptions {
		if !subscription.Enabled {
			continue
		}
		selected := selectChanges(changes, subscription.ServiceIDs)
		if len(selected) == 0 {
			continue
		}
		sortChangesForDelivery(selected)
		shards, err := renderDeliveryShards(subscription, changedAt, selected, batch.StatusPageURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("渲染订阅 %q: %w", subscription.ID, err))
			continue
		}
		availableAt := time.Now()
		for shardIndex, shard := range shards {
			payload := &RenderPayload{
				ChangedAt: changedAt, Changes: append([]model.StatusChange(nil), shard.changes...),
				StatusPageURL: batch.StatusPageURL,
			}
			deliveries = append(deliveries, Delivery{
				DedupeKey: dedupeKey(
					subscription.ID, changedAt, shardIndex, shard.changes,
				),
				SubscriptionID: subscription.ID,
				Text:           shard.text,
				RenderPayload:  payload,
				CreatedAt:      changedAt,
				AvailableAt:    availableAt,
			})
		}
	}
	return deliveries, errors.Join(errs...)
}

type deliveryShard struct {
	changes []model.StatusChange
	text    string
}

// renderDeliveryShards 贪心扩展当前分片；一旦越过 Telegram 上限，就提交
// 上一个有效分片并从当前 change 重新开始。固定模板本身过长时整批失败。
func renderDeliveryShards(
	subscription compiledSubscription,
	changedAt time.Time,
	changes []model.StatusChange,
	statusPageURL string,
) ([]deliveryShard, error) {
	shards := make([]deliveryShard, 0, 1)
	current := make([]model.StatusChange, 0, len(changes))
	currentText := ""
	for _, change := range changes {
		candidate := append(append([]model.StatusChange(nil), current...), change)
		text, err := renderDeliveryText(subscription, changedAt, candidate, statusPageURL)
		if err == nil {
			current = candidate
			currentText = text
			continue
		}
		if !errors.Is(err, ErrMessageTooLong) || len(current) == 0 {
			return nil, err
		}
		shards = append(shards, deliveryShard{
			changes: append([]model.StatusChange(nil), current...),
			text:    currentText,
		})
		current = []model.StatusChange{change}
		currentText, err = renderDeliveryText(subscription, changedAt, current, statusPageURL)
		if err != nil {
			return nil, err
		}
	}
	if len(current) > 0 {
		shards = append(shards, deliveryShard{
			changes: append([]model.StatusChange(nil), current...),
			text:    currentText,
		})
	}
	return shards, nil
}

func renderDeliveryText(
	subscription compiledSubscription,
	changedAt time.Time,
	changes []model.StatusChange,
	statusPageURL string,
) (string, error) {
	text, err := executeTemplate(subscription.template, NewTemplateContext(changedAt, changes))
	if err != nil {
		return "", err
	}
	return appendStatusPageLink(text, statusPageURL, subscription.Language)
}

func finalChanges(changes []model.StatusChange) []model.StatusChange {
	positions := make(map[string]int, len(changes))
	merged := make([]model.StatusChange, 0, len(changes))
	for _, change := range changes {
		change.ServiceID = strings.TrimSpace(change.ServiceID)
		if change.ServiceID == "" {
			continue
		}
		if change.Status == "" {
			if change.OK {
				change.Status = "up"
			} else {
				change.Status = "down"
			}
		}
		if change.Status != "up" && change.Status != "down" {
			continue
		}
		change.OK = change.Status == "up"
		if position, exists := positions[change.ServiceID]; exists {
			// 聚合窗口以首个旧状态和最后新状态为准，避免 up -> down -> up
			// 被误报为恢复事件。
			change.PreviousStatus = merged[position].PreviousStatus
			merged[position] = change
			continue
		}
		positions[change.ServiceID] = len(merged)
		merged = append(merged, change)
	}
	result := make([]model.StatusChange, 0, len(merged))
	for _, change := range merged {
		if change.PreviousStatus != change.Status {
			result = append(result, change)
		}
	}
	return result
}

func selectChanges(changes []model.StatusChange, serviceIDs []string) []model.StatusChange {
	selectedIDs := make(map[string]struct{}, len(serviceIDs))
	for _, id := range serviceIDs {
		selectedIDs[id] = struct{}{}
	}
	selected := make([]model.StatusChange, 0, len(changes))
	for _, change := range changes {
		if _, ok := selectedIDs[change.ServiceID]; ok {
			selected = append(selected, change)
		}
	}
	return selected
}

func sortChangesForDelivery(changes []model.StatusChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		left, right := strings.ToLower(changes[i].Model), strings.ToLower(changes[j].Model)
		if left == right {
			return changes[i].ServiceID < changes[j].ServiceID
		}
		return left < right
	})
}

func (n *Notifier) drainOne() (bool, error) {
	now := time.Now()
	delivery, err := n.repository.Claim(n.ctx, now, now.Add(n.deliveryLease))
	if err != nil || delivery == nil {
		return false, err
	}
	job, active, resolveErr := n.resolveDelivery(delivery)
	if !active {
		if err := n.repository.MarkSent(n.ctx, delivery.ID, delivery.LeaseToken); err != nil {
			return true, fmt.Errorf("取消已禁用的 Telegram 通知: %w", err)
		}
		return true, nil
	}
	if err := resolveErr; err == nil {
		err = n.sendClaimed(delivery, job)
		if err == nil {
			if err := n.repository.MarkSent(n.ctx, delivery.ID, delivery.LeaseToken); err != nil {
				return true, fmt.Errorf("确认 Telegram 通知投递: %w", err)
			}
			return true, nil
		}
		resolveErr = err
	}
	if resolveErr != nil {
		n.logger.Error("Telegram 通知发送失败", "subscription", delivery.SubscriptionID, "err", resolveErr)
		if markErr := n.handleDeliveryFailure(delivery, job, resolveErr); markErr != nil {
			return true, errors.Join(resolveErr, markErr)
		}
		return true, nil
	}
	return true, nil
}

func isPermanentDeliveryFailure(err error) bool {
	var telegramError *deliveryError
	return errors.As(err, &telegramError) && !telegramError.retryable
}

func (n *Notifier) resolveDelivery(delivery *Delivery) (sendJob, bool, error) {
	config := n.configSnapshot()
	for _, subscription := range config.subscriptions {
		if subscription.ID != delivery.SubscriptionID {
			continue
		}
		if !subscription.Enabled {
			return sendJob{}, false, nil
		}
		job := sendJob{
			botToken: config.botToken, chatID: subscription.ChatID,
			text: delivery.Text, name: delivery.SubscriptionID,
			configFingerprint: subscription.fingerprint,
		}
		if delivery.RenderPayload != nil {
			payload := delivery.RenderPayload
			changes := selectChanges(payload.Changes, subscription.ServiceIDs)
			if len(changes) == 0 {
				return sendJob{}, false, nil
			}
			sortChangesForDelivery(changes)
			text, err := renderDeliveryText(
				subscription, payload.ChangedAt, changes, payload.StatusPageURL,
			)
			if err != nil {
				return job, true, &deliveryError{
					err: fmt.Errorf("使用当前订阅配置重新渲染通知: %w", err),
				}
			}
			job.text = text
		}
		return job, true, nil
	}
	return sendJob{}, false, nil
}

func (n *Notifier) sendClaimed(delivery *Delivery, job sendJob) error {
	ctx, cancel := context.WithCancel(n.ctx)
	renewed := make(chan error, 1)
	go func() {
		err := n.renewLease(ctx, delivery)
		if err != nil {
			cancel()
		}
		renewed <- err
	}()
	sendErr := n.sendWithRetry(ctx, job)
	cancel()
	renewErr := <-renewed
	if renewErr != nil {
		return renewErr
	}
	return sendErr
}

func (n *Notifier) renewLease(ctx context.Context, delivery *Delivery) error {
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
			if err := n.repository.Renew(ctx, delivery.ID, delivery.LeaseToken, now.Add(n.deliveryLease)); err != nil {
				return fmt.Errorf("续租 Telegram 通知: %w", err)
			}
		}
	}
}

func (n *Notifier) handleDeliveryFailure(delivery *Delivery, job sendJob, sendErr error) error {
	failedPermanently := isPermanentDeliveryFailure(sendErr)
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	configChanged := true
	for _, subscription := range n.config.subscriptions {
		if subscription.ID == job.name && subscription.Enabled {
			configChanged = job.configFingerprint != subscription.fingerprint
			break
		}
	}
	permanent := failedPermanently && !configChanged
	if permanent && delivery.PermanentFails+1 >= permanentFailureAttemptLimit {
		n.logger.Error(
			"永久失败的 Telegram 通知已隔离",
			"subscription", delivery.SubscriptionID,
			"attempts", delivery.Attempts+1,
			"permanent_failures", delivery.PermanentFails+1,
			"err", sendErr,
		)
		if err := n.repository.Quarantine(n.ctx, delivery.ID, delivery.LeaseToken, DeliveryFailure{
			Error: sendErr.Error(), Permanent: true,
			ConfigFingerprint: job.configFingerprint,
		}); err != nil {
			return fmt.Errorf("隔离 Telegram 通知: %w", err)
		}
		return nil
	}
	delay := n.redeliveryDelay(delivery.Attempts, sendErr)
	if configChanged && failedPermanently {
		// 旧配置导致的确定性 4xx 应立即用新配置重试；429、网络和服务端
		// 故障与配置无关，仍须保留 Retry-After 或指数退避。
		delay = 0
	}
	if err := n.repository.MarkFailed(n.ctx, delivery.ID, delivery.LeaseToken, DeliveryFailure{
		AvailableAt: time.Now().Add(delay), Error: sendErr.Error(), Permanent: permanent,
		ConfigFingerprint: job.configFingerprint,
	}); err != nil {
		return fmt.Errorf("延后 Telegram 通知重试: %w", err)
	}
	return nil
}

func (n *Notifier) redeliveryDelay(attempts int, sendErr error) time.Duration {
	if len(n.redeliveryDelays) == 0 {
		return 0
	}
	if attempts < 0 {
		attempts = 0
	}
	var delay time.Duration
	if attempts >= len(n.redeliveryDelays) {
		delay = n.redeliveryDelays[len(n.redeliveryDelays)-1]
	} else {
		delay = n.redeliveryDelays[attempts]
	}
	var telegramError *deliveryError
	if errors.As(sendErr, &telegramError) && telegramError.retryAfter > delay {
		delay = telegramError.retryAfter
	}
	return delay
}
