package notification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendTestRetriesTransientFailureThreeTimes(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		status := http.StatusInternalServerError
		body := `{"ok":false,"description":"temporary"}`
		if attempt >= 4 {
			status = http.StatusOK
			body = `{"ok":true}`
		}
		return response(status, body), nil
	})
	n, err := New(Options{Client: client, Repository: NewMemoryOutbox(), RetryDelays: []time.Duration{0, 0, 0}, Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	if err := n.SendTest(context.Background(), "ops", ""); err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 5 {
		t.Fatalf("期望异常卡片重试 3 次后再发送恢复卡片，实际 %d 次", got)
	}
}

func TestSendTestDoesNotRetryPermanentFailure(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return response(http.StatusBadRequest, `{"ok":false,"description":"bad chat id"}`), nil
	})
	n, err := New(Options{Client: client, Repository: NewMemoryOutbox(), RetryDelays: []time.Duration{0, 0, 0}, Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	if err := n.SendTest(context.Background(), "ops", ""); err == nil {
		t.Fatal("期望 Telegram 参数错误")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("永久错误不应重试，实际发送 %d 次", got)
	}
}

func TestSendErrorRedactsBotToken(t *testing.T) {
	t.Parallel()
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("request failed for " + request.URL.String())
	})
	n, err := New(Options{Client: client, Repository: NewMemoryOutbox(), RetryDelays: []time.Duration{}, Logger: discardLogger()}, validConfig("super-secret", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	err = n.SendTest(context.Background(), "ops", "")
	if err == nil || strings.Contains(err.Error(), "super-secret") || !strings.Contains(err.Error(), "****") {
		t.Fatalf("Bot Token 应从错误中脱敏: %v", err)
	}
}

func TestTelegramRetryAfterIsClassified(t *testing.T) {
	t.Parallel()
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, `{"ok":false,"error_code":429,"description":"slow down","parameters":{"retry_after":2}}`), nil
	})
	n, err := New(Options{Client: client, Repository: NewMemoryOutbox(), Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	deliveryErr := n.send(context.Background(), sendJob{botToken: "token", chatID: "chat", text: "message"})
	if deliveryErr == nil || !deliveryErr.retryable || deliveryErr.retryAfter != 2*time.Second {
		t.Fatalf("429 分类错误: %+v", deliveryErr)
	}
}

func TestSendUsesInlineStatusPageButton(t *testing.T) {
	t.Parallel()
	var replyMarkup string
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		replyMarkup = request.Form.Get("reply_markup")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Header:     make(http.Header),
		}, nil
	})
	notifier, err := New(Options{Client: client, Repository: NewMemoryOutbox(), Logger: discardLogger()}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, notifier)
	if deliveryErr := notifier.send(context.Background(), sendJob{
		botToken: "token", chatID: "chat", text: "message",
		statusPageURL: "https://status.example.com/", language: DefaultLanguage,
	}); deliveryErr != nil {
		t.Fatal(deliveryErr)
	}
	if !strings.Contains(replyMarkup, `"text":"查看探针页"`) || !strings.Contains(replyMarkup, `"url":"https://status.example.com/"`) {
		t.Fatalf("探针页没有使用内联按钮: %q", replyMarkup)
	}
}

func TestMalformedSuccessResponseIsRetryable(t *testing.T) {
	t.Parallel()
	client := httpClientFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{not-json`), nil
	})
	n, err := New(Options{
		Client: client, Repository: NewMemoryOutbox(), Logger: discardLogger(),
	}, validConfig("token", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotifier(t, n)
	deliveryErr := n.send(context.Background(), sendJob{
		botToken: "token", chatID: "chat", text: "message",
	})
	if deliveryErr == nil || !deliveryErr.retryable {
		t.Fatalf("畸形 2xx 响应应视为投递结果未知并重试: %+v", deliveryErr)
	}
}

func TestUnconfirmedSuccessResponseUsesExplicitErrorCodeClassification(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		body      string
		retryable bool
	}{
		{name: "missing confirmation", body: `{}`, retryable: true},
		{name: "embedded server error", body: `{"ok":false,"error_code":500}`, retryable: true},
		{name: "embedded client error", body: `{"ok":false,"error_code":400}`, retryable: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := httpClientFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, testCase.body), nil
			})
			notifier, err := New(Options{
				Client: client, Repository: NewMemoryOutbox(), Logger: discardLogger(),
			}, validConfig("token", "chat"))
			if err != nil {
				t.Fatal(err)
			}
			defer closeNotifier(t, notifier)
			deliveryErr := notifier.send(context.Background(), sendJob{
				botToken: "token", chatID: "chat", text: "message",
			})
			if deliveryErr == nil || deliveryErr.retryable != testCase.retryable {
				t.Fatalf("响应分类错误: error=%+v want_retryable=%v", deliveryErr, testCase.retryable)
			}
		})
	}
}
