// Package app 负责组装进程内模块，并按依赖顺序管理它们的生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/httpserver"
	"github.com/xgxg-mdl/model-uptime/internal/monitor"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
	"github.com/xgxg-mdl/model-uptime/internal/storage/sqlite"
	"github.com/xgxg-mdl/model-uptime/internal/update"
)

const defaultShutdownTimeout = 10 * time.Second

// BuildInfo 是发布构建注入的版本元数据。
type BuildInfo struct {
	Version string
	Commit  string
	BuiltAt string
}

// Options 描述一个进程实例的部署参数。
type Options struct {
	Address         string
	DataDir         string
	AdminToken      string
	BuildInfo       BuildInfo
	DeploymentTag   string
	UpdateURL       string
	UpdateToken     string
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
}

type application struct {
	http          *http.Server
	monitor       *monitor.Scheduler
	notifications *notification.Notifier
	updates       *update.Service
	store         *sqlite.Store
	logger        *slog.Logger
	timeout       time.Duration
	configPath    string
	serviceCount  int
	configCreated bool
	adminReady    bool
}

// Run 启动应用，直到 ctx 取消或 HTTP listener 失败。
func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	applyDefaults(&options)
	runtime, err := build(options)
	if err != nil {
		return err
	}
	if err := runtime.monitor.Start(context.Background()); err != nil {
		return errors.Join(fmt.Errorf("启动监控调度器失败: %w", err), runtime.shutdown())
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- runtime.http.ListenAndServe()
	}()
	if runtime.configCreated {
		runtime.logger.Info("已生成默认配置，编辑后重启生效", "path", runtime.configPath)
	}
	runtime.logger.Info("model-uptime 已启动",
		"addr", runtime.http.Addr,
		"config", runtime.configPath,
		"services", runtime.serviceCount,
		"admin_configured", runtime.adminReady,
		"version", options.BuildInfo.Version,
		"commit", options.BuildInfo.Commit,
	)

	var runErr error
	select {
	case <-ctx.Done():
		runtime.logger.Info("收到退出信号，正在优雅停机")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
	}
	return errors.Join(runErr, runtime.shutdown())
}

func applyDefaults(options *Options) {
	if options.Address == "" {
		options.Address = ":8080"
	}
	if options.DataDir == "" {
		options.DataDir = "./data"
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
}

func build(options Options) (*application, error) {
	if err := os.MkdirAll(options.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	configPath := filepath.Join(options.DataDir, "config.yaml")
	config, created, err := settings.LoadOrCreate(configPath)
	if err != nil {
		return nil, err
	}
	adminToken := options.AdminToken
	if adminToken == "" {
		adminToken = config.AdminToken
	}
	if adminToken == "" {
		options.Logger.Warn("未配置管理令牌：首次访问 /admin/ 时可在页面设置密码")
	}

	runtime := &application{
		logger: options.Logger, timeout: options.ShutdownTimeout,
		configPath: configPath, serviceCount: len(config.Services),
		configCreated: created, adminReady: adminToken != "",
	}
	fail := func(cause error) (*application, error) {
		return nil, errors.Join(cause, runtime.shutdown())
	}
	runtime.store, err = sqlite.Open(filepath.Join(options.DataDir, "probe.db"))
	if err != nil {
		return fail(fmt.Errorf("打开数据库失败: %w", err))
	}
	runtime.notifications, err = notification.New(notification.Options{
		Logger:     options.Logger,
		Repository: runtime.store,
	}, config.Telegram)
	if err != nil {
		return fail(fmt.Errorf("初始化 Telegram 通知模块失败: %w", err))
	}
	runtime.updates = update.New(update.Options{
		BuildInfo: update.BuildInfo{
			Version: options.BuildInfo.Version,
			Commit:  options.BuildInfo.Commit,
			BuiltAt: options.BuildInfo.BuiltAt,
		},
		DeploymentTag: options.DeploymentTag,
		UpdateURL:     options.UpdateURL,
		UpdateToken:   options.UpdateToken,
		Logger:        options.Logger,
	})
	runtime.monitor = monitor.New(runtime.store, options.Logger)
	if err := runtime.monitor.Reload(config.Services, config.Page); err != nil {
		return fail(fmt.Errorf("加载监控配置失败: %w", err))
	}
	manager, err := admin.New(admin.Options{
		Initial:       config,
		AdminToken:    adminToken,
		Repository:    settings.NewFileRepository(configPath),
		Monitor:       runtime.monitor,
		Notifications: runtime.notifications,
	})
	if err != nil {
		return fail(fmt.Errorf("初始化管理模块失败: %w", err))
	}
	httpHandler, err := httpserver.New(httpserver.Options{
		Admin:   manager,
		Status:  runtime.monitor,
		Updater: runtime.updates,
		Logger:  options.Logger,
	})
	if err != nil {
		return fail(fmt.Errorf("初始化 HTTP 模块失败: %w", err))
	}
	runtime.http = &http.Server{
		Addr:              options.Address,
		Handler:           httpHandler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return runtime, nil
}

// shutdown 从最外层入口向持久化层逐级停止，避免下游关闭后仍有在途 I/O。
func (a *application) shutdown() error {
	var errs []error
	if a.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
		err := a.http.Shutdown(ctx)
		cancel()
		if err != nil {
			err = errors.Join(err, a.http.Close())
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, fmt.Errorf("关闭 HTTP 模块: %w", err))
		}
	}
	if a.monitor != nil {
		if err := stopAndWait(a.timeout, a.monitor.Stop); err != nil {
			errs = append(errs, fmt.Errorf("关闭监控模块: %w", err))
		}
	}
	if a.notifications != nil {
		if err := stopAndWait(a.timeout, a.notifications.Close); err != nil {
			errs = append(errs, fmt.Errorf("关闭通知模块: %w", err))
		}
	}
	if a.updates != nil {
		if err := stopAndWait(a.timeout, a.updates.Close); err != nil {
			errs = append(errs, fmt.Errorf("关闭更新模块: %w", err))
		}
	}
	if a.store != nil {
		if err := a.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭 SQLite: %w", err))
		}
	}
	return errors.Join(errs...)
}

func stopAndWait(timeout time.Duration, stop func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := stop(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	// 超时仍保留错误语义，但不能在下游依赖仍被访问时继续关闭。
	return errors.Join(err, stop(context.Background()))
}
