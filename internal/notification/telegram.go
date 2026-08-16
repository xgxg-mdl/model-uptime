package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient 允许测试或调用方注入 HTTP 实现。
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type sendJob struct {
	botToken          string
	chatID            string
	text              string
	name              string
	configFingerprint string
}

type deliveryError struct {
	err        error
	retryable  bool
	retryAfter time.Duration
}

func (e *deliveryError) Error() string { return e.err.Error() }
func (e *deliveryError) Unwrap() error { return e.err }

func (n *Notifier) sendWithRetry(ctx context.Context, job sendJob) error {
	for attempt := 0; ; attempt++ {
		err := n.send(ctx, job)
		if err == nil {
			return nil
		}
		if !err.retryable || attempt >= len(n.retryDelays) {
			return err
		}
		delay := n.retryDelays[attempt]
		if err.retryAfter > delay {
			delay = err.retryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
}

func (n *Notifier) send(ctx context.Context, job sendJob) *deliveryError {
	form := url.Values{
		"chat_id":                  {job.chatID},
		"text":                     {job.text},
		"parse_mode":               {"HTML"},
		"disable_web_page_preview": {"true"},
	}
	endpoint := n.apiBaseURL + "/bot" + job.botToken + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return &deliveryError{err: redactTokenError(err, job.botToken)}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := n.client.Do(request)
	if err != nil {
		return &deliveryError{err: redactTokenError(err, job.botToken), retryable: true}
	}
	defer response.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode >= 200 && response.StatusCode < 300 && decodeErr == nil && result.OK {
		return nil
	}
	description := strings.TrimSpace(result.Description)
	if description == "" {
		if decodeErr != nil {
			description = decodeErr.Error()
		} else {
			description = http.StatusText(response.StatusCode)
		}
	}
	err = fmt.Errorf("Telegram API returned %d: %s", response.StatusCode, description)
	// 只有 HTTP 或 Telegram error_code 明确给出的非 429 4xx 才是配置型
	// 永久错误；2xx 未确认、3xx、429 和 5xx 都属于未知或可恢复结果。
	permanentHTTPError := response.StatusCode >= 400 && response.StatusCode < 500 &&
		response.StatusCode != http.StatusTooManyRequests
	permanentAPIError := result.ErrorCode >= 400 && result.ErrorCode < 500 &&
		result.ErrorCode != http.StatusTooManyRequests
	retryable := !permanentHTTPError && !permanentAPIError
	return &deliveryError{
		err: err, retryable: retryable,
		retryAfter: time.Duration(result.Parameters.RetryAfter) * time.Second,
	}
}

func redactTokenError(err error, token string) error {
	message := err.Error()
	if token == "" || !strings.Contains(message, token) {
		return err
	}
	return errors.New(strings.ReplaceAll(message, token, "****"))
}
