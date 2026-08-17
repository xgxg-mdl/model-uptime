// Package settings 负责配置文件的初始化、加载、校验与原子写回。
// 配置文件是配置的可信源，配置页的在线修改最终落盘到该文件并热重载。
package settings

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

//go:embed config.template.yaml
var defaultConfig []byte

// Config 是完整的服务配置。
type Config struct {
	AdminToken string              `yaml:"admin_token"`
	Page       model.PageConfig    `yaml:"page"`
	Services   []model.Service     `yaml:"services"`
	Telegram   notification.Config `yaml:"telegram"`
}

// Clone 返回可独立修改的深拷贝，避免配置快照之间共享 map、slice 或指针。
func (c *Config) Clone() Config {
	if c == nil {
		return Config{}
	}
	out := *c
	out.Services = append([]model.Service(nil), c.Services...)
	for i := range out.Services {
		out.Services[i].Enabled = cloneBool(c.Services[i].Enabled)
		out.Services[i].Stream = cloneBool(c.Services[i].Stream)
		out.Services[i].Headers = cloneStrings(c.Services[i].Headers)
	}
	out.Telegram.Subscriptions = append([]notification.Subscription(nil), c.Telegram.Subscriptions...)
	for i := range out.Telegram.Subscriptions {
		out.Telegram.Subscriptions[i].ServiceIDs = append([]string(nil), c.Telegram.Subscriptions[i].ServiceIDs...)
	}
	return out
}

// LoadOrCreate 原子创建缺失的默认配置，然后返回已校验的运行时快照。
// created 仅在本次调用完成首次创建时为 true。
func LoadOrCreate(path string) (config *Config, created bool, err error) {
	created, err = createDefault(path)
	if err != nil {
		return nil, false, err
	}
	config, err = Load(path)
	if err != nil {
		return nil, created, err
	}
	return config, created, nil
}

func createDefault(path string) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("创建配置目录失败: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查配置文件失败: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-default-*.tmp")
	if err != nil {
		return false, fmt.Errorf("创建默认配置临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("限制默认配置权限失败: %w", err)
	}
	if _, err := tmp.Write(defaultConfig); err != nil {
		tmp.Close()
		return false, fmt.Errorf("写入默认配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("同步默认配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("关闭默认配置临时文件失败: %w", err)
	}

	// Link 将已完整同步的文件以“仅当目标不存在”语义发布，
	// 避免并发启动覆盖另一个进程刚创建的配置。
	if err := os.Link(tmpName, path); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("发布默认配置失败: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return false, err
	}
	return true, nil
}

// Load 从文件读取配置，填充默认值并校验。
// 文件不存在时返回零配置（由调用方决定是否落盘初始文件）。
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(c); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("解析配置 %s 失败: 只允许一个 YAML 文档", path)
		}
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	c.Normalize()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	return c, nil
}

// Normalize 填充各层默认值。
func (c *Config) Normalize() {
	c.Page.Normalize()
	notification.NormalizeConfig(&c.Telegram)
	for i := range c.Services {
		c.Services[i].Normalize()
	}
	normalizeServiceSortOrders(c.Services)
}

// normalizeServiceSortOrders 将旧配置中缺失的排序值依次追加到现有最大值之后，
// 既保留原列表顺序，也不会覆盖用户显式设置的排序值。
func normalizeServiceSortOrders(services []model.Service) {
	maxOrder := 0
	for _, service := range services {
		if service.SortOrder > maxOrder {
			maxOrder = service.SortOrder
		}
	}
	for index := range services {
		if services[index].SortOrder != 0 {
			continue
		}
		maxOrder++
		services[index].SortOrder = maxOrder
	}
}

// Validate 校验整份配置。
func (c *Config) Validate() error {
	if err := c.Page.Validate(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(c.Services))
	for i := range c.Services {
		svc := &c.Services[i]
		if err := svc.Validate(); err != nil {
			return err
		}
		if seen[svc.ID] {
			return fmt.Errorf("服务 id 重复: %q", svc.ID)
		}
		seen[svc.ID] = true
	}
	if err := notification.ValidateConfig(c.Telegram); err != nil {
		return err
	}
	for _, subscription := range c.Telegram.Subscriptions {
		if subscription.Name == "" {
			return fmt.Errorf("Telegram 订阅 %q: name 不能为空", subscription.ID)
		}
		references := make(map[string]bool, len(subscription.ServiceIDs))
		for _, id := range subscription.ServiceIDs {
			if id == "" || !seen[id] {
				return fmt.Errorf("Telegram 订阅 %q 引用了不存在的服务 %q", subscription.ID, id)
			}
			if references[id] {
				return fmt.Errorf("Telegram 订阅 %q 重复引用服务 %q", subscription.ID, id)
			}
			references[id] = true
		}
	}
	return nil
}

// Save 原子写回配置：先写临时文件再 rename，避免中途崩溃留下半截文件。
func (c *Config) Save(path string) error {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return fmt.Errorf("拒绝保存无效配置: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后该行是 no-op
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("限制临时配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	// rename 只能保证命名原子性；先同步文件，再同步目录，避免断电后出现
	// 文件名已切换但内容尚未持久化的窗口。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步临时配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("落盘配置失败: %w", err)
	}
	return syncDirectory(dir)
}

// ServiceByID 返回指定 id 的服务副本；不存在返回零值与 false。
func (c *Config) ServiceByID(id string) (model.Service, bool) {
	for _, s := range c.Services {
		if s.ID == id {
			return s, true
		}
	}
	return model.Service{}, false
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开配置目录失败: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("同步配置目录失败: %w", err)
	}
	return nil
}
