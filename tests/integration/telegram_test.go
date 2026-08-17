package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
	"github.com/xgxg-mdl/model-uptime/internal/storage/sqlite"
)

func TestTelegramAdminFlowAndServiceReferenceCleanup(t *testing.T) {
	requests := make(chan string, 2)
	var rejectTelegram atomic.Bool
	telegramAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析 Telegram 请求: %v", err)
		}
		if rejectTelegram.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
			return
		}
		requests <- r.URL.Path + "|" + r.Form.Get("chat_id") + "|" + r.Form.Get("parse_mode") + "|" + r.Form.Get("text") + "|" + r.Form.Get("reply_markup")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer telegramAPI.Close()

	dir := t.TempDir()
	cfg := &settings.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{PublicURL: "https://status.example.com/?from=test&view=full", HistoryLen: 60, RefreshSec: 5},
		Services: []model.Service{{
			ID: "s1", Name: "svc-one", Protocol: model.ProtocolHTTP,
			BaseURL: "http://example.com", IntervalSec: 60, Enabled: boolp(true),
		}},
		Telegram: notification.Config{
			BotToken: "secret-token",
			Subscriptions: []notification.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "-100", ServiceIDs: []string{"s1"},
			}},
		},
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	notifications, err := notification.New(notification.Options{
		APIBaseURL:  telegramAPI.URL,
		Repository:  notification.NewMemoryOutbox(),
		RetryDelays: []time.Duration{},
	}, cfg.Telegram)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := notifications.Close(ctx); err != nil {
			t.Errorf("关闭 notifier: %v", err)
		}
	}()
	ts := startIntegrationServer(t, cfg, configPath, nil, notifications)

	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/telegram", testToken, nil)
	if code != http.StatusOK || out["bot_token"] != "****" || out["token_configured"] != true {
		t.Fatalf("Telegram GET 未正确脱敏: code=%d out=%v", code, out)
	}

	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/telegram", testToken, map[string]any{
		"bot_token": "",
		"subscriptions": []map[string]any{{
			"id": "ops", "name": "Primary operations", "enabled": true,
			"chat_id": "-100", "language": "en-US", "service_ids": []string{"s1"}, "template": "<b>{{.TotalChanges}}</b>",
		}},
	})
	if code != http.StatusOK || out["bot_token"] != "****" {
		t.Fatalf("Telegram PUT 失败: code=%d out=%v", code, out)
	}
	saved, err := settings.Load(configPath)
	if err != nil || saved.Telegram.BotToken != "secret-token" || saved.Telegram.Subscriptions[0].Name != "Primary operations" || saved.Telegram.Subscriptions[0].Language != notification.LanguageEnglish {
		t.Fatalf("Token 保留或配置落盘失败: cfg=%+v err=%v", saved, err)
	}

	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/telegram/test", testToken, map[string]string{"subscription_id": "ops"})
	if code != http.StatusOK {
		t.Fatalf("Telegram 测试发送失败: code=%d out=%v", code, out)
	}
	for index := 0; index < 2; index++ {
		select {
		case request := <-requests:
			parts := strings.SplitN(request, "|", 5)
			if len(parts) != 5 || parts[0] != "/botsecret-token/sendMessage" || parts[1] != "-100" || parts[2] != "HTML" {
				t.Fatalf("Telegram 请求错误: %s", request)
			}
			if strings.Contains(parts[3], "Open status page") ||
				!strings.Contains(parts[4], `"text":"Open status page"`) ||
				!strings.Contains(parts[4], `"url":"https://status.example.com/?from=test\u0026view=full"`) {
				t.Fatalf("Telegram 测试消息未使用探针页按钮: %s", request)
			}
		case <-time.After(time.Second):
			t.Fatalf("只收到 %d 条 Telegram 分类测试消息", index)
		}
	}

	rejectTelegram.Store(true)
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/telegram/test", testToken, map[string]string{"subscription_id": "ops"})
	if code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "chat not found") {
		t.Fatalf("Telegram 错误必须完整返回管理页: code=%d out=%v", code, out)
	}
	rejectTelegram.Store(false)

	code, out = doJSON(t, ts, http.MethodDelete, "/api/admin/services/s1", testToken, nil)
	if code != http.StatusOK {
		t.Fatalf("删除服务失败: code=%d out=%v", code, out)
	}
	saved, err = settings.Load(configPath)
	if err != nil || len(saved.Telegram.Subscriptions) != 1 || len(saved.Telegram.Subscriptions[0].ServiceIDs) != 0 {
		t.Fatalf("删除服务未清理订阅引用: cfg=%+v err=%v", saved, err)
	}
}

func TestQuarantinedNotificationRecoversAfterOfflineConfigChange(t *testing.T) {
	var oldAttempts atomic.Int32
	var newAttempts atomic.Int32
	oldFourthAttempt := make(chan struct{})
	newDelivery := make(chan string, 1)
	var oldOnce sync.Once
	telegramAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析 Telegram 请求: %v", err)
		}
		switch r.URL.Path {
		case "/botold-token/sendMessage":
			if oldAttempts.Add(1) == 4 {
				oldOnce.Do(func() { close(oldFourthAttempt) })
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"invalid token"}`)
		case "/botnew-token/sendMessage":
			newAttempts.Add(1)
			newDelivery <- r.Form.Get("text")
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			t.Errorf("意外 Telegram 路径: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer telegramAPI.Close()

	databasePath := filepath.Join(t.TempDir(), "probe.db")
	store, err := sqlite.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	changedAt := time.Now()
	if err := store.Enqueue(context.Background(), []notification.Delivery{{
		DedupeKey: "offline-config", SubscriptionID: "ops", Text: "stale",
		AvailableAt: changedAt,
		RenderPayload: &notification.RenderPayload{
			ChangedAt: changedAt,
			Changes: []model.StatusChange{{
				ServiceID: "a", Model: "alpha", PreviousStatus: "up", Status: "down",
			}},
		},
	}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	oldConfig := notification.Config{BotToken: "old-token", Subscriptions: []notification.Subscription{{
		ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"a"},
		Template: `old {{range .Changes}}{{.Model}}{{end}}`,
	}}}
	oldNotifier, err := notification.New(notification.Options{
		APIBaseURL: telegramAPI.URL, Repository: store, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{}, RedeliveryDelays: []time.Duration{time.Millisecond},
	}, oldConfig)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	select {
	case <-oldFourthAttempt:
	case <-time.After(time.Second):
		t.Fatal("旧配置通知未进入第四次永久失败")
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	if err := oldNotifier.Close(closeCtx); err != nil {
		cancelClose()
		store.Close()
		t.Fatal(err)
	}
	cancelClose()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = sqlite.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	newConfig := notification.Config{BotToken: "new-token", Subscriptions: []notification.Subscription{{
		ID: "ops", Enabled: true, ChatID: "chat", ServiceIDs: []string{"a"},
		Template: `new {{range .Changes}}{{.Model}}{{end}}`,
	}}}
	newNotifier, err := notification.New(notification.Options{
		APIBaseURL: telegramAPI.URL, Repository: store, PollInterval: time.Millisecond,
		RetryDelays: []time.Duration{}, RedeliveryDelays: []time.Duration{time.Millisecond},
	}, newConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := newNotifier.Close(ctx); err != nil {
			t.Errorf("关闭新配置 notifier: %v", err)
		}
	}()
	select {
	case text := <-newDelivery:
		if text != "new alpha" {
			t.Fatalf("重启后没有使用新模板重渲染: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("离线修改配置并重启后，隔离通知没有恢复")
	}
	if oldAttempts.Load() != 4 || newAttempts.Load() != 1 {
		t.Fatalf("跨重启投递次数错误: old=%d new=%d", oldAttempts.Load(), newAttempts.Load())
	}
}
