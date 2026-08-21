package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloseCancelsAndWaitsForSendTest(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var once sync.Once
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		once.Do(func() { close(started) })
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	n, err := New(Options{
		Client: client, Repository: NewMemoryOutbox(), RetryDelays: []time.Duration{},
		Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- n.SendTest(context.Background(), "ops", "")
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := n.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("阻塞测试发送时关闭应报告 deadline: %v", err)
	}
	if err := <-sendDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("测试发送没有被关闭取消: %v", err)
	}
	if err := n.SendTest(context.Background(), "ops", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 SendTest = %v", err)
	}
}

func TestNewRequiresRepositoryAndPositiveRetryDelays(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{Logger: discardLogger()}, validConfig("token", "chat")); err == nil {
		t.Fatal("缺少 repository 时应拒绝创建通知器")
	}
	if _, err := New(Options{
		Repository: NewMemoryOutbox(), RedeliveryDelays: []time.Duration{0}, Logger: discardLogger(),
	}, validConfig("token", "chat")); err == nil {
		t.Fatal("零退避会造成忙循环，应被拒绝")
	}
	if _, err := New(Options{
		Repository: NewMemoryOutbox(), PersistenceRetryDelays: []time.Duration{0}, Logger: discardLogger(),
	}, validConfig("token", "chat")); err == nil {
		t.Fatal("持久化零退避会造成忙循环，应被拒绝")
	}
}

type requestRecord struct {
	path      string
	chatID    string
	text      string
	parseMode string
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func validConfig(token, chatID string) Config {
	return Config{BotToken: token, Subscriptions: []Subscription{{ID: "ops", Enabled: true, ChatID: chatID, ServiceUIDs: []string{"a"}, Template: `{{.TotalChanges}}`}}}
}

func newTestNotifier(t *testing.T, apiBaseURL string, config Config) *Notifier {
	t.Helper()
	n, err := New(Options{APIBaseURL: apiBaseURL, Repository: NewMemoryOutbox(), RetryDelays: []time.Duration{}, Logger: discardLogger()}, config)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func closeNotifier(t *testing.T, notifier *Notifier) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := notifier.Close(ctx); err != nil {
		t.Errorf("关闭通知器: %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
