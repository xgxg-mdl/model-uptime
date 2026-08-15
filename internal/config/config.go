// Package config 负责配置文件的加载、校验与原子写回。
// 配置文件是配置的可信源，配置页的在线修改最终落盘到该文件并热重载。
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
)

// Config 是完整的服务配置。
type Config struct {
	AdminToken string           `yaml:"admin_token"`
	Page       model.PageConfig `yaml:"page"`
	Services   []model.Service  `yaml:"services"`
	Telegram   notifier.Config  `yaml:"telegram"`
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
	if err := yaml.Unmarshal(data, c); err != nil {
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
	notifier.NormalizeConfig(&c.Telegram)
	for i := range c.Services {
		c.Services[i].Normalize()
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
	if err := notifier.ValidateConfig(c.Telegram); err != nil {
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
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("落盘配置失败: %w", err)
	}
	return nil
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
