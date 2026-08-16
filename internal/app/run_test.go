package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testOptions(dataDir string) Options {
	return Options{
		Address:         "127.0.0.1:0",
		DataDir:         dataDir,
		AdminToken:      "test-password",
		ShutdownTimeout: 2 * time.Second,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestRunCreatesDataAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dataDir := t.TempDir()
	if err := Run(ctx, testOptions(dataDir)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", "probe.db"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err != nil {
			t.Fatalf("应用未创建 %s: %v", name, err)
		}
	}
}

func TestRunReturnsListenerFailureAfterCleaningUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	options := testOptions(t.TempDir())
	options.Address = listener.Addr().String()
	err = Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "HTTP 服务异常退出") {
		t.Fatalf("监听冲突错误 = %v", err)
	}
}
