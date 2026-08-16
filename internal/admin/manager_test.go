package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
)

type memoryRepository struct {
	config settings.Config
	err    error
	saves  int
}

func (r *memoryRepository) Save(value *settings.Config) error {
	r.saves++
	if r.err != nil {
		return r.err
	}
	r.config = value.Clone()
	return nil
}

type fakeMonitor struct {
	services []model.Service
	page     model.PageConfig
	err      error
	reloads  int
}

func (m *fakeMonitor) Reload(services []model.Service, page model.PageConfig) error {
	m.reloads++
	if m.err != nil {
		return m.err
	}
	m.services = append([]model.Service(nil), services...)
	m.page = page
	return nil
}

func (m *fakeMonitor) ProbeNow(_ context.Context, id string) (*model.ProbeResult, error) {
	for _, service := range m.services {
		if service.ID == id {
			return &model.ProbeResult{OK: true}, nil
		}
	}
	return nil, errors.New("服务不存在: " + id)
}

type fakeNotifications struct {
	config  notification.Config
	err     error
	updates int
}

func (n *fakeNotifications) UpdateConfig(value notification.Config) error {
	n.updates++
	if n.err != nil {
		return n.err
	}
	n.config = value
	return nil
}

func (n *fakeNotifications) SendTest(context.Context, string, string) error { return nil }

func initialConfig() *settings.Config {
	enabled := true
	value := &settings.Config{
		AdminToken: "original-token",
		Page:       model.PageConfig{HistoryLen: 60, RefreshSec: 5, ShowUptime: true},
		Services: []model.Service{{
			ID: "one", Name: "One", Protocol: model.ProtocolHTTP, BaseURL: "https://example.com",
			APIKey: "secret", Enabled: &enabled,
		}},
	}
	value.Normalize()
	return value
}

func newManager(t *testing.T) (*admin.Manager, *memoryRepository, *fakeMonitor, *fakeNotifications) {
	t.Helper()
	repository := &memoryRepository{}
	monitor := &fakeMonitor{}
	notifications := &fakeNotifications{}
	manager, err := admin.New(admin.Options{
		Initial: initialConfig(), AdminToken: "original-token", Repository: repository,
		Monitor: monitor, Notifications: notifications,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, repository, monitor, notifications
}

func TestManagerServiceLifecycle(t *testing.T) {
	manager, repository, monitor, _ := newManager(t)
	created, err := manager.CreateService(model.Service{
		Name: "Second Service", Protocol: model.ProtocolHTTP, BaseURL: "https://example.org", APIKey: "new-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "second-service" || repository.saves != 1 || monitor.reloads != 1 {
		t.Fatalf("创建结果异常: service=%+v saves=%d reloads=%d", created, repository.saves, monitor.reloads)
	}

	created.Name = "Second Updated"
	created.APIKey = ""
	updated, err := manager.UpdateService(created.ID, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIKey != "new-secret" {
		t.Fatal("空密钥更新应保留原密钥")
	}

	duplicate, err := manager.DuplicateService(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != "second-service-copy" || duplicate.Name != "Second Updated (copy)" {
		t.Fatalf("复制结果异常: %+v", duplicate)
	}

	telegram := notification.Config{BotToken: "bot-token", Subscriptions: []notification.Subscription{{
		ID: "ops", Name: "Operations", Enabled: true, ChatID: "1", ServiceIDs: []string{created.ID},
	}}}
	if _, err := manager.UpdateTelegram(telegram); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteService(created.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if _, exists := snapshot.ServiceByID(created.ID); exists {
		t.Fatal("服务删除后仍存在")
	}
	if len(snapshot.Telegram.Subscriptions[0].ServiceIDs) != 0 {
		t.Fatal("删除服务后订阅引用未清理")
	}
}

func TestManagerPublishesAtomicallyAndRollsBack(t *testing.T) {
	manager, repository, monitor, notifications := newManager(t)
	monitor.err = errors.New("database unavailable")
	_, err := manager.CreateService(model.Service{
		ID: "two", Name: "Two", Protocol: model.ProtocolHTTP, BaseURL: "https://example.org",
	})
	if err == nil || admin.KindOf(err) != admin.ErrorInternal {
		t.Fatalf("运行时失败应返回内部错误: %v", err)
	}
	snapshot := manager.Snapshot()
	if _, exists := snapshot.ServiceByID("two"); exists {
		t.Fatal("运行时失败后内存配置不应改变")
	}
	if _, exists := repository.config.ServiceByID("two"); exists {
		t.Fatal("运行时失败后磁盘配置应回滚")
	}
	if notifications.updates != 2 {
		t.Fatalf("通知配置应先发布再回滚，updates=%d", notifications.updates)
	}
	if repository.saves != 2 {
		t.Fatalf("配置应先保存再回滚，saves=%d", repository.saves)
	}
}

func TestManagerRejectsPersistenceFailureWithoutPublishing(t *testing.T) {
	manager, repository, monitor, notifications := newManager(t)
	repository.err = errors.New("disk full")
	_, err := manager.UpdatePage(model.PageConfig{HistoryLen: 30, RefreshSec: 10, ShowUptime: true})
	if err == nil || admin.KindOf(err) != admin.ErrorInternal {
		t.Fatalf("持久化失败应返回内部错误: %v", err)
	}
	if monitor.reloads != 0 || notifications.updates != 0 {
		t.Fatalf("持久化失败不应发布运行时: monitor=%d notifications=%d", monitor.reloads, notifications.updates)
	}
	if manager.Snapshot().Page.HistoryLen != 60 {
		t.Fatal("持久化失败后内存配置不应改变")
	}
}

func TestManagerAuthenticationAndSetup(t *testing.T) {
	manager, _, _, _ := newManager(t)
	if !manager.Authenticate("original-token") || manager.Authenticate("wrong") {
		t.Fatal("管理密码校验异常")
	}
	if err := manager.SetupToken("another-token"); err == nil || admin.KindOf(err) != admin.ErrorConflict {
		t.Fatalf("已配置密码时 setup 应冲突: %v", err)
	}

	repository := &memoryRepository{}
	empty, err := admin.New(admin.Options{Initial: &settings.Config{}, Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.SetupToken("new-password"); err != nil {
		t.Fatal(err)
	}
	if !empty.Authenticate("new-password") || repository.config.AdminToken != "new-password" {
		t.Fatal("首次密码没有持久化并立即生效")
	}
}

func TestManagerSnapshotIsDeepCopy(t *testing.T) {
	manager, _, _, _ := newManager(t)
	snapshot := manager.Snapshot()
	snapshot.Services[0].Headers = map[string]string{"X-Test": "changed"}
	*snapshot.Services[0].Enabled = false
	if !manager.Snapshot().Services[0].IsEnabled() || manager.Snapshot().Services[0].Headers != nil {
		t.Fatal("调用方修改快照污染了管理器状态")
	}
}
