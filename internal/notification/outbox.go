package notification

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

var ErrLeaseLost = errors.New("通知 outbox 租约已失效")

// Delivery 是 outbox 中一条可独立确认的 Telegram 投递。
// Bot Token 不进入 outbox；实际发送时使用当前已生效的通知配置。
type Delivery struct {
	ID                       int64
	DedupeKey                string
	SubscriptionID           string
	Text                     string
	RenderPayload            *RenderPayload
	CreatedAt                time.Time
	AvailableAt              time.Time
	Attempts                 int
	PermanentFails           int
	FailureConfigFingerprint string
	LastError                string
	LeaseToken               string
	Quarantined              bool
}

// RenderPayload 保留重新应用当前订阅模板所需的领域数据。配置修复后，
// 已隔离投递可以重新渲染，而不是继续发送由旧模板生成的无效 HTML。
type RenderPayload struct {
	ChangedAt     time.Time            `json:"changed_at"`
	Changes       []model.StatusChange `json:"changes"`
	StatusPageURL string               `json:"status_page_url,omitempty"`
}

// DeliveryFailure 保留一次失败的重试时间和实际投递配置。永久失败的配置
// 指纹用于判断配置修复后是否应该重新激活消息。
type DeliveryFailure struct {
	AvailableAt       time.Time
	Error             string
	Permanent         bool
	ConfigFingerprint string
}

// Repository 是通知模块消费的持久化接口。transition 确认与 outbox 写入
// 必须由同一个 adapter 原子提交，避免调用方把两个存储错误组合后丢失通知。
type Repository interface {
	Claim(context.Context, time.Time, time.Time) (*Delivery, error)
	Renew(context.Context, int64, string, time.Time) error
	MarkSent(context.Context, int64, string) error
	MarkFailed(context.Context, int64, string, DeliveryFailure) error
	Quarantine(context.Context, int64, string, DeliveryFailure) error
	ReactivateFailures(context.Context, map[string]string, time.Time) error
	ClaimTransitions(context.Context, time.Time, time.Time, int) (*model.TransitionBatch, string, error)
	RenewTransitions(context.Context, string, string, time.Time) error
	CommitTransitions(context.Context, string, string, []Delivery) error
}

// MemoryOutbox 是测试和无持久化场景使用的线程安全 adapter。
// 生产装配使用 SQLite adapter，进程重启后仍可继续投递。
type MemoryOutbox struct {
	mu         sync.Mutex
	nextID     int64
	nextLease  int64
	deliveries []Delivery
}

func NewMemoryOutbox() *MemoryOutbox { return &MemoryOutbox{} }

func (o *MemoryOutbox) Enqueue(ctx context.Context, deliveries []Delivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, delivery := range deliveries {
		if delivery.DedupeKey != "" && o.containsDedupeKey(delivery.DedupeKey) {
			continue
		}
		o.nextID++
		delivery.ID = o.nextID
		o.deliveries = append(o.deliveries, cloneDelivery(delivery))
	}
	return nil
}

func (o *MemoryOutbox) Claim(ctx context.Context, now, leaseUntil time.Time) (*Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		delivery := &o.deliveries[index]
		if delivery.Quarantined || delivery.AvailableAt.After(now) ||
			o.hasOlderDelivery(index, delivery.SubscriptionID) {
			continue
		}
		o.nextLease++
		delivery.LeaseToken = fmt.Sprintf("memory-%d", o.nextLease)
		delivery.AvailableAt = leaseUntil
		copy := cloneDelivery(*delivery)
		return &copy, nil
	}
	return nil, nil
}

func (o *MemoryOutbox) Renew(ctx context.Context, id int64, leaseToken string, leaseUntil time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		delivery := &o.deliveries[index]
		if delivery.ID == id && delivery.LeaseToken == leaseToken {
			delivery.AvailableAt = leaseUntil
			return nil
		}
	}
	return ErrLeaseLost
}

func (o *MemoryOutbox) MarkSent(ctx context.Context, id int64, leaseToken string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		if o.deliveries[index].ID != id || o.deliveries[index].LeaseToken != leaseToken {
			continue
		}
		o.deliveries = append(o.deliveries[:index], o.deliveries[index+1:]...)
		return nil
	}
	return ErrLeaseLost
}

func (o *MemoryOutbox) MarkFailed(
	ctx context.Context,
	id int64,
	leaseToken string,
	failure DeliveryFailure,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		delivery := &o.deliveries[index]
		if delivery.ID != id || delivery.LeaseToken != leaseToken {
			continue
		}
		delivery.Attempts++
		if failure.Permanent {
			delivery.PermanentFails++
			delivery.FailureConfigFingerprint = failure.ConfigFingerprint
		} else {
			delivery.PermanentFails = 0
			delivery.FailureConfigFingerprint = ""
		}
		delivery.AvailableAt = failure.AvailableAt
		delivery.LastError = failure.Error
		delivery.LeaseToken = ""
		return nil
	}
	return ErrLeaseLost
}

func (o *MemoryOutbox) Quarantine(
	ctx context.Context,
	id int64,
	leaseToken string,
	failure DeliveryFailure,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		delivery := &o.deliveries[index]
		if delivery.ID != id || delivery.LeaseToken != leaseToken {
			continue
		}
		delivery.Attempts++
		delivery.PermanentFails++
		delivery.FailureConfigFingerprint = failure.ConfigFingerprint
		delivery.LastError = failure.Error
		delivery.LeaseToken = ""
		delivery.Quarantined = true
		return nil
	}
	return ErrLeaseLost
}

func (o *MemoryOutbox) ReactivateFailures(
	ctx context.Context,
	configFingerprints map[string]string,
	availableAt time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.deliveries {
		delivery := &o.deliveries[index]
		currentFingerprint, active := configFingerprints[delivery.SubscriptionID]
		if !active || delivery.LeaseToken != "" ||
			(!delivery.Quarantined && delivery.PermanentFails == 0) ||
			currentFingerprint == delivery.FailureConfigFingerprint {
			continue
		}
		delivery.AvailableAt = availableAt
		delivery.PermanentFails = 0
		delivery.FailureConfigFingerprint = ""
		delivery.Quarantined = false
	}
	return nil
}

func (o *MemoryOutbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.deliveries)
}

func (o *MemoryOutbox) containsDedupeKey(key string) bool {
	for index := range o.deliveries {
		if o.deliveries[index].DedupeKey == key {
			return true
		}
	}
	return false
}

func (o *MemoryOutbox) hasOlderDelivery(index int, subscriptionID string) bool {
	for previous := 0; previous < index; previous++ {
		if !o.deliveries[previous].Quarantined &&
			o.deliveries[previous].SubscriptionID == subscriptionID {
			return true
		}
	}
	return false
}

func cloneDelivery(delivery Delivery) Delivery {
	if delivery.RenderPayload == nil {
		return delivery
	}
	payload := *delivery.RenderPayload
	payload.Changes = append([]model.StatusChange(nil), delivery.RenderPayload.Changes...)
	delivery.RenderPayload = &payload
	return delivery
}
