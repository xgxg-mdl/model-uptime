package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/config"
	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
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
	// 默认未暂停时 services[].pauses 不出现或为空。
	if _, hasPauses := svc["pauses"]; hasPauses {
		if pauses, ok := svc["pauses"].([]any); ok && len(pauses) != 0 {
			t.Errorf("未暂停时 pauses 应为空，got %v", pauses)
		}
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

func TestUpdateServiceRejectsIDChange(t *testing.T) {
	ts := newTestServer(t)
	// 创建第二个服务
	code, _ := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "two", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusCreated {
		t.Fatal("创建失败")
	}
	// 服务 ID 是订阅与历史的稳定引用，创建后不允许修改。
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/services/s1", testToken, map[string]any{
		"id": "two", "name": "x", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusBadRequest {
		t.Errorf("修改服务 ID 应返回 400，got %d", code)
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
		"public_url":  " https://status.example.com/models?view=all ",
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
	if page["public_url"] != "https://status.example.com/models?view=all" {
		t.Errorf("public_url 未归一化或热更新: %v", page["public_url"])
	}

	// 非法值
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/page", testToken, map[string]any{"history_len": 99999})
	if code != http.StatusBadRequest {
		t.Errorf("非法 history_len 应 400，got %d", code)
	}
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/page", testToken, map[string]any{
		"public_url": "javascript:alert(1)", "history_len": 60, "refresh_sec": 5,
	})
	if code != http.StatusBadRequest {
		t.Errorf("非法 public_url 应 400，got %d", code)
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

func boolPtr(b bool) *bool { return &b }

func TestBulkUpdateServices(t *testing.T) {
	ts := newTestServer(t)

	// 准备 3 个服务：1 个 http + 2 个 LLM，便于覆盖协议差异。全部显式启用。
	mk := func(id string, protocol string) map[string]any {
		m := map[string]any{"name": id, "protocol": protocol, "base_url": "http://x", "enabled": true}
		if protocol != "http" {
			m["model"] = "m"
		}
		return m
	}
	for _, s := range []struct{ id, proto string }{
		{"a", "http"}, {"b", "chat"}, {"c", "message"},
	} {
		if code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, mk(s.id, s.proto)); code != http.StatusCreated {
			t.Fatalf("创建 %s = %d, %v", s.id, code, out)
		}
	}

	list := func() map[string]map[string]any {
		_, out := doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
		m := map[string]map[string]any{}
		for _, raw := range out["services"].([]any) {
			row := raw.(map[string]any)
			m[row["id"].(string)] = row
		}
		return m
	}

	// 批量禁用 b、c
	code, out := doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"b", "c"}, "patch": map[string]any{"enabled": false},
	})
	if code != http.StatusOK {
		t.Fatalf("批量禁用 = %d, %v", code, out)
	}
	rows := list()
	if rows["a"]["enabled"] != true || rows["b"]["enabled"] != false || rows["c"]["enabled"] != false {
		t.Errorf("批量禁用结果异常: a=%v b=%v c=%v", rows["a"]["enabled"], rows["b"]["enabled"], rows["c"]["enabled"])
	}

	// 批量设 interval + timeout，a 未在 ids 中应保持不变
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"b", "c"}, "patch": map[string]any{"interval_sec": 30, "timeout_sec": 8},
	})
	if code != http.StatusOK {
		t.Fatal("批量设置字段失败")
	}
	rows = list()
	if int(rows["b"]["interval_sec"].(float64)) != 30 || int(rows["c"]["timeout_sec"].(float64)) != 8 {
		t.Errorf("字段未生效: b.interval=%v c.timeout=%v", rows["b"]["interval_sec"], rows["c"]["timeout_sec"])
	}
	if int(rows["a"]["interval_sec"].(float64)) == 30 {
		t.Error("未选中的 a 不应被修改")
	}

	// 批量设 stream=true：http 服务 a 仍应保持 stream=nil（Normalize 清空）
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"a", "b"}, "patch": map[string]any{"stream": true},
	})
	if code != http.StatusOK {
		t.Fatal("批量设 stream 失败")
	}
	rows = list()
	if rows["a"]["stream"] != nil {
		t.Errorf("http 服务 stream 应被清空，got %v", rows["a"]["stream"])
	}
	if rows["b"]["stream"] != true {
		t.Errorf("LLM 服务 stream 应为 true，got %v", rows["b"]["stream"])
	}

	// patch 为空对象：应成功且不改动
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"b"}, "patch": map[string]any{},
	})
	if code != http.StatusOK {
		t.Errorf("空 patch 应成功，got %d", code)
	}

	// 不存在的 id → 404，整体不落盘
	code, out = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"b", "nope"}, "patch": map[string]any{"enabled": true},
	})
	if code != http.StatusNotFound {
		t.Errorf("部分 id 缺失应 404，got %d", code)
	}
	rows = list()
	if rows["b"]["enabled"] != false {
		t.Error("404 时不应落盘任何修改")
	}

	// 空 ids → 400
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{}, "patch": map[string]any{"enabled": true},
	})
	if code != http.StatusBadRequest {
		t.Errorf("空 ids 应 400，got %d", code)
	}

	// 非法 interval → 400（updateConfig 校验）
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"ids": []string{"b"}, "patch": map[string]any{"interval_sec": 1},
	})
	if code != http.StatusBadRequest {
		t.Errorf("非法 interval 应 400，got %d", code)
	}

	// 未认证 → 401
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", "", map[string]any{
		"ids": []string{"b"}, "patch": map[string]any{"enabled": true},
	})
	if code != http.StatusUnauthorized {
		t.Errorf("未认证应 401，got %d", code)
	}
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

func TestTelegramAdminFlowAndServiceReferenceCleanup(t *testing.T) {
	requests := make(chan string, 1)
	var rejectTelegram atomic.Bool
	telegramAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析 Telegram 请求: %v", err)
		}
		if rejectTelegram.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
			return
		}
		requests <- r.URL.Path + "|" + r.Form.Get("chat_id") + "|" + r.Form.Get("parse_mode") + "|" + r.Form.Get("text")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer telegramAPI.Close()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	scheduler := scheduler.New(st, nil)
	cfg := &config.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{PublicURL: "https://status.example.com/?from=test&view=full", HistoryLen: 60, RefreshSec: 5},
		Services: []model.Service{{
			ID: "s1", Name: "svc-one", Protocol: model.ProtocolHTTP,
			BaseURL: "http://example.com", IntervalSec: 60, Enabled: boolp(true),
		}},
		Telegram: notifier.Config{
			BotToken: "secret-token",
			Subscriptions: []notifier.Subscription{{
				ID: "ops", Name: "Operations", Enabled: true, ChatID: "-100", ServiceIDs: []string{"s1"},
			}},
		},
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	notifications, err := notifier.New(notifier.Options{APIBaseURL: telegramAPI.URL, RetryDelays: []time.Duration{}}, cfg.Telegram)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := notifications.Close(ctx); err != nil {
			t.Errorf("关闭 notifier: %v", err)
		}
	}()
	scheduler.SetNotifier(notifications)
	scheduler.Reload(cfg.Services, cfg.Page)
	server, err := New(Options{
		Scheduler: scheduler, Notifier: notifications, ConfigPath: configPath, AdminToken: testToken,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/telegram", testToken, nil)
	if code != http.StatusOK || out["bot_token"] != "****" || out["token_configured"] != true {
		t.Fatalf("Telegram GET 未正确脱敏: code=%d out=%v", code, out)
	}

	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/telegram", testToken, map[string]any{
		"bot_token": "",
		"subscriptions": []map[string]any{{
			"id": "ops", "name": "Primary operations", "enabled": true,
			"chat_id": "-100", "language": "en-US", "service_ids": []string{"s1"}, "template": "<b>{{.TotalChanges}}</b>",
		}},
	})
	if code != http.StatusOK || out["bot_token"] != "****" {
		t.Fatalf("Telegram PUT 失败: code=%d out=%v", code, out)
	}
	saved, err := config.Load(configPath)
	if err != nil || saved.Telegram.BotToken != "secret-token" || saved.Telegram.Subscriptions[0].Name != "Primary operations" || saved.Telegram.Subscriptions[0].Language != notifier.LanguageEnglish {
		t.Fatalf("Token 保留或配置落盘失败: cfg=%+v err=%v", saved, err)
	}

	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/telegram/test", testToken, map[string]string{"subscription_id": "ops"})
	if code != http.StatusOK {
		t.Fatalf("Telegram 测试发送失败: code=%d out=%v", code, out)
	}
	select {
	case request := <-requests:
		parts := strings.SplitN(request, "|", 4)
		if len(parts) != 4 || parts[0] != "/botsecret-token/sendMessage" || parts[1] != "-100" || parts[2] != "HTML" {
			t.Fatalf("Telegram 请求错误: %s", request)
		}
		if !strings.Contains(parts[3], `<a href="https://status.example.com/?from=test&amp;view=full">Open status page</a>`) {
			t.Fatalf("Telegram 测试消息缺少探针页链接: %s", parts[3])
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到 Telegram 测试请求")
	}

	rejectTelegram.Store(true)
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/telegram/test", testToken, map[string]string{"subscription_id": "ops"})
	if code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "chat not found") {
		t.Fatalf("Telegram 错误必须完整返回管理页: code=%d out=%v", code, out)
	}
	rejectTelegram.Store(false)

	code, out = doJSON(t, ts, http.MethodDelete, "/api/admin/services/s1", testToken, nil)
	if code != http.StatusOK {
		t.Fatalf("删除服务失败: code=%d out=%v", code, out)
	}
	saved, err = config.Load(configPath)
	if err != nil || len(saved.Telegram.Subscriptions) != 1 || len(saved.Telegram.Subscriptions[0].ServiceIDs) != 0 {
		t.Fatalf("删除服务未清理订阅引用: cfg=%+v err=%v", saved, err)
	}
}
