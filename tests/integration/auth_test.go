package integration_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
)

func TestAdminAuthRequired(t *testing.T) {
	ts := newIntegrationServer(t)
	code, _ := doJSON(t, ts, http.MethodGet, "/api/admin/services", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("无令牌应 401，got %d", code)
	}
	// 错误令牌
	code, _ = doJSON(t, ts, http.MethodGet, "/api/admin/services", "wrong-token", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("错误令牌应 401，got %d", code)
	}
}

func TestLogin(t *testing.T) {
	ts := newIntegrationServer(t)
	code, _ := doJSON(t, ts, http.MethodPost, "/api/admin/login", "", map[string]string{"token": testToken})
	if code != http.StatusOK {
		t.Errorf("正确令牌应 200，got %d", code)
	}
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/login", "", map[string]string{"token": "bad"})
	if code != http.StatusUnauthorized {
		t.Errorf("错误令牌应 401，got %d", code)
	}
}

// TestSetupFlow 首次设置管理密码：未配置时允许设置，设置后端点永久失效，
// 新密码写入配置文件并立即生效。
func TestSetupFlow(t *testing.T) {
	cfg := &settings.Config{Page: model.PageConfig{HistoryLen: 60}}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	ts := startIntegrationServer(t, cfg, configPath, nil, nil)

	// 未配置：setup-status = false
	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/setup-status", "", nil)
	if code != http.StatusOK || out["token_configured"] != false {
		t.Errorf("setup-status 应为 false: %d %v", code, out)
	}
	// 未配置时管理 API 拒绝
	code, _ = doJSON(t, ts, http.MethodGet, "/api/admin/services", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("未配置时管理 API 应 401，got %d", code)
	}

	// 空密码 / 过短密码 → 400
	for _, tk := range []string{"", "short"} {
		code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/setup", "", map[string]string{"token": tk})
		if code != http.StatusBadRequest {
			t.Errorf("密码 %q 应 400，got %d", tk, code)
		}
	}

	// 设置成功
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/setup", "", map[string]string{"token": "my-new-password"})
	if code != http.StatusOK {
		t.Fatalf("首次设置应 200，got %d", code)
	}
	// setup-status 翻转
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/setup-status", "", nil)
	if out["token_configured"] != true {
		t.Errorf("设置后 setup-status 应为 true: %v", out)
	}
	// 新密码立即生效，可访问管理 API
	code, _ = doJSON(t, ts, http.MethodGet, "/api/admin/services", "my-new-password", nil)
	if code != http.StatusOK {
		t.Errorf("新密码应可访问管理 API，got %d", code)
	}
	// 再次 setup → 永久失效
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/setup", "", map[string]string{"token": "another-password"})
	if code != http.StatusConflict {
		t.Errorf("已配置后 setup 应 409，got %d", code)
	}

	// 密码已持久化到配置文件
	loaded, err := settings.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AdminToken != "my-new-password" {
		t.Errorf("配置文件 admin_token = %q", loaded.AdminToken)
	}
}
