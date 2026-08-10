// Package main 装配 model-uptime 服务：加载配置、打开存储、启动调度器与 HTTP 服务。
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lefachao/model-uptime/internal/api"
	"github.com/lefachao/model-uptime/internal/config"
	"github.com/lefachao/model-uptime/internal/scheduler"
	"github.com/lefachao/model-uptime/internal/store"
)

//go:embed config.template.yaml
var defaultConfigBytes []byte

// writeDefaultConfig 首次启动时落盘默认配置。
func writeDefaultConfig(path string) error {
	return os.WriteFile(path, defaultConfigBytes, 0o644)
}

func main() {
	port := os.Getenv("PORT")
	dataDir := os.Getenv("DATA_DIR")
	adminToken := os.Getenv("ADMIN_TOKEN")

	flag.StringVar(&port, "port", defaultStr(port, "8080"), "HTTP 监听端口")
	flag.StringVar(&dataDir, "data", defaultStr(dataDir, "./data"), "数据目录（配置与数据库）")
	flag.StringVar(&adminToken, "admin-token", adminToken, "配置页管理令牌（可用环境变量 ADMIN_TOKEN）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		logger.Error("创建数据目录失败", "err", err, "dir", dataDir)
		os.Exit(1)
	}

	configPath := filepath.Join(dataDir, "config.yaml")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := writeDefaultConfig(configPath); err != nil {
			logger.Error("生成默认配置失败", "err", err)
			os.Exit(1)
		}
		logger.Info("已生成默认配置，编辑后重启生效", "path", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	// 管理令牌优先级：环境变量 > 配置文件。两者均为空时，允许首次访问管理页设置密码。
	if adminToken == "" {
		adminToken = cfg.AdminToken
	}
	if adminToken == "" {
		logger.Warn("未配置管理令牌：首次访问 /admin/ 时可在页面设置密码")
	}

	st, err := store.Open(filepath.Join(dataDir, "probe.db"))
	if err != nil {
		logger.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	sch := scheduler.New(st, logger)
	sch.Reload(cfg.Services, cfg.Page)
	sch.Start()
	defer sch.Stop()

	srv, err := api.New(api.Options{
		Scheduler:  sch,
		ConfigPath: configPath,
		AdminToken: adminToken,
		Logger:     logger,
	}, cfg)
	if err != nil {
		logger.Error("初始化 HTTP 服务失败", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 优雅停机：收到信号后先停 HTTP（等待在途请求），调度器随 main 退出关闭
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopCh
		logger.Info("收到退出信号，正在优雅停机")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	logger.Info("model-uptime 已启动",
		"addr", httpServer.Addr,
		"config", configPath,
		"services", len(cfg.Services),
		"admin_configured", adminToken != "",
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP 服务异常退出", "err", err)
		os.Exit(1)
	}
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
