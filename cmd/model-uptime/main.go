// Package main 解析进程参数与系统信号，应用装配由 internal/app 统一负责。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xgxg-mdl/model-uptime/internal/app"
)

// 由发布镜像的 ldflags 注入；源码直接构建时保持开发标识。
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	port := os.Getenv("PORT")
	dataDir := os.Getenv("DATA_DIR")
	adminToken := os.Getenv("ADMIN_TOKEN")
	flag.StringVar(&port, "port", defaultString(port, "8080"), "HTTP 监听端口")
	flag.StringVar(&dataDir, "data", defaultString(dataDir, "./data"), "数据目录（配置与数据库）")
	flag.StringVar(&adminToken, "admin-token", adminToken, "配置页管理令牌（可用环境变量 ADMIN_TOKEN）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, app.Options{
		Address:    ":" + port,
		DataDir:    dataDir,
		AdminToken: adminToken,
		BuildInfo: app.BuildInfo{
			Version: version,
			Commit:  commit,
			BuiltAt: buildTime,
		},
		DeploymentTag: os.Getenv("MODEL_UPTIME_TAG"),
		UpdateURL:     os.Getenv("UPDATE_URL"),
		UpdateToken:   os.Getenv("UPDATE_TOKEN"),
		Logger:        logger,
	}); err != nil {
		logger.Error("model-uptime 退出", "err", err)
		os.Exit(1)
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
