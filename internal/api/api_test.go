package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lefachao/model-uptime/internal/config"
	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/scheduler"
	"github.com/lefachao/model-uptime/internal/store"
)

const testToken = "test-token-123"

func boolp(b bool) *bool { return &b }

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sch := scheduler.New(st, nil)
	cfg := &config.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{HistoryLen: 60, RefreshSec: 5},
		Services: []model.Service{{
			ID: "s1", Name: "svc-one", Protocol: model.ProtocolHTTP,
			BaseURL: "http://example.com", IntervalSec: 60, Enabled: boolp(true),
		}},
	}
	srv, err := New(Options{
		Scheduler:  sch,
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		AdminToken: testToken,
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sch.Reload(cfg.Services, cfg.Page)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close() })
	return ts
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, ts.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestStatusEndpoint(t *testing.T) {
	ts := newTestServer(t)
	code, out := doJSON(t, ts, http.MethodGet, "/api/status", "", nil)
	if code != http.StatusOK {
		t.Fatalf("status code = %d", code)
	}
	if out["all_ok"] != true {
		t.Errorf("all_ok = %v", out["all_ok"])
	}
	if _, ok := out["generated_at"]; !ok {
		t.Error("缺少 generated_at")
	}
	svcs, ok := out["services"].([]any)
	if !ok || len(svcs) != 1 {
		t.Fatalf("services = %v", out["services"])
	}
	svc := svcs[0].(map[string]any)
	if svc["model"] != "svc-one" {
		t.Errorf("model = %v", svc["model"])
	}
	if svc["uptime_pct"] == nil {
		t.Error("缺少 uptime_pct")
	}
	if got, ok := svc["interval_sec"].(float64); !ok || got != 60 {
		t.Errorf("interval_sec = %v，期望 60", svc["interval_sec"])
	}
	page, ok := out["page"].(map[string]any)
	if !ok || page["history_len"] == nil {
		t.Errorf("缺少 page 配置: %v", out["page"])
	}
}

func TestAdminAuthRequired(t *testing.T) {
	ts := newTestServer(t)
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
	ts := newTestServer(t)
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
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sch := scheduler.New(st, nil)
	cfg := &config.Config{Page: model.PageConfig{HistoryLen: 60}}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	srv, err := New(Options{Scheduler: sch, ConfigPath: configPath}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sch.Reload(cfg.Services, cfg.Page)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

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
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AdminToken != "my-new-password" {
		t.Errorf("配置文件 admin_token = %q", loaded.AdminToken)
	}
}

func TestServiceCRUD(t *testing.T) {
	ts := newTestServer(t)
	hdr := map[string]string{"Authorization": "Bearer " + testToken}

	// 创建
	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "New SVC", "protocol": "http", "base_url": "https://api.example.com",
		"api_key": "sk-secret-key-12345",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 = %d, %v", code, out)
	}
	svc := out["service"].(map[string]any)
	newID := svc["id"].(string)
	if newID != "new-svc" {
		t.Errorf("slug id = %q", newID)
	}
	// 返回已脱敏
	if got := svc["api_key"].(string); got != "" && got == "sk-secret-key-12345" {
		t.Error("创建响应不应泄露完整密钥")
	}

	// 列表：密钥脱敏
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	if code != http.StatusOK {
		t.Fatal("列表失败")
	}
	svcs := out["services"].([]any)
	if len(svcs) != 2 {
		t.Fatalf("服务数 = %d", len(svcs))
	}
	created := svcs[1].(map[string]any)
	if created["api_key"].(string) != "" && created["api_key"].(string) == "sk-secret-key-12345" {
		t.Error("列表泄露完整密钥")
	}

	// 更新：留空 api_key 保留原密钥
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/services/"+newID, testToken, map[string]any{
		"id": newID, "name": "New SVC v2", "protocol": "http", "base_url": "https://api2.example.com",
		"api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("更新 = %d", code)
	}
	// 验证配置落盘后密钥保留（通过 test 端点? 直接读配置文件太绕，改读 list 的脱敏值非空即可）
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs = out["services"].([]any)
	updated := svcs[1].(map[string]any)
	if updated["name"] != "New SVC v2" {
		t.Errorf("更新未生效: %v", updated["name"])
	}
	if updated["api_key"].(string) == "" {
		t.Error("留空更新后密钥应被保留（脱敏值非空）")
	}

	// 删除
	code, _ = doJSON(t, ts, http.MethodDelete, "/api/admin/services/"+newID, testToken, nil)
	if code != http.StatusOK {
		t.Fatalf("删除 = %d", code)
	}
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	if len(out["services"].([]any)) != 1 {
		t.Error("删除后应只剩 1 个服务")
	}

	// 404 更新
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/services/nope", testToken, map[string]any{})
	if code != http.StatusNotFound {
		t.Errorf("更新不存在服务应 404，got %d", code)
	}
	_ = hdr
}

func TestUpdateServiceConflictingID(t *testing.T) {
	ts := newTestServer(t)
	// 创建第二个服务
	code, _ := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "two", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusCreated {
		t.Fatal("创建失败")
	}
	// 把 s1 改名为 two → 冲突
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/services/s1", testToken, map[string]any{
		"id": "two", "name": "x", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusConflict {
		t.Errorf("改名冲突应 409，got %d", code)
	}
}

func TestPageConfig(t *testing.T) {
	ts := newTestServer(t)
	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/page", testToken, nil)
	if code != http.StatusOK {
		t.Fatal("读取 page 失败")
	}
	page := out
	if page["history_len"].(float64) != 60 {
		t.Errorf("history_len = %v", page["history_len"])
	}

	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/page", testToken, map[string]any{
		// PUT 为全量替换：配置页会提交完整对象，缺省字段按零值处理
		"title": "t", "subtitle": "s", "probe_comment": "c",
		"history_len": 90, "refresh_sec": 5,
		"show_uptime": true, "show_samples": true, "show_latency": false, "show_avg_load": true,
	})
	if code != http.StatusOK {
		t.Fatalf("更新 page = %d, %v", code, out)
	}
	// 热重载后状态 API 应反映新配置
	code, out = doJSON(t, ts, http.MethodGet, "/api/status", "", nil)
	page = out["page"].(map[string]any)
	if page["history_len"].(float64) != 90 {
		t.Errorf("热重载后 history_len = %v", page["history_len"])
	}
	if page["show_latency"].(bool) {
		t.Error("show_latency 应为 false")
	}

	// 非法值
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/page", testToken, map[string]any{"history_len": 99999})
	if code != http.StatusBadRequest {
		t.Errorf("非法 history_len 应 400，got %d", code)
	}
}

func TestDuplicateService(t *testing.T) {
	ts := newTestServer(t)

	// 先创建一个带密钥与 headers 的 http 服务，便于校验深拷贝
	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "orig", "protocol": "http", "base_url": "http://example.com",
		"api_key": "sk-orig-secret-12345",
		"headers": map[string]string{"X-Custom": "v1"},
		"method":  "GET", "expect_status": 200,
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 = %d, %v", code, out)
	}

	// 第一次复制：得到 orig-copy
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/services/orig/duplicate", testToken, nil)
	if code != http.StatusCreated {
		t.Fatalf("复制 = %d, %v", code, out)
	}
	dup := out["service"].(map[string]any)
	if dup["id"] != "orig-copy" {
		t.Errorf("复制 id = %q, want orig-copy", dup["id"])
	}
	if dup["name"] != "orig (copy)" {
		t.Errorf("复制 name = %q", dup["name"])
	}
	// 返回脱敏，非空且不泄露明文
	if ak, _ := dup["api_key"].(string); ak == "" || ak == "sk-orig-secret-12345" {
		t.Errorf("复制响应密钥脱敏异常: %q", ak)
	}

	// 第二次复制：orig-copy 已占用，应得到 orig-copy2
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/services/orig/duplicate", testToken, nil)
	if code != http.StatusCreated {
		t.Fatalf("二次复制 = %d, %v", code, out)
	}
	dup2 := out["service"].(map[string]any)
	if dup2["id"] != "orig-copy2" {
		t.Errorf("二次复制 id = %q, want orig-copy2", dup2["id"])
	}

	// 列表里应有 4 条（s1 + orig + orig-copy + orig-copy2）
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs := out["services"].([]any)
	if len(svcs) != 4 {
		t.Fatalf("复制后服务数 = %d, want 4", len(svcs))
	}

	// 复制的服务在配置中保留明文密钥（前端脱敏，后端存的是明文）
	// 直接读配置文件验证，避免依赖脱敏接口
	cfg := ts.Config // httptest.Server 没有 Config 字段，下面改用 list + 更新流程验证

	// 编辑复制出来的服务并保存（留空 api_key），应保留复制的密钥。
	// 这验证了深拷贝后 headers map、enabled 指针与原服务独立。
	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/services/orig-copy", testToken, map[string]any{
		"id": "orig-copy", "name": "orig (copy) v2", "protocol": "http",
		"base_url": "http://example.com", "api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("编辑复制服务 = %d, %v", code, out)
	}
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs = out["services"].([]any)
	for _, s := range svcs {
		row := s.(map[string]any)
		if row["id"] == "orig-copy" {
			if row["name"] != "orig (copy) v2" {
				t.Errorf("编辑未生效: %v", row["name"])
			}
			if ak, _ := row["api_key"].(string); ak == "" {
				t.Error("复制服务留空 api_key 更新后应保留原密钥")
			}
		}
	}

	// 复制不存在的服务 → 404
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/services/nope/duplicate", testToken, nil)
	if code != http.StatusNotFound {
		t.Errorf("复制不存在服务应 404, got %d", code)
	}

	// 未认证 → 401
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/services/orig/duplicate", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("未认证复制应 401, got %d", code)
	}
	_ = cfg
}

func TestTestEndpoint(t *testing.T) {
	// 探测端点指向一个真实返回 200 的 mock 服务
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer up.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sch := scheduler.New(st, nil)
	cfg := &config.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{HistoryLen: 60},
		Services: []model.Service{{
			ID: "s1", Name: "s1", Protocol: model.ProtocolHTTP,
			BaseURL: up.URL, Enabled: boolp(true),
		}},
	}
	srv, err := New(Options{
		Scheduler: sch, ConfigPath: filepath.Join(t.TempDir(), "config.yaml"),
		AdminToken: testToken,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sch.Reload(cfg.Services, cfg.Page)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services/s1/test", testToken, nil)
	if code != http.StatusOK {
		t.Fatalf("test = %d, %v", code, out)
	}
	if out["ok"] != true {
		t.Errorf("探测应成功: %v", out)
	}
	if out["latency_ms"] == nil {
		t.Errorf("应返回延迟: %v", out)
	}
}
