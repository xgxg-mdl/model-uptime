// Package notification 负责将模型状态变化聚合为 Telegram 通知。
package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

const (
	defaultRequestTimeout   = 10 * time.Second
	defaultPollInterval     = time.Second
	defaultDeliveryLease    = time.Minute
	persistenceAttemptLimit = 3
)

var (
	ErrClosed                     = errors.New("Telegram notifier is closed")
	ErrSubscriptionNotFound       = errors.New("Telegram subscription not found")
	defaultPersistenceRetryDelays = []time.Duration{
		100 * time.Millisecond,
		500 * time.Millisecond,
		2 * time.Second,
		5 * time.Second,
	}
)

// Options 配置通知器的运行时依赖。
type Options struct {
	Context          context.Context
	Client           HTTPClient
	Logger           *slog.Logger
	Repository       Repository
	APIBaseURL       string
	RetryDelays      []time.Duration
	RedeliveryDelays []time.Duration
	// PersistenceRetryDelays 控制单轮持久化提交的重试间隔。
	PersistenceRetryDelays []time.Duration
	PollInterval           time.Duration
	DeliveryLease          time.Duration
}

// Notifier 将持久化状态变化渲染入 outbox，并持有可热更新的配置快照。
type Notifier struct {
	client                 HTTPClient
	logger                 *slog.Logger
	apiBaseURL             string
	retryDelays            []time.Duration
	redeliveryDelays       []time.Duration
	persistenceRetryDelays []time.Duration
	pollInterval           time.Duration
	deliveryLease          time.Duration
	repository             Repository
	ctx                    context.Context
	cancel                 context.CancelFunc
	wake                   chan struct{}
	stop                   chan struct{}

	configMu sync.RWMutex
	config   runtimeConfig

	lifecycleMu  sync.RWMutex
	closed       bool
	operations   sync.WaitGroup
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	wg           sync.WaitGroup
}

// New 校验初始配置并启动单个有序发送 worker。
func New(options Options, config Config) (*Notifier, error) {
	compiled, err := compileConfig(config)
	if err != nil {
		return nil, err
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: defaultRequestTimeout}
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if strings.TrimSpace(options.APIBaseURL) == "" {
		options.APIBaseURL = "https://api.telegram.org"
	}
	retryDelays := options.RetryDelays
	if retryDelays == nil {
		retryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	redeliveryDelays := options.RedeliveryDelays
	if redeliveryDelays == nil {
		redeliveryDelays = []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}
	}
	persistenceRetryDelays := options.PersistenceRetryDelays
	if len(persistenceRetryDelays) == 0 {
		persistenceRetryDelays = defaultPersistenceRetryDelays
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.DeliveryLease <= 0 {
		options.DeliveryLease = defaultDeliveryLease
	}
	if options.Repository == nil {
		return nil, errors.New("notification repository is required")
	}
	for _, delay := range redeliveryDelays {
		if delay <= 0 {
			return nil, errors.New("notification redelivery delays must be positive")
		}
	}
	for _, delay := range persistenceRetryDelays {
		if delay <= 0 {
			return nil, errors.New("notification persistence retry delays must be positive")
		}
	}
	parent := options.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if err := options.Repository.ReactivateFailures(
		ctx, compiled.activeFingerprints(), time.Now(),
	); err != nil {
		cancel()
		return nil, fmt.Errorf("恢复配置已变化的 Telegram 通知失败: %w", err)
	}
	n := &Notifier{
		client: options.Client, logger: options.Logger,
		apiBaseURL:             strings.TrimRight(options.APIBaseURL, "/"),
		retryDelays:            append([]time.Duration(nil), retryDelays...),
		redeliveryDelays:       append([]time.Duration(nil), redeliveryDelays...),
		persistenceRetryDelays: append([]time.Duration(nil), persistenceRetryDelays...),
		pollInterval:           options.PollInterval, deliveryLease: options.DeliveryLease,
		repository: options.Repository, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), stop: make(chan struct{}),
		config: compiled, shutdownDone: make(chan struct{}),
	}
	n.wg.Add(1)
	go n.run()
	return n, nil
}

// UpdateConfig 先完整编译新模板，再原子替换运行时配置。
func (n *Notifier) UpdateConfig(config Config) error {
	if !n.beginOperation() {
		return ErrClosed
	}
	defer n.operations.Done()
	compiled, err := compileConfig(config)
	if err != nil {
		return err
	}
	n.configMu.Lock()
	defer n.configMu.Unlock()
	if n.config.equivalent(compiled) {
		return nil
	}
	if err := n.repository.ReactivateFailures(
		n.ctx, compiled.activeFingerprints(), time.Now(),
	); err != nil {
		return fmt.Errorf("恢复配置已变化的 Telegram 通知失败: %w", err)
	}
	n.config = compiled
	n.signalWorker()
	return nil
}

// SendTest 同步发送一条包含异常和恢复示例的消息，便于管理 API 返回准确结果。
func (n *Notifier) SendTest(ctx context.Context, subscriptionID, statusPageURL string) error {
	if !n.beginOperation() {
		return ErrClosed
	}
	defer n.operations.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(ctx)
	stopNotifierCancel := context.AfterFunc(n.ctx, cancel)
	defer func() {
		stopNotifierCancel()
		cancel()
	}()

	config := n.configSnapshot()
	for _, subscription := range config.subscriptions {
		if subscription.ID != subscriptionID {
			continue
		}
		now := time.Now()
		downModel, recoveredModel, provider, probeError := "example-down", "example-recovered", "example", "probe timeout"
		if subscription.Language == DefaultLanguage {
			downModel, recoveredModel, provider, probeError = "示例异常模型", "示例恢复模型", "示例提供商", "探测超时"
		}
		templateContext := NewTemplateContext(now, []model.StatusChange{
			{ServiceID: "example-down", Model: downModel, Provider: provider, Protocol: "chat", Error: probeError, PreviousStatus: "up", Status: "down", LastTS: now.Unix(), TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
			{ServiceID: "example-recovered", Model: recoveredModel, Provider: provider, Protocol: "chat", OK: true, LatencyMS: 128, PreviousStatus: "down", Status: "up", LastTS: now.Unix(), OutageDurationSec: 474, TodayUpSec: 34740, TodayDownSec: 4200, TodayDownCount: 4, TodayUptimePct: 89.20},
		})
		text, err := executeTemplate(subscription.template, templateContext)
		if err != nil {
			return fmt.Errorf("render Telegram subscription %q: %w", subscriptionID, err)
		}
		text, err = appendStatusPageLink(text, statusPageURL, subscription.Language)
		if err != nil {
			return fmt.Errorf("render Telegram subscription %q: %w", subscriptionID, err)
		}
		return n.sendWithRetry(operationCtx, sendJob{
			botToken: config.botToken, chatID: subscription.ChatID,
			text: text, name: subscription.ID,
			configFingerprint: subscription.fingerprint,
		})
	}
	return fmt.Errorf("%w: %s", ErrSubscriptionNotFound, subscriptionID)
}

// Close 停止接收新通知，并处理当前已经到期的状态变化与消息。
// 超时或父上下文取消时，中断在途请求；未确认消息仍留在 outbox。
func (n *Notifier) Close(ctx context.Context) error {
	n.lifecycleMu.Lock()
	if !n.closed {
		n.closed = true
		n.shutdownOnce.Do(func() { go n.shutdown() })
	}
	done := n.shutdownDone
	n.lifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		n.cancel()
		<-done
		return ctx.Err()
	}
}

func (n *Notifier) shutdown() {
	n.operations.Wait()
	close(n.stop)
	n.wg.Wait()
	n.cancel()
	close(n.shutdownDone)
}

func (n *Notifier) configSnapshot() runtimeConfig {
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	return n.config
}

func (n *Notifier) beginOperation() bool {
	n.lifecycleMu.Lock()
	defer n.lifecycleMu.Unlock()
	if n.closed {
		return false
	}
	n.operations.Add(1)
	return true
}

func (n *Notifier) retryPersistentOperation(
	ctx context.Context,
	operation string,
	perform func(context.Context) error,
) error {
	for attempt := 0; attempt < persistenceAttemptLimit; attempt++ {
		err := perform(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrTransitionLeaseLost) {
			return fmt.Errorf("%s: %w", operation, err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("%s: %w", operation, contextErr)
		}
		if attempt == persistenceAttemptLimit-1 {
			return fmt.Errorf("%s: %w", operation, err)
		}

		delayIndex := attempt
		if delayIndex >= len(n.persistenceRetryDelays) {
			delayIndex = len(n.persistenceRetryDelays) - 1
		}
		delay := n.persistenceRetryDelays[delayIndex]
		n.logger.Warn(operation+"失败，将重试", "err", err, "retry_in", delay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", operation, ctx.Err())
		}
	}
	return errors.New("unreachable persistence retry state")
}

func (n *Notifier) signalWorker() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Notifier) run() {
	defer n.wg.Done()
	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()
	shuttingDown := false
	for {
		worked, err := n.processRound()
		if err != nil && !errors.Is(err, context.Canceled) {
			n.logger.Error("处理 Telegram 通知队列失败", "err", err)
		}
		if n.ctx.Err() != nil {
			return
		}
		if worked {
			continue
		}
		if shuttingDown {
			return
		}
		select {
		case <-n.ctx.Done():
			return
		case <-n.stop:
			shuttingDown = true
		case <-n.wake:
		case <-ticker.C:
		}
	}
}

func (n *Notifier) processRound() (bool, error) {
	// 每轮两种来源各处理一条，避免大量历史 transition 饿死已持久化投递。
	transitionWorked, transitionErr := n.ingestTransition()
	outboxWorked, outboxErr := n.drainOne()
	return transitionWorked || outboxWorked, errors.Join(transitionErr, outboxErr)
}
