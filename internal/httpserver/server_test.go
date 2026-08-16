package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
	"github.com/xgxg-mdl/model-uptime/internal/update"
)

type testRepository struct {
	saved settings.Config
}

func (r *testRepository) Save(next *settings.Config) error {
	r.saved = next.Clone()
	return nil
}

type testMonitor struct {
	page model.PageConfig
}

func (m *testMonitor) Reload(_ []model.Service, page model.PageConfig) error {
	m.page = page
	return nil
}

func (*testMonitor) ProbeNow(context.Context, string) (*model.ProbeResult, error) {
	return nil, errors.New("服务不存在")
}

type testNotifications struct{}

func (*testNotifications) UpdateConfig(notification.Config) error { return nil }
func (*testNotifications) SendTest(context.Context, string, string) error {
	return nil
}

type testStatusProvider struct{}

func (testStatusProvider) Snapshot() model.StatusResponse {
	return model.StatusResponse{GeneratedAt: 123, AllOK: true, Services: []model.ServiceView{}}
}

type testUpdateProvider struct {
	status update.Status
	forces []bool
	starts []string
	err    error
}

func (u *testUpdateProvider) Check(_ context.Context, force bool) (update.Status, error) {
	u.forces = append(u.forces, force)
	return u.status, u.err
}

func (u *testUpdateProvider) Start(version string) error {
	u.starts = append(u.starts, version)
	return u.err
}

type testServer struct {
	handler http.Handler
	repo    *testRepository
	monitor *testMonitor
}

func newTestServer(t *testing.T, updateProvider UpdateProvider) testServer {
	t.Helper()
	initial := &settings.Config{}
	repository := &testRepository{}
	monitor := &testMonitor{}
	manager, err := admin.New(admin.Options{
		Initial:       initial,
		AdminToken:    "correct-password",
		Repository:    repository,
		Monitor:       monitor,
		Notifications: &testNotifications{},
	})
	if err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><title>Status</title>")},
		"admin/index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Admin</title>")},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log('ok')")},
		"fonts/test.woff2": &fstest.MapFile{Data: []byte("font")},
	}
	server, err := New(Options{
		Admin: manager, Status: testStatusProvider{}, Updater: updateProvider,
		Assets: assets, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return testServer{handler: server.Handler(), repo: repository, monitor: monitor}
}

func request(t *testing.T, handler http.Handler, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		req.Header.Set("Authorization", "Bearer correct-password")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestStaticFilesHaveSecurityAndCacheHeaders(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)

	page := request(t, server.handler, http.MethodGet, "/", "", false)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<title>Status</title>") {
		t.Fatalf("状态页响应 = %d %q", page.Code, page.Body.String())
	}
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTML Cache-Control = %q", page.Header().Get("Cache-Control"))
	}
	for _, header := range []string{"Content-Security-Policy", "Permissions-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if page.Header().Get(header) == "" {
			t.Errorf("缺少安全响应头 %s", header)
		}
	}

	asset := request(t, server.handler, http.MethodGet, "/assets/app.js", "", false)
	if asset.Code != http.StatusOK || asset.Header().Get("Cache-Control") != "public, max-age=300, must-revalidate" {
		t.Fatalf("脚本缓存响应 = %d, %q", asset.Code, asset.Header().Get("Cache-Control"))
	}
	font := request(t, server.handler, http.MethodGet, "/fonts/test.woff2", "", false)
	if font.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("字体 Cache-Control = %q", font.Header().Get("Cache-Control"))
	}
	health := request(t, server.handler, http.MethodGet, "/healthz", "", false)
	if health.Code != http.StatusNoContent || health.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("健康检查响应 = %d, %q", health.Code, health.Header().Get("Cache-Control"))
	}
}

func TestWebAssetsContainsEntryPages(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"index.html", "admin/index.html"} {
		contents, err := fs.ReadFile(webAssets(), name)
		if err != nil {
			t.Fatalf("读取嵌入资源 %s: %v", name, err)
		}
		if !strings.Contains(strings.ToLower(string(contents)), "<!doctype html>") {
			t.Fatalf("嵌入资源 %s 不是完整 HTML 页面", name)
		}
	}
}

func TestAdminEndpointsRequireBearerAuthentication(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)

	unauthorized := request(t, server.handler, http.MethodGet, "/api/admin/services", "", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未认证状态码 = %d", unauthorized.Code)
	}
	authorized := request(t, server.handler, http.MethodGet, "/api/admin/services", "", true)
	if authorized.Code != http.StatusOK {
		t.Fatalf("已认证状态码 = %d: %s", authorized.Code, authorized.Body.String())
	}
}

func TestJSONRequestsRejectUnknownTrailingAndOversizedBodies(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "unknown field", body: `{"unknown":true}`, status: http.StatusBadRequest},
		{name: "trailing value", body: `{}` + ` {}`, status: http.StatusBadRequest},
		{name: "too large", body: `{"name":"` + strings.Repeat("a", maxJSONBody) + `"}`, status: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			recorder := request(t, server.handler, http.MethodPost, "/api/admin/services", test.body, true)
			if recorder.Code != test.status {
				t.Fatalf("状态码 = %d，期望 %d，响应 %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestPageUpdateReturnsNormalizedServerState(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	recorder := request(t, server.handler, http.MethodPut, "/api/admin/page", `{}`, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page model.PageConfig
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Title == "" || page.RefreshSec != 5 || !page.ShowUptime || !page.ShowSamples || !page.ShowLatency || !page.ShowAvgLoad {
		t.Fatalf("响应不是归一化页面配置: %+v", page)
	}
	if server.repo.saved.Page != page || server.monitor.page != page {
		t.Fatalf("磁盘、监控器与响应状态不一致: saved=%+v monitor=%+v response=%+v", server.repo.saved.Page, server.monitor.page, page)
	}
}

func TestTelegramViewIncludesCanonicalTemplatesAndMasksToken(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	recorder := request(t, server.handler, http.MethodGet, "/api/admin/telegram", "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		BotToken        string            `json:"bot_token"`
		TokenConfigured bool              `json:"token_configured"`
		Templates       map[string]string `json:"templates"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BotToken != "" || response.TokenConfigured {
		t.Fatalf("空 token 视图错误: %+v", response)
	}
	if response.Templates["zh"] != notification.TemplateForLanguage("zh") || response.Templates["en"] != notification.TemplateForLanguage("en") {
		t.Fatal("Telegram 内置模板没有由通知模块统一下发")
	}
}

func TestUpdateEndpointsPreserveCheckAndStartSemantics(t *testing.T) {
	t.Parallel()
	updates := &testUpdateProvider{status: update.Status{
		Enabled: true, CurrentVersion: "v0.8.4", LatestVersion: "v0.9.0", UpdateAvailable: true,
	}}
	server := newTestServer(t, updates)

	get := request(t, server.handler, http.MethodGet, "/api/admin/update", "", true)
	start := request(t, server.handler, http.MethodPost, "/api/admin/update", "", true)
	if get.Code != http.StatusOK || start.Code != http.StatusAccepted {
		t.Fatalf("更新接口状态码 = GET %d, POST %d", get.Code, start.Code)
	}
	if len(updates.forces) != 2 || updates.forces[0] || !updates.forces[1] {
		t.Fatalf("版本检查 force 序列 = %v", updates.forces)
	}
	if len(updates.starts) != 1 || updates.starts[0] != "v0.9.0" {
		t.Fatalf("触发版本 = %v", updates.starts)
	}
}

func TestNewRejectsMissingRequiredModules(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok"), Mode: fs.ModePerm}}
	_, err := New(Options{Assets: assets, Status: testStatusProvider{}})
	if err == nil {
		t.Fatal("缺少管理模块时应拒绝创建 HTTP server")
	}
}
