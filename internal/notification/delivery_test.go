package notification

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestBuildDeliveriesAggregatesPerSubscription(t *testing.T) {
	t.Parallel()
	config, err := compileConfig(Config{
		BotToken: "token",
		Subscriptions: []Subscription{
			{ID: "ops", Enabled: true, ChatID: "-100", ServiceIDs: []string{"a", "b"}, Template: `{{.TotalChanges}}|{{range .DownModels}}D:{{.Model}};{{end}}{{range .RecoveredModels}}R:{{.Model}};{{end}}`},
			{ID: "other", Enabled: true, ChatID: "200", ServiceIDs: []string{"c"}, Template: `{{.TotalChanges}}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := buildDeliveries(config, deliveryBatch{
		StatusPageURL: "https://status.example.com/?model=a&view=full",
		Changes: []model.StatusChange{
			{ServiceID: "a", Model: "alpha-old", Status: "down", PreviousStatus: "up"},
			{ServiceID: "a", Model: "alpha-middle", OK: true, Status: "up", PreviousStatus: "down"},
			{ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up"},
			{ServiceID: "b", Model: "beta", OK: true, Status: "up", PreviousStatus: "down"},
		}}, func(subscriptionID string, _ time.Time, shardIndex int, _ []model.StatusChange) string {
		return fmt.Sprintf("%s-%d", subscriptionID, shardIndex)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("投递数 = %d，期望 1: %+v", len(deliveries), deliveries)
	}
	delivery := deliveries[0]
	wantText := "2|D:alpha;R:beta;\n\n<a href=\"https://status.example.com/?model=a&amp;view=full\">查看探针页</a>"
	if delivery.SubscriptionID != "ops" || delivery.DedupeKey != "ops-0" || delivery.Text != wantText {
		t.Fatalf("聚合投递错误: %+v", delivery)
	}
	if delivery.RenderPayload == nil || len(delivery.RenderPayload.Changes) != 2 {
		t.Fatalf("投递缺少重渲染数据: %+v", delivery.RenderPayload)
	}
}

type pausedOutbox struct {
	*MemoryOutbox
	active atomic.Bool
}

func (o *pausedOutbox) Claim(ctx context.Context, now, leaseUntil time.Time) (*Delivery, error) {
	if !o.active.Load() {
		return nil, ctx.Err()
	}
	return o.MemoryOutbox.Claim(ctx, now, leaseUntil)
}

func TestPendingDeliveryUsesOneCurrentRoutingSnapshot(t *testing.T) {
	t.Parallel()
	outbox := &pausedOutbox{MemoryOutbox: NewMemoryOutbox()}
	requests := make(chan requestRecord, 1)
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		requests <- requestRecord{path: request.URL.Path, chatID: request.Form.Get("chat_id")}
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "pending", SubscriptionID: "ops", Text: "pending", AvailableAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{
		Client: client, Repository: outbox, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{}, Logger: discardLogger(),
	}, validConfig("old-token", "old-chat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.UpdateConfig(validConfig("new-token", "new-chat")); err != nil {
		t.Fatal(err)
	}
	outbox.active.Store(true)
	n.signalWorker()
	select {
	case request := <-requests:
		if request.path != "/botnew-token/sendMessage" || request.chatID != "new-chat" {
			t.Fatalf("积压消息混用了配置版本: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("积压消息没有发送")
	}
	closeNotifier(t, n)
}

func TestResolveDeliveryReappliesCurrentServiceFilter(t *testing.T) {
	t.Parallel()
	config := validConfig("token", "new-chat")
	config.Subscriptions[0].ServiceIDs = []string{"b"}
	config.Subscriptions[0].Template = `{{range .Changes}}{{.Model}} {{end}}`
	notifier, err := New(Options{Repository: NewMemoryOutbox(), Logger: discardLogger()}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, notifier)

	delivery := &Delivery{SubscriptionID: "ops", Text: "stale", RenderPayload: &RenderPayload{
		ChangedAt: time.Now(),
		Changes: []model.StatusChange{
			{ServiceID: "a", Model: "alpha", PreviousStatus: "up", Status: "down"},
			{ServiceID: "b", Model: "beta", PreviousStatus: "up", Status: "down"},
		},
	}}
	job, active, err := notifier.resolveDelivery(delivery)
	if err != nil || !active {
		t.Fatalf("使用当前订阅重渲染: active=%v err=%v", active, err)
	}
	if strings.Contains(job.text, "alpha") || !strings.Contains(job.text, "beta") || job.chatID != "new-chat" {
		t.Fatalf("重渲染没有应用当前服务筛选与路由: %+v", job)
	}

	delivery.RenderPayload.Changes = delivery.RenderPayload.Changes[:1]
	if _, active, err := notifier.resolveDelivery(delivery); err != nil || active {
		t.Fatalf("当前订阅已不包含任何 payload 服务时应取消投递: active=%v err=%v", active, err)
	}
}

func TestConfigUpdatePreservesRetryAfterFromInFlightDelivery(t *testing.T) {
	t.Parallel()
	outbox := &pausedOutbox{MemoryOutbox: NewMemoryOutbox()}
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "retry-after", SubscriptionID: "ops", Text: "message", AvailableAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	notifier, err := New(Options{
		Repository: outbox, RedeliveryDelays: []time.Duration{time.Millisecond}, Logger: discardLogger(),
	}, validConfig("old-token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, notifier)
	claimed, err := outbox.MemoryOutbox.Claim(context.Background(), time.Now(), time.Now().Add(time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("领取测试投递: delivery=%+v err=%v", claimed, err)
	}
	job, active, err := notifier.resolveDelivery(claimed)
	if err != nil || !active {
		t.Fatalf("解析测试投递: active=%v err=%v", active, err)
	}
	if err := notifier.UpdateConfig(validConfig("new-token", "chat")); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := notifier.handleDeliveryFailure(claimed, job, &deliveryError{
		err: errors.New("rate limited"), retryable: true, retryAfter: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	stored := outbox.deliveries[0]
	outbox.mu.Unlock()
	if stored.AvailableAt.Before(before.Add(59*time.Minute)) || stored.PermanentFails != 0 {
		t.Fatalf("配置更新绕过了 Retry-After 或保留了永久失败计数: %+v", stored)
	}
}

func TestUnrelatedSubscriptionUpdateDoesNotResetPermanentFailure(t *testing.T) {
	t.Parallel()
	config := validConfig("token", "ops-chat")
	config.Subscriptions = append(config.Subscriptions, Subscription{
		ID: "other", Enabled: true, ChatID: "other-chat", ServiceIDs: []string{"b"}, Template: "other",
	})
	compiled, err := compileConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	outbox := &pausedOutbox{MemoryOutbox: NewMemoryOutbox()}
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "targeted-config", SubscriptionID: "ops", Text: "message",
		AvailableAt: time.Now(), PermanentFails: permanentFailureAttemptLimit - 1,
		FailureConfigFingerprint: compiled.subscriptions[0].fingerprint,
	}}); err != nil {
		t.Fatal(err)
	}
	notifier, err := New(Options{Repository: outbox, Logger: discardLogger()}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, notifier)
	claimed, err := outbox.MemoryOutbox.Claim(context.Background(), time.Now(), time.Now().Add(time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("领取测试投递: delivery=%+v err=%v", claimed, err)
	}
	job, active, err := notifier.resolveDelivery(claimed)
	if err != nil || !active {
		t.Fatalf("解析测试投递: active=%v err=%v", active, err)
	}
	updated := config
	updated.Subscriptions = append([]Subscription(nil), config.Subscriptions...)
	updated.Subscriptions[1].ChatID = "new-other-chat"
	if err := notifier.UpdateConfig(updated); err != nil {
		t.Fatal(err)
	}
	if err := notifier.handleDeliveryFailure(claimed, job, &deliveryError{
		err: errors.New("bad request"), retryable: false,
	}); err != nil {
		t.Fatal(err)
	}
	outbox.mu.Lock()
	stored := outbox.deliveries[0]
	outbox.mu.Unlock()
	if !stored.Quarantined || stored.PermanentFails != permanentFailureAttemptLimit {
		t.Fatalf("其他订阅配置变化错误重置了当前订阅失败: %+v", stored)
	}
}

func TestPermanentTelegramFailureIsQuarantinedWithoutBlockingLaterDelivery(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	var badAttempts atomic.Int64
	var goodAttempts atomic.Int64
	goodDelivered := make(chan struct{})
	var deliveredOnce sync.Once
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		switch request.Form.Get("text") {
		case "bad":
			attempt := badAttempts.Add(1)
			if attempt <= 3 {
				return response(http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"temporary"}`), nil
			}
			return response(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"bad request"}`), nil
		case "good":
			goodAttempts.Add(1)
			deliveredOnce.Do(func() { close(goodDelivered) })
			return response(http.StatusOK, `{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected message %q", request.Form.Get("text"))
		}
	})
	now := time.Now()
	if err := outbox.Enqueue(context.Background(), []Delivery{
		{DedupeKey: "bad", SubscriptionID: "ops", Text: "bad", AvailableAt: now},
		{DedupeKey: "good", SubscriptionID: "ops", Text: "good", AvailableAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{
		Client: client, Repository: outbox, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{},
		RedeliveryDelays: []time.Duration{
			time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond,
		},
		Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	select {
	case <-goodDelivered:
	case <-time.After(time.Second):
		t.Fatal("永久失败消息隔离后，同订阅后续消息仍未发送")
	}
	if got := badAttempts.Load(); got != int64(3+permanentFailureAttemptLimit) {
		t.Fatalf("混合临时与永久错误发送次数 = %d，期望 %d", got, 3+permanentFailureAttemptLimit)
	}
	if got := goodAttempts.Load(); got != 1 {
		t.Fatalf("后续正常消息发送次数 = %d，期望 1", got)
	}
	waitForOutboxLength(t, outbox, 1)
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.deliveries) != 1 || !outbox.deliveries[0].Quarantined ||
		outbox.deliveries[0].PermanentFails != permanentFailureAttemptLimit {
		t.Fatalf("永久失败消息未保留在隔离状态: %+v", outbox.deliveries)
	}
}

func TestPermanentTelegramFailureCanRecoverAfterConfigUpdate(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	var oldAttempts atomic.Int64
	var newAttempts atomic.Int64
	delivered := make(chan struct{})
	newTexts := make(chan string, 1)
	var deliveredOnce sync.Once
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/botold-token/sendMessage":
			oldAttempts.Add(1)
			return response(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"invalid token"}`), nil
		case "/botnew-token/sendMessage":
			newAttempts.Add(1)
			if err := request.ParseForm(); err != nil {
				return nil, err
			}
			newTexts <- request.Form.Get("text")
			deliveredOnce.Do(func() { close(delivered) })
			return response(http.StatusOK, `{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram endpoint %q", request.URL.Path)
		}
	})
	changedAt := time.Unix(300, 0)
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "recover", SubscriptionID: "ops", Text: "stale",
		AvailableAt: time.Now(), RenderPayload: &RenderPayload{
			ChangedAt: changedAt,
			Changes: []model.StatusChange{{
				ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up",
			}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	oldConfig := validConfig("old-token", "chat")
	oldConfig.Subscriptions[0].Template = `old {{range .Changes}}{{.Model}}{{end}}`
	n, err := New(Options{
		Client: client, Repository: outbox, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{}, RedeliveryDelays: []time.Duration{time.Millisecond},
		Logger: discardLogger(),
	}, oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	waitForOutboxQuarantined(t, outbox)
	newConfig := validConfig("new-token", "chat")
	newConfig.Subscriptions[0].Template = `new {{range .Changes}}{{.Model}}{{end}}`
	if err := n.UpdateConfig(newConfig); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("配置修复后永久错误消息没有恢复发送")
	}
	if got := oldAttempts.Load(); got != permanentFailureAttemptLimit {
		t.Fatalf("旧配置发送次数 = %d，期望 %d", got, permanentFailureAttemptLimit)
	}
	if got := newAttempts.Load(); got != 1 {
		t.Fatalf("新配置发送次数 = %d，期望 1", got)
	}
	select {
	case text := <-newTexts:
		if text != "new alpha" {
			t.Fatalf("配置修复后没有用新模板重渲染: %q", text)
		}
	default:
		t.Fatal("缺少新配置发送内容")
	}
}

func TestConfigUpdateWinsAgainstInFlightPermanentFailure(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	oldConfig := validConfig("old-token", "chat")
	compiledOld, err := compileConfig(oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "in-flight-config", SubscriptionID: "ops",
		Text: "message", AvailableAt: time.Now(), PermanentFails: permanentFailureAttemptLimit - 1,
		FailureConfigFingerprint: compiledOld.subscriptions[0].fingerprint,
	}}); err != nil {
		t.Fatal(err)
	}
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	newDelivered := make(chan struct{})
	var oldOnce, newOnce sync.Once
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/botold-token/sendMessage":
			oldOnce.Do(func() { close(oldStarted) })
			<-releaseOld
			return response(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"old config"}`), nil
		case "/botnew-token/sendMessage":
			newOnce.Do(func() { close(newDelivered) })
			return response(http.StatusOK, `{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram endpoint %q", request.URL.Path)
		}
	})
	n, err := New(Options{
		Client: client, Repository: outbox, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{}, RedeliveryDelays: []time.Duration{time.Hour},
		Logger: discardLogger(),
	}, oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("旧配置发送未开始")
	}
	if err := n.UpdateConfig(validConfig("new-token", "chat")); err != nil {
		t.Fatal(err)
	}
	close(releaseOld)
	select {
	case <-newDelivered:
	case <-time.After(time.Second):
		t.Fatal("旧配置永久失败覆盖了已经生效的新配置")
	}
	waitForOutboxLength(t, outbox, 0)
}

func TestOutboxRetriesUnconfirmedDeliveryAfterRestart(t *testing.T) {
	t.Parallel()
	outbox := NewMemoryOutbox()
	started := make(chan struct{})
	parent, cancelParent := context.WithCancel(context.Background())
	firstClient := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "restart", SubscriptionID: "ops", Text: "message", AvailableAt: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := New(Options{
		Context: parent, Client: firstClient, Repository: outbox, RetryDelays: []time.Duration{},
		RedeliveryDelays: []time.Duration{time.Millisecond}, PollInterval: time.Millisecond,
		DeliveryLease: 5 * time.Millisecond, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancelParent()
	closeNotifier(t, first)
	if got := outbox.Len(); got != 1 {
		t.Fatalf("未确认投递应保留在 outbox，实际 %d 条", got)
	}

	delivered := make(chan struct{})
	secondClient := httpClientFunc(func(*http.Request) (*http.Response, error) {
		close(delivered)
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	second, err := New(Options{
		Client: secondClient, Repository: outbox, RetryDelays: []time.Duration{},
		RedeliveryDelays: []time.Duration{time.Millisecond}, PollInterval: time.Millisecond,
		DeliveryLease: 5 * time.Millisecond, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("重启后没有恢复未确认投递")
	}
	closeNotifier(t, second)
	if got := outbox.Len(); got != 0 {
		t.Fatalf("恢复发送后 outbox 仍有 %d 条", got)
	}
}

func waitForOutboxQuarantined(t *testing.T, outbox *MemoryOutbox) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		outbox.mu.Lock()
		matched := len(outbox.deliveries) > 0 && outbox.deliveries[0].Quarantined
		outbox.mu.Unlock()
		if matched {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待 outbox 投递进入隔离状态超时")
}

func waitForOutboxLength(t *testing.T, outbox *MemoryOutbox, length int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if outbox.Len() == length {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 outbox 长度变为 %d 超时，当前 %d", length, outbox.Len())
}
