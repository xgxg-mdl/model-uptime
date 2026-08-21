package admin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
)

// Monitor 是管理命令所需的监控模块接口。
type Monitor interface {
	Reload([]model.Service, model.PageConfig) error
	ProbeNow(context.Context, string) (*model.ProbeResult, error)
}

// Notifications 是管理命令所需的通知模块接口。
type Notifications interface {
	UpdateConfig(notification.Config) error
	SendTest(context.Context, string, string) error
}

// SendDailyTestNotification 发送日报格式的测试通知。
func (m *Manager) SendDailyTestNotification(ctx context.Context, subscriptionID string) error {
	if m.notifications == nil {
		return internal("通知模块未初始化", nil)
	}
	snapshot := m.Snapshot()
	daily, ok := m.notifications.(interface {
		SendDailyTest(context.Context, string, string) error
	})
	if !ok {
		return internal("通知模块不支持日报测试", nil)
	}
	return daily.SendDailyTest(ctx, subscriptionID, snapshot.Page.PublicURL)
}

// ConfigRepository 是管理模块持久化配置事务所需的最小接口。
// 接口由消费方定义，settings.FileRepository 作为文件存储适配器实现它。
type ConfigRepository interface {
	Save(*settings.Config) error
}

type Options struct {
	Initial       *settings.Config
	AdminToken    string
	Repository    ConfigRepository
	Monitor       Monitor
	Notifications Notifications
}

// Manager 串行化“复制、修改、持久化、发布”事务，并持有唯一运行时配置快照。
type Manager struct {
	mu            sync.RWMutex
	config        settings.Config
	adminToken    string
	repository    ConfigRepository
	monitor       Monitor
	notifications Notifications
}

func New(options Options) (*Manager, error) {
	if options.Initial == nil {
		return nil, errors.New("initial config is required")
	}
	initial := options.Initial.Clone()
	initial.Normalize()
	if err := initial.Validate(); err != nil {
		return nil, fmt.Errorf("初始配置无效: %w", err)
	}
	return &Manager{
		config:        initial,
		adminToken:    options.AdminToken,
		repository:    options.Repository,
		monitor:       options.Monitor,
		notifications: options.Notifications,
	}, nil
}

func (m *Manager) Snapshot() settings.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Clone()
}

func (m *Manager) TokenConfigured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.adminToken != ""
}

func (m *Manager) Authenticate(token string) bool {
	m.mu.RLock()
	expected := m.adminToken
	m.mu.RUnlock()
	if expected == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (m *Manager) SetupToken(token string) error {
	token = strings.TrimSpace(token)
	if utf8.RuneCountInString(token) < 8 {
		return invalid("管理密码至少需要 8 个字符")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adminToken != "" {
		return conflict("管理密码已设置，请直接登录")
	}
	next := m.config.Clone()
	next.AdminToken = token
	if err := m.commitLocked(next); err != nil {
		return err
	}
	m.adminToken = token
	return nil
}

func (m *Manager) CreateService(service model.Service) (model.Service, error) {
	// uid 只能由服务端生成，避免请求体注入一个已有历史所属的内部身份。
	service.UID = ""
	service.LegacyID = ""
	service.Normalize()
	if err := service.Validate(); err != nil {
		return model.Service{}, invalid(err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	if _, exists := next.ServiceByModel(service.Model); exists {
		return model.Service{}, conflict("服务 model 已存在: %s", service.Model)
	}
	if service.SortOrder == 0 {
		service.SortOrder = nextServiceSortOrder(next.Services)
	}
	next.Services = append(next.Services, service)
	if err := m.commitLocked(next); err != nil {
		return model.Service{}, err
	}
	return service, nil
}

func (m *Manager) UpdateService(uid string, service model.Service) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	index := serviceIndex(next.Services, uid)
	if index < 0 {
		return model.Service{}, notFound("服务不存在: %s", uid)
	}
	previous := next.Services[index]
	if service.APIKey == "" || service.APIKey == model.APIKeySentinel {
		service.APIKey = previous.APIKey
	}
	service.UID = previous.UID
	service.LegacyID = ""
	if service.SortOrder == 0 {
		service.SortOrder = previous.SortOrder
	}
	service.Normalize()
	if err := service.Validate(); err != nil {
		return model.Service{}, invalid(err.Error())
	}
	if duplicate, exists := next.ServiceByModel(service.Model); exists && duplicate.UID != uid {
		return model.Service{}, conflict("服务 model 已存在: %s", service.Model)
	}
	next.Services[index] = service
	if err := m.commitLocked(next); err != nil {
		return model.Service{}, err
	}
	return service, nil
}

func (m *Manager) DuplicateService(uid string) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	index := serviceIndex(next.Services, uid)
	if index < 0 {
		return model.Service{}, notFound("服务不存在: %s", uid)
	}
	duplicate := next.Services[index]
	duplicate.UID = ""
	duplicate.LegacyID = ""
	base := duplicate.Model + "-copy"
	duplicate.Model = base
	for suffix := 2; serviceModelIndex(next.Services, duplicate.Model) >= 0; suffix++ {
		duplicate.Model = fmt.Sprintf("%s%d", base, suffix)
	}
	duplicate.Name += " (copy)"
	duplicate.SortOrder = nextServiceSortOrder(next.Services)
	duplicate.Normalize()
	next.Services = append(next.Services, duplicate)
	if err := m.commitLocked(next); err != nil {
		return model.Service{}, err
	}
	return duplicate, nil
}

func (m *Manager) DeleteService(uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	index := serviceIndex(next.Services, uid)
	if index < 0 {
		return notFound("服务不存在: %s", uid)
	}
	next.Services = append(next.Services[:index], next.Services[index+1:]...)
	for index := range next.Telegram.Subscriptions {
		subscription := &next.Telegram.Subscriptions[index]
		selected := subscription.ServiceUIDs[:0]
		for _, serviceUID := range subscription.ServiceUIDs {
			if serviceUID != uid {
				selected = append(selected, serviceUID)
			}
		}
		subscription.ServiceUIDs = selected
	}
	return m.commitLocked(next)
}

type ServicePatch struct {
	Enabled     *bool
	IntervalSec *int
	TimeoutSec  *int
	WarningSec  *int
	Stream      *bool
}

func (m *Manager) UpdateServices(uids []string, patch ServicePatch) ([]model.Service, error) {
	if len(uids) == 0 {
		return nil, invalid("uids 不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	selected := make(map[string]struct{}, len(uids))
	missing := make([]string, 0)
	for _, uid := range uids {
		if _, duplicate := selected[uid]; duplicate {
			continue
		}
		selected[uid] = struct{}{}
		if serviceIndex(next.Services, uid) < 0 {
			missing = append(missing, uid)
		}
	}
	if len(missing) > 0 {
		return nil, notFound("服务不存在: %s", strings.Join(missing, ", "))
	}
	for index := range next.Services {
		service := &next.Services[index]
		if _, ok := selected[service.UID]; !ok {
			continue
		}
		if patch.Enabled != nil {
			service.Enabled = copyBool(patch.Enabled)
		}
		if patch.IntervalSec != nil {
			service.IntervalSec = *patch.IntervalSec
		}
		if patch.TimeoutSec != nil {
			service.TimeoutSec = *patch.TimeoutSec
		}
		if patch.WarningSec != nil {
			service.WarningSec = *patch.WarningSec
		}
		if patch.Stream != nil {
			service.Stream = copyBool(patch.Stream)
		}
	}
	if err := m.commitLocked(next); err != nil {
		return nil, err
	}
	return m.config.Clone().Services, nil
}

func (m *Manager) UpdatePage(page model.PageConfig) (model.PageConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	next.Page = page
	if err := m.commitLocked(next); err != nil {
		return model.PageConfig{}, err
	}
	return m.config.Page, nil
}

func (m *Manager) UpdateTelegram(nextConfig notification.Config) (notification.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.config.Clone()
	if nextConfig.BotToken == "" || nextConfig.BotToken == "****" || nextConfig.BotToken == model.APIKeySentinel {
		nextConfig.BotToken = next.Telegram.BotToken
	}
	next.Telegram = nextConfig
	if err := m.commitLocked(next); err != nil {
		return notification.Config{}, err
	}
	return m.config.Telegram, nil
}

func (m *Manager) ProbeNow(ctx context.Context, uid string) (*model.ProbeResult, error) {
	if m.monitor == nil {
		return nil, internal("监控模块未初始化", nil)
	}
	result, err := m.monitor.ProbeNow(ctx, uid)
	if err != nil {
		return nil, notFound("%s", err)
	}
	return result, nil
}

func (m *Manager) SendTestNotification(ctx context.Context, subscriptionID string) error {
	if m.notifications == nil {
		return internal("Telegram 通知器未初始化", nil)
	}
	snapshot := m.Snapshot()
	if err := m.notifications.SendTest(ctx, subscriptionID, snapshot.Page.PublicURL); err != nil {
		return internal(err.Error(), err)
	}
	return nil
}

// commitLocked 先持久化新文档，再发布到运行时。发布失败时按相反顺序回滚，
// 让磁盘、通知器、监控器和内存快照保持同一个已知版本。
func (m *Manager) commitLocked(next settings.Config) error {
	next.Normalize()
	if err := next.Validate(); err != nil {
		return invalid(err.Error())
	}
	previous := m.config.Clone()
	if m.repository != nil {
		if err := m.repository.Save(&next); err != nil {
			return internal("保存配置失败: "+err.Error(), err)
		}
	}
	if m.notifications != nil {
		if err := m.notifications.UpdateConfig(next.Telegram); err != nil {
			return m.rollbackLocked(previous, false, internal("应用通知配置失败: "+err.Error(), err))
		}
	}
	if m.monitor != nil {
		if err := m.monitor.Reload(next.Services, next.Page); err != nil {
			return m.rollbackLocked(previous, true, internal("应用监控配置失败: "+err.Error(), err))
		}
	}
	m.config = next
	return nil
}

func (m *Manager) rollbackLocked(previous settings.Config, rollbackNotifications bool, cause error) error {
	errorsToJoin := []error{cause}
	if rollbackNotifications && m.notifications != nil {
		if err := m.notifications.UpdateConfig(previous.Telegram); err != nil {
			errorsToJoin = append(errorsToJoin, fmt.Errorf("回滚通知配置失败: %w", err))
		}
	}
	if m.repository != nil {
		if err := m.repository.Save(&previous); err != nil {
			errorsToJoin = append(errorsToJoin, fmt.Errorf("回滚配置文件失败: %w", err))
		}
	}
	return errors.Join(errorsToJoin...)
}

func serviceIndex(services []model.Service, uid string) int {
	for index := range services {
		if services[index].UID == uid {
			return index
		}
	}
	return -1
}

func serviceModelIndex(services []model.Service, serviceModel string) int {
	for index := range services {
		if services[index].Model == serviceModel {
			return index
		}
	}
	return -1
}

func nextServiceSortOrder(services []model.Service) int {
	maxOrder := 0
	for _, service := range services {
		if service.SortOrder > maxOrder {
			maxOrder = service.SortOrder
		}
	}
	return maxOrder + 1
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
