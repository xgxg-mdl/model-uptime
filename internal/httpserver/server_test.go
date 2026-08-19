package httpserver

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/heatmap"
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

type testHeatmapProvider struct {
	response heatmap.Response
	err      error
	ranges   []string
}

func (p *testHeatmapProvider) Build(_ context.Context, rangeName string) (heatmap.Response, error) {
	p.ranges = append(p.ranges, rangeName)
	return p.response, p.err
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
	handler  http.Handler
	repo     *testRepository
	monitor  *testMonitor
	heatmaps *testHeatmapProvider
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
		"index.html":         &fstest.MapFile{Data: []byte("<!doctype html><title>Status</title>")},
		"admin/index.html":   &fstest.MapFile{Data: []byte("<!doctype html><title>Admin</title>")},
		"heatmap/index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Heatmap</title>")},
		"assets/app.js":      &fstest.MapFile{Data: []byte("console.log('ok')")},
		"fonts/test.woff2":   &fstest.MapFile{Data: []byte("font")},
	}
	heatmaps := &testHeatmapProvider{}
	server, err := New(Options{
		Admin: manager, Status: testStatusProvider{}, Heatmap: heatmaps, Updater: updateProvider,
		Assets: assets, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return testServer{handler: server.Handler(), repo: repository, monitor: monitor, heatmaps: heatmaps}
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
	heatmapPage := request(t, server.handler, http.MethodGet, "/heatmap", "", false)
	if heatmapPage.Code != http.StatusOK || !strings.Contains(heatmapPage.Body.String(), "<title>Heatmap</title>") {
		t.Fatalf("热力图页面响应 = %d %q", heatmapPage.Code, heatmapPage.Body.String())
	}
	if heatmapPage.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("热力图 HTML Cache-Control = %q", heatmapPage.Header().Get("Cache-Control"))
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

func TestPublicHeatmapEndpointDefaultsTo7D(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	server.heatmaps.response = heatmap.Response{Range: heatmap.Range7D, Timezone: "Asia/Shanghai"}

	response := request(t, server.handler, http.MethodGet, "/api/heatmap", "", false)
	if response.Code != http.StatusOK {
		t.Fatalf("热力图 API = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Encoding") != "" || !strings.Contains(response.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("identity 热力图编码头错误: Content-Encoding=%q Vary=%q", response.Header().Get("Content-Encoding"), response.Header().Get("Vary"))
	}
	if len(server.heatmaps.ranges) != 1 || server.heatmaps.ranges[0] != heatmap.Range7D {
		t.Fatalf("默认范围 = %v", server.heatmaps.ranges)
	}

	server.heatmaps.err = heatmap.ErrInvalidRange
	invalid := request(t, server.handler, http.MethodGet, "/api/heatmap?range=year", "", false)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("非法热力图范围 = %d", invalid.Code)
	}
}

func TestPublicHeatmapEndpointNormalizesLegacyRanges(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		legacy    string
		canonical string
	}{
		{legacy: "day", canonical: heatmap.Range1D},
		{legacy: "week", canonical: heatmap.Range7D},
		{legacy: "month", canonical: heatmap.Range30D},
	} {
		t.Run(test.legacy, func(t *testing.T) {
			server := newTestServer(t, nil)
			server.heatmaps.response = heatmap.Response{Range: test.canonical}
			response := request(t, server.handler, http.MethodGet, "/api/heatmap?range="+test.legacy, "", false)
			if response.Code != http.StatusOK {
				t.Fatalf("旧范围 %q 响应 = %d: %s", test.legacy, response.Code, response.Body.String())
			}
			if len(server.heatmaps.ranges) != 1 || server.heatmaps.ranges[0] != test.canonical {
				t.Fatalf("旧范围 %q 规范化结果 = %v，期望 %q", test.legacy, server.heatmaps.ranges, test.canonical)
			}
		})
	}
}

func TestPublicHeatmapEndpointCompressesLargeResponses(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	cells := make([]heatmap.Cell, 30*24)
	for index := range cells {
		cells[index] = heatmap.Cell{
			StartTS: 1_776_000_000 + int64(index)*3600, EndTS: 1_776_003_600 + int64(index)*3600,
			Status: heatmap.StatusHealthy, Intensity: 5, CoveragePct: 100,
			ActualSamples: 60, ExpectedSamples: 60, HealthySamples: 60, UptimePct: 100,
		}
	}
	server.heatmaps.response = heatmap.Response{
		Range:    heatmap.Range30D,
		Services: []heatmap.ServiceView{{ID: "service-1", Model: "Model", Cells: cells}},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/heatmap?range=30d", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	server.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip 热力图响应 = %d, Content-Encoding=%q", recorder.Code, recorder.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(recorder.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("gzip 热力图响应缺少 Vary: %q", recorder.Header().Get("Vary"))
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var response heatmap.Response
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Services) != 1 || len(response.Services[0].Cells) != len(cells) {
		t.Fatalf("解压后的热力图响应不完整: %+v", response)
	}
	if recorder.Header().Get("Content-Length") != "" {
		t.Fatalf("gzip 响应不应保留 Content-Length: %q", recorder.Header().Get("Content-Length"))
	}
}

func TestAcceptsGzipHonorsQualityAndCasing(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"GZIP", true},
		{"br, gzip;q=0.5", true},
		{"*;q=0.5", true},
		{"br", false},
		{"gzip;q=0", false},
		{"gzip;q=2", false},
		{"gzip;q=invalid", false},
	} {
		if got := acceptsGzip(test.header); got != test.want {
			t.Errorf("acceptsGzip(%q) = %t，期望 %t", test.header, got, test.want)
		}
	}
}

func TestPublicHeatmapEndpointReadsMultipleAcceptEncodingHeaders(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	server.heatmaps.response = heatmap.Response{Range: heatmap.Range7D}
	req := httptest.NewRequest(http.MethodGet, "/api/heatmap", nil)
	req.Header.Add("Accept-Encoding", "br")
	req.Header.Add("Accept-Encoding", "GZIP;q=0.5")
	recorder := httptest.NewRecorder()
	server.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("多值 Accept-Encoding 响应 = %d, Content-Encoding=%q", recorder.Code, recorder.Header().Get("Content-Encoding"))
	}
}

func TestPublicHeatmapEndpointCompressesErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		err    error
		path   string
		status int
	}{
		{"invalid range", heatmap.ErrInvalidRange, "/api/heatmap?range=year", http.StatusBadRequest},
		{"build failure", errors.New("database unavailable"), "/api/heatmap?range=7d", http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t, nil)
			server.heatmaps.err = test.err
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			recorder := httptest.NewRecorder()
			server.handler.ServeHTTP(recorder, req)
			if recorder.Code != test.status || recorder.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("gzip 错误响应 = %d, Content-Encoding=%q", recorder.Code, recorder.Header().Get("Content-Encoding"))
			}
			reader, err := gzip.NewReader(recorder.Body)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			var body map[string]string
			if err := json.NewDecoder(reader).Decode(&body); err != nil || body["error"] == "" {
				t.Fatalf("gzip 错误响应无法解码: body=%v err=%v", body, err)
			}
		})
	}
}

func TestPublicHeatmapHeadResponseHasNoBody(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, nil)
	server.heatmaps.response = heatmap.Response{Range: heatmap.Range7D}
	httpServer := httptest.NewServer(server.handler)
	defer httpServer.Close()
	req, err := http.NewRequest(http.MethodHead, httpServer.URL+"/api/heatmap", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "gzip" || len(body) != 0 {
		t.Fatalf("HEAD 响应 = %d, Content-Encoding=%q, body=%dB", response.StatusCode, response.Header.Get("Content-Encoding"), len(body))
	}
}

func TestWebAssetsContainsEntryPages(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"index.html", "admin/index.html", "heatmap/index.html"} {
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
	if page.Title == "" || page.RefreshSec != 5 || page.EnableCommandAnimation == nil || !*page.EnableCommandAnimation || !page.ShowUptime || !page.ShowSamples || !page.ShowLatency || !page.ShowAvgLoad {
		t.Fatalf("响应不是归一化页面配置: %+v", page)
	}
	if !reflect.DeepEqual(server.repo.saved.Page, page) || !reflect.DeepEqual(server.monitor.page, page) {
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
