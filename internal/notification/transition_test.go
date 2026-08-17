package notification

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func TestWorkerIngestsTransitionIntoOutbox(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	batch := transitionBatch("batch-1", changedAt, "")
	source := newTransitionSource(true, transitionTestRecord{
		batch: batch, availableAt: changedAt.Add(-time.Hour),
	})
	outbox := newRecordingOutbox()
	source.setOutbox(outbox)
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour, Logger: discardLogger(),
	}, Config{BotToken: "token", Subscriptions: []Subscription{
		{ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"a"}, Template: `{{.TotalChanges}}|{{range .Changes}}{{.Model}}{{end}}`},
		{ID: "other", Enabled: true, ChatID: "other", ServiceIDs: []string{"b"}, Template: `unexpected`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForTransition(t, source.processed, "batch-1")
	deliveries := outbox.snapshot()
	if len(deliveries) != 1 {
		t.Fatalf("transition 应生成一条订阅投递，实际 %d 条: %+v", len(deliveries), deliveries)
	}
	delivery := deliveries[0]
	if delivery.SubscriptionID != "ops" || delivery.Text != "1|alpha" {
		t.Fatalf("transition 渲染结果错误: %+v", delivery)
	}
	if delivery.DedupeKey != transitionDeliveryDedupeKey("batch-1", "ops", 0, batch.Changes) {
		t.Fatalf("transition 未使用持久化批次键去重: %q", delivery.DedupeKey)
	}
	if !delivery.CreatedAt.Equal(changedAt) {
		t.Fatalf("投递时间未继承 transition 批次时间: %v", delivery.CreatedAt)
	}
}

func TestTransitionUsesCurrentCompleteConfig(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	source := newTransitionSource(false, transitionTestRecord{
		batch:       transitionBatch("batch-current", changedAt, "https://status.example.com/?view=full&model=a"),
		availableAt: changedAt.Add(-time.Hour),
	})
	outbox := newRecordingOutbox()
	source.setOutbox(outbox)
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour, Logger: discardLogger(),
	}, Config{BotToken: "old-token", Subscriptions: []Subscription{{
		ID: "old", Enabled: true, ChatID: "old-chat", ServiceIDs: []string{"a"}, Template: `old`,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	waitForSignal(t, source.firstAttempt, "worker 首轮 transition 检查")

	if err := n.UpdateConfig(Config{BotToken: "new-token", Subscriptions: []Subscription{{
		ID: "current", Enabled: true, ChatID: "new-chat", Language: LanguageEnglish,
		ServiceIDs: []string{"a"}, Template: `current|{{range .Changes}}{{.Model}}{{end}}`,
	}}}); err != nil {
		t.Fatal(err)
	}
	source.setActive(true)
	n.signalWorker()
	waitForTransition(t, source.processed, "batch-current")

	deliveries := outbox.snapshot()
	if len(deliveries) != 1 || deliveries[0].SubscriptionID != "current" {
		t.Fatalf("transition 未使用当前订阅快照: %+v", deliveries)
	}
	want := "current|alpha"
	if deliveries[0].Text != want || deliveries[0].StatusPageURL != "https://status.example.com/?view=full&model=a" {
		t.Fatalf("transition 未使用当前模板、语言或页面地址: %q", deliveries[0].Text)
	}
}

func TestTransitionCommitRetriesWithoutDuplicateOutbox(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Minute)
	source := newTransitionSource(true, transitionTestRecord{
		batch: transitionBatch("batch-replay", changedAt, ""), availableAt: changedAt,
	})
	source.commitFailures = 1
	outbox := newRecordingOutbox()
	source.setOutbox(outbox)
	n, err := New(Options{
		Repository: source, PollInterval: time.Millisecond,
		DeliveryLease: 5 * time.Millisecond, PersistenceRetryDelays: []time.Duration{time.Millisecond},
		Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForTransition(t, source.processed, "batch-replay")
	claims, commits := source.counts()
	if claims != 1 || commits != 2 {
		t.Fatalf("原子提交失败后未在同一租约内重试: claims=%d commits=%d", claims, commits)
	}
	if got := outbox.snapshot(); len(got) != 1 {
		t.Fatalf("原子提交重试产生了重复 outbox 投递: %+v", got)
	}
	attempts := source.commitDedupeKeys()
	if len(attempts) != 2 || len(attempts[0]) != 1 || attempts[0][0] == "" ||
		attempts[0][0] != attempts[1][0] {
		t.Fatalf("提交重试必须使用相同 dedupe key: %q", attempts)
	}
}

func TestTransitionWithoutCurrentTargetIsAcknowledged(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Minute)
	source := newTransitionSource(true, transitionTestRecord{
		batch: transitionBatch("batch-no-target", changedAt, ""), availableAt: changedAt,
	})
	outbox := newRecordingOutbox()
	source.setOutbox(outbox)
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour, Logger: discardLogger(),
	}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForTransition(t, source.processed, "batch-no-target")
	if deliveries := outbox.snapshot(); len(deliveries) != 0 {
		t.Fatalf("当前配置没有目标时不应创建 outbox 投递: %+v", deliveries)
	}
}

func TestCloseIngestsDueTransitionsAndLeavesFutureTransitions(t *testing.T) {
	t.Parallel()
	now := time.Now()
	outbox := NewMemoryOutbox()
	source := newTransitionSource(false,
		transitionTestRecord{batch: transitionBatch("due", now, ""), availableAt: now.Add(-time.Minute)},
		transitionTestRecord{batch: transitionBatch("future", now.Add(time.Hour), ""), availableAt: now.Add(time.Hour)},
	)
	source.setOutbox(outbox)
	var requestsMu sync.Mutex
	requests := 0
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		requestsMu.Lock()
		requests++
		requestsMu.Unlock()
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	n, err := New(Options{
		Repository: source, Client: client,
		PollInterval: time.Hour, RetryDelays: []time.Duration{}, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, source.firstAttempt, "worker 首轮 transition 检查")
	source.setActive(true)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.Close(ctx); err != nil {
		t.Fatal(err)
	}
	processed := source.processedKeys()
	if len(processed) != 1 || processed[0] != "due" {
		t.Fatalf("关闭阶段应只确认已到期 transition: %q", processed)
	}
	requestsMu.Lock()
	requestCount := requests
	requestsMu.Unlock()
	if requestCount != 1 {
		t.Fatalf("关闭阶段应发送已到期 transition 的投递，实际请求 %d 次", requestCount)
	}
	if err := n.SendTest(context.Background(), "ops", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 SendTest 应被生命周期门禁拒绝: %v", err)
	}
	if err := n.UpdateConfig(validConfig("token", "chat")); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 UpdateConfig 应被生命周期门禁拒绝: %v", err)
	}
}

func TestTransitionRenewsLeaseWhileCommitIsBlocked(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Minute)
	outbox := newRecordingOutbox()
	source := newTransitionSource(true, transitionTestRecord{
		batch: transitionBatch("batch-renew", changedAt, ""), availableAt: changedAt,
	})
	source.setOutbox(outbox)
	releaseCommit := make(chan struct{})
	source.commitBlock = releaseCommit
	source.commitStarted = make(chan struct{})
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour,
		DeliveryLease: 30 * time.Millisecond, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForSignal(t, source.commitStarted, "transition 原子提交开始")
	waitForTransition(t, source.renewed, "batch-renew")
	if renewals := source.renewalCount(); renewals < 1 {
		t.Fatalf("阻塞提交期间续租次数 = %d，期望至少 1", renewals)
	}
	close(releaseCommit)
	waitForTransition(t, source.processed, "batch-renew")
}

func TestTransitionCommitRetryKeepsStableMessageShards(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	serviceIDs := make([]string, 0, 7)
	changes := make([]model.StatusChange, 0, 7)
	for index := 0; index < 7; index++ {
		serviceID := fmt.Sprintf("service-%02d", index)
		serviceIDs = append(serviceIDs, serviceID)
		changes = append(changes, model.StatusChange{
			ServiceID: serviceID,
			Model:     fmt.Sprintf("%02d-%s", index, strings.Repeat("界", 1190)),
			Status:    "down", PreviousStatus: "up", LastTS: changedAt.Unix(),
		})
	}
	outbox := newRecordingOutbox()
	source := newTransitionSource(true, transitionTestRecord{
		batch: model.TransitionBatch{
			Key: "batch-shards", ChangedAt: changedAt,
			StatusPageURL: "https://status.example.com/?view=full",
			Changes:       changes,
		},
		availableAt: changedAt.Add(-time.Minute),
	})
	source.setOutbox(outbox)
	source.commitFailures = 2
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour,
		PersistenceRetryDelays: []time.Duration{time.Millisecond}, Logger: discardLogger(),
	}, Config{BotToken: "token", Subscriptions: []Subscription{{
		ID: "ops", Enabled: true, ChatID: "chat", Language: LanguageEnglish,
		ServiceIDs: serviceIDs, Template: "{{range .Changes}}{{.Model}}\n{{end}}",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForTransition(t, source.processed, "batch-shards")
	attempts := source.commitDedupeKeys()
	if len(attempts) != 3 {
		t.Fatalf("原子提交次数 = %d，期望 3", len(attempts))
	}
	if len(attempts[0]) < 2 {
		t.Fatalf("长消息没有拆分: keys=%q", attempts[0])
	}
	for index := 1; index < len(attempts); index++ {
		if !reflect.DeepEqual(attempts[0], attempts[index]) {
			t.Fatalf("第 %d 次提交的分片 key 不稳定:\n首次=%q\n本次=%q", index+1, attempts[0], attempts[index])
		}
	}
	seenKeys := make(map[string]struct{}, len(attempts[0]))
	for _, key := range attempts[0] {
		if key == "" {
			t.Fatal("分片 dedupe key 不能为空")
		}
		if _, exists := seenKeys[key]; exists {
			t.Fatalf("不同分片复用了 dedupe key: %q", key)
		}
		seenKeys[key] = struct{}{}
	}

	deliveries := outbox.snapshot()
	if len(deliveries) != len(attempts[0]) {
		t.Fatalf("持久化分片数 = %d，期望 %d", len(deliveries), len(attempts[0]))
	}
	var rendered strings.Builder
	for _, delivery := range deliveries {
		if runes := utf8.RuneCountInString(delivery.Text); runes > TelegramMessageLimit {
			t.Fatalf("分片字符数 = %d，超过 Telegram 限制", runes)
		}
		if delivery.StatusPageURL != "https://status.example.com/?view=full" || strings.Contains(delivery.Text, "Open status page") {
			t.Fatalf("分片未把状态页保存为按钮数据: %+v", delivery)
		}
		rendered.WriteString(delivery.Text)
	}
	allText := rendered.String()
	for _, change := range changes {
		if count := strings.Count(allText, change.Model); count != 1 {
			t.Fatalf("模型 %q 在全部分片中出现 %d 次，期望 1", change.ServiceID, count)
		}
	}
}

func TestWorkerAlternatesTransitionsAndOutboxDeliveries(t *testing.T) {
	t.Parallel()
	now := time.Now()
	events := &transitionEventLog{}
	outbox := &trackingOutbox{MemoryOutbox: NewMemoryOutbox(), events: events}
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "existing", SubscriptionID: "ops", Text: "existing", AvailableAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	source := newTransitionSource(true,
		transitionTestRecord{batch: transitionBatch("batch-fair-1", now, ""), availableAt: now.Add(-time.Minute)},
		transitionTestRecord{batch: transitionBatch("batch-fair-2", now, ""), availableAt: now.Add(-time.Minute)},
		transitionTestRecord{batch: transitionBatch("batch-fair-3", now, ""), availableAt: now.Add(-time.Minute)},
	)
	source.setOutbox(outbox)
	source.onCommit = func(key string) { events.add("transition:" + key) }
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	n, err := New(Options{
		Repository: source, Client: client,
		PollInterval: time.Hour, RetryDelays: []time.Duration{}, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	waitForTransition(t, source.processed, "batch-fair-1")
	waitForTransition(t, source.processed, "batch-fair-2")
	waitForTransition(t, source.processed, "batch-fair-3")
	got := events.snapshot()
	firstTransition := indexOfEvent(got, "transition:batch-fair-1")
	existingDelivery := indexOfEvent(got, "delivery:existing")
	secondTransition := indexOfEvent(got, "transition:batch-fair-2")
	if firstTransition < 0 || existingDelivery < 0 || secondTransition < 0 ||
		!(firstTransition < existingDelivery && existingDelivery < secondTransition) {
		t.Fatalf("worker 未交替处理 transition 与 outbox: %q", got)
	}
}

func TestPersistentTransitionFailureDoesNotStarveExistingOutbox(t *testing.T) {
	t.Parallel()
	now := time.Now()
	outbox := NewMemoryOutbox()
	if err := outbox.Enqueue(context.Background(), []Delivery{{
		DedupeKey: "existing-after-failure", SubscriptionID: "ops",
		Text: "existing", AvailableAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	source := newTransitionSource(true, transitionTestRecord{
		batch: transitionBatch("batch-persistent-failure", now, ""), availableAt: now.Add(-time.Minute),
	})
	source.commitFailures = 100
	source.setOutbox(outbox)
	delivered := make(chan struct{})
	var deliveredOnce sync.Once
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		deliveredOnce.Do(func() { close(delivered) })
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	n, err := New(Options{
		Repository: source, Client: client, PollInterval: time.Hour,
		DeliveryLease: time.Hour, PersistenceRetryDelays: []time.Duration{time.Millisecond},
		RetryDelays: []time.Duration{}, Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("持续失败的 transition 提交饿死了已有 outbox 投递")
	}
	claims, commits := source.counts()
	if claims != 1 || commits != persistenceAttemptLimit {
		t.Fatalf("单轮提交重试未受限: claims=%d commits=%d", claims, commits)
	}
}

func TestTransitionRenderFailureIsNotAcknowledged(t *testing.T) {
	t.Parallel()
	changedAt := time.Now().Add(-time.Minute)
	source := newTransitionSource(true, transitionTestRecord{
		batch: transitionBatch("batch-render-error", changedAt, ""), availableAt: changedAt,
	})
	outbox := newRecordingOutbox()
	source.setOutbox(outbox)
	config := validConfig("token", "chat")
	config.Subscriptions[0].Template = strings.Repeat("x", TelegramMessageLimit+1)
	n, err := New(Options{
		Repository: source, PollInterval: time.Hour,
		DeliveryLease: time.Hour, Logger: discardLogger(),
	}, config)
	if err != nil {
		t.Fatal(err)
	}
	waitForTransition(t, source.claimed, "batch-render-error")
	closeNotifier(t, n)

	_, commits := source.counts()
	if commits != 0 || len(source.processedKeys()) != 0 {
		t.Fatalf("渲染失败的 transition 不应被提交: commits=%d processed=%q", commits, source.processedKeys())
	}
	if deliveries := outbox.snapshot(); len(deliveries) != 0 {
		t.Fatalf("渲染失败不应留下不完整投递: %+v", deliveries)
	}
}

type transitionTestRecord struct {
	batch       model.TransitionBatch
	availableAt time.Time
	leaseUntil  time.Time
	leaseToken  string
	processed   bool
}

type transitionTestSource struct {
	transitionTestOutbox
	mu                sync.Mutex
	records           []transitionTestRecord
	active            bool
	nextLease         int
	claimCount        int
	commitCount       int
	renewCount        int
	commitFailures    int
	renewFailures     int
	commitAttempts    [][]string
	commitBlock       <-chan struct{}
	commitStarted     chan struct{}
	commitStartedOnce sync.Once
	onCommit          func(string)
	firstOnce         sync.Once
	firstAttempt      chan struct{}
	claimed           chan string
	renewed           chan string
	processed         chan string
}

type transitionTestOutbox interface {
	Repository
	Enqueue(context.Context, []Delivery) error
}

func newTransitionSource(active bool, records ...transitionTestRecord) *transitionTestSource {
	return &transitionTestSource{
		records: records, active: active, firstAttempt: make(chan struct{}),
		claimed: make(chan string, len(records)*2), renewed: make(chan string, len(records)*8+1),
		processed: make(chan string, len(records)*2),
	}
}

func (s *transitionTestSource) setOutbox(outbox transitionTestOutbox) {
	s.mu.Lock()
	s.transitionTestOutbox = outbox
	s.mu.Unlock()
}

func (s *transitionTestSource) ClaimTransitions(
	ctx context.Context, now, leaseUntil time.Time, _ int,
) (*model.TransitionBatch, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstOnce.Do(func() { close(s.firstAttempt) })
	if !s.active {
		return nil, "", nil
	}
	for index := range s.records {
		record := &s.records[index]
		if record.processed || record.availableAt.After(now) || record.leaseUntil.After(now) {
			continue
		}
		s.nextLease++
		s.claimCount++
		record.leaseUntil = leaseUntil
		record.leaseToken = "lease-" + time.Unix(int64(s.nextLease), 0).Format("150405")
		batch := record.batch
		batch.Changes = append([]model.StatusChange(nil), record.batch.Changes...)
		s.claimed <- batch.Key
		return &batch, record.leaseToken, nil
	}
	return nil, "", nil
}

func (s *transitionTestSource) RenewTransitions(
	ctx context.Context,
	groupKey string,
	leaseToken string,
	leaseUntil time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		record := &s.records[index]
		if record.batch.Key != groupKey || record.leaseToken != leaseToken || record.processed {
			continue
		}
		s.renewCount++
		if s.renewFailures > 0 {
			s.renewFailures--
			return errors.New("temporary transition renewal failure")
		}
		record.leaseUntil = leaseUntil
		select {
		case s.renewed <- groupKey:
		default:
		}
		return nil
	}
	return ErrLeaseLost
}

func (s *transitionTestSource) CommitTransitions(
	ctx context.Context,
	groupKey string,
	leaseToken string,
	deliveries []Delivery,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		keys = append(keys, delivery.DedupeKey)
	}
	s.mu.Lock()
	s.commitCount++
	s.commitAttempts = append(s.commitAttempts, keys)
	block := s.commitBlock
	started := s.commitStarted
	if s.commitFailures > 0 {
		s.commitFailures--
		s.mu.Unlock()
		return errors.New("temporary transition commit failure")
	}
	s.mu.Unlock()
	if started != nil {
		s.commitStartedOnce.Do(func() { close(started) })
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// fake 在同一临界区完成入箱和 ledger 确认，复现生产 adapter 的原子语义。
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		record := &s.records[index]
		if record.batch.Key != groupKey || record.leaseToken != leaseToken || record.processed {
			continue
		}
		if s.transitionTestOutbox == nil {
			return errors.New("transition test source missing outbox")
		}
		if err := s.transitionTestOutbox.Enqueue(ctx, deliveries); err != nil {
			return err
		}
		record.processed = true
		if s.onCommit != nil {
			s.onCommit(groupKey)
		}
		s.processed <- groupKey
		return nil
	}
	return ErrLeaseLost
}

func (s *transitionTestSource) setActive(active bool) {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
}

func (s *transitionTestSource) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimCount, s.commitCount
}

func (s *transitionTestSource) renewalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewCount
}

func (s *transitionTestSource) commitDedupeKeys() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := make([][]string, len(s.commitAttempts))
	for index := range s.commitAttempts {
		attempts[index] = append([]string(nil), s.commitAttempts[index]...)
	}
	return attempts
}

func (s *transitionTestSource) processedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.records))
	for index := range s.records {
		if s.records[index].processed {
			keys = append(keys, s.records[index].batch.Key)
		}
	}
	return keys
}

type recordingOutbox struct {
	*MemoryOutbox
}

func newRecordingOutbox() *recordingOutbox {
	return &recordingOutbox{MemoryOutbox: NewMemoryOutbox()}
}

func (o *recordingOutbox) Enqueue(ctx context.Context, deliveries []Delivery) error {
	return o.MemoryOutbox.Enqueue(ctx, deliveries)
}

func (o *recordingOutbox) Claim(context.Context, time.Time, time.Time) (*Delivery, error) {
	return nil, nil
}

func (o *recordingOutbox) snapshot() []Delivery {
	o.MemoryOutbox.mu.Lock()
	defer o.MemoryOutbox.mu.Unlock()
	return append([]Delivery(nil), o.MemoryOutbox.deliveries...)
}

type transitionEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *transitionEventLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *transitionEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type trackingOutbox struct {
	*MemoryOutbox
	events *transitionEventLog
}

func (o *trackingOutbox) Claim(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
) (*Delivery, error) {
	delivery, err := o.MemoryOutbox.Claim(ctx, now, leaseUntil)
	if delivery != nil {
		o.events.add("delivery:" + delivery.Text)
	}
	return delivery, err
}

func indexOfEvent(events []string, wanted string) int {
	for index, event := range events {
		if event == wanted {
			return index
		}
	}
	return -1
}

func transitionBatch(key string, changedAt time.Time, statusPageURL string) model.TransitionBatch {
	return model.TransitionBatch{
		Key: key, ChangedAt: changedAt, StatusPageURL: statusPageURL,
		Changes: []model.StatusChange{{
			ServiceID: "a", Model: "alpha", Status: "down", PreviousStatus: "up", LastTS: changedAt.Unix(),
		}},
	}
}

func waitForTransition(t *testing.T, channel <-chan string, key string) {
	t.Helper()
	select {
	case got := <-channel:
		if got != key {
			t.Fatalf("收到 transition %q，期望 %q", got, key)
		}
	case <-time.After(time.Second):
		t.Fatalf("等待 transition %q 超时", key)
	}
}

func waitForSignal(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("等待%s超时", description)
	}
}
