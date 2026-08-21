package integration_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
)

func TestServiceCRUD(t *testing.T) {
	ts := newIntegrationServer(t)

	// 创建
	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "New SVC", "model": "new-model", "protocol": "http", "base_url": "https://api.example.com",
		"api_key": "sk-secret-key-12345",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 = %d, %v", code, out)
	}
	svc := out["service"].(map[string]any)
	newUID := svc["uid"].(string)
	if newUID == "" || svc["model"] != "new-model" {
		t.Errorf("服务身份异常: %v", svc)
	}
	if _, exists := svc["id"]; exists {
		t.Errorf("响应不应包含旧 id: %v", svc)
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
	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/services/"+newUID, testToken, map[string]any{
		"name": "New SVC v2", "model": "new-model-v2", "protocol": "http", "base_url": "https://api2.example.com",
		"api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("更新 = %d", code)
	}
	if updatedService := out["service"].(map[string]any); updatedService["uid"] != newUID {
		t.Fatalf("修改 model 后 uid 发生变化: %v", updatedService)
	}
	// 验证配置落盘后密钥保留（通过 test 端点? 直接读配置文件太绕，改读 list 的脱敏值非空即可）
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs = out["services"].([]any)
	updated := svcs[1].(map[string]any)
	if updated["name"] != "New SVC v2" {
		t.Errorf("更新未生效: %v", updated["name"])
	}
	if updated["model"] != "new-model-v2" || updated["uid"] != newUID {
		t.Errorf("model 更新或 uid 保持失败: %v", updated)
	}
	if updated["api_key"].(string) == "" {
		t.Error("留空更新后密钥应被保留（脱敏值非空）")
	}

	// 删除
	code, _ = doJSON(t, ts, http.MethodDelete, "/api/admin/services/"+newUID, testToken, nil)
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
}

func TestUpdateServiceAllowsModelChangeAndRejectsDuplicates(t *testing.T) {
	ts := newIntegrationServer(t)
	// 创建第二个服务
	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "two", "model": "two", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusCreated {
		t.Fatal("创建失败")
	}
	uid := out["service"].(map[string]any)["uid"].(string)
	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/services/"+uid, testToken, map[string]any{
		"model": "two-renamed", "name": "x", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusOK || out["service"].(map[string]any)["uid"] != uid {
		t.Fatalf("修改 model 应保留 uid: code=%d out=%v", code, out)
	}
	code, _ = doJSON(t, ts, http.MethodPut, "/api/admin/services/"+uid, testToken, map[string]any{
		"model": "s1", "name": "x", "protocol": "http", "base_url": "http://x",
	})
	if code != http.StatusConflict {
		t.Errorf("重复 model 应返回 409，got %d", code)
	}
}

func TestDuplicateService(t *testing.T) {
	ts := newIntegrationServer(t)

	// 先创建一个带密钥与 headers 的 http 服务，便于校验深拷贝
	code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, map[string]any{
		"name": "orig", "model": "orig", "protocol": "http", "base_url": "http://example.com",
		"api_key": "sk-orig-secret-12345",
		"headers": map[string]string{"X-Custom": "v1"},
		"method":  "GET", "expect_status": 200,
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 = %d, %v", code, out)
	}
	originalUID := out["service"].(map[string]any)["uid"].(string)

	// 第一次复制：得到 orig-copy
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/services/"+originalUID+"/duplicate", testToken, nil)
	if code != http.StatusCreated {
		t.Fatalf("复制 = %d, %v", code, out)
	}
	dup := out["service"].(map[string]any)
	if dup["model"] != "orig-copy" || dup["uid"] == originalUID {
		t.Errorf("复制身份异常: %v", dup)
	}
	if dup["name"] != "orig (copy)" {
		t.Errorf("复制 name = %q", dup["name"])
	}
	// 返回脱敏，非空且不泄露明文
	if ak, _ := dup["api_key"].(string); ak == "" || ak == "sk-orig-secret-12345" {
		t.Errorf("复制响应密钥脱敏异常: %q", ak)
	}

	// 第二次复制：orig-copy 已占用，应得到 orig-copy2
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/services/"+originalUID+"/duplicate", testToken, nil)
	if code != http.StatusCreated {
		t.Fatalf("二次复制 = %d, %v", code, out)
	}
	dup2 := out["service"].(map[string]any)
	if dup2["model"] != "orig-copy2" {
		t.Errorf("二次复制 model = %q, want orig-copy2", dup2["model"])
	}

	// 列表里应有 4 条（s1 + orig + orig-copy + orig-copy2）
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs := out["services"].([]any)
	if len(svcs) != 4 {
		t.Fatalf("复制后服务数 = %d, want 4", len(svcs))
	}

	// 编辑复制出来的服务并保存（留空 api_key），应保留复制的密钥。
	duplicateUID := dup["uid"].(string)
	code, out = doJSON(t, ts, http.MethodPut, "/api/admin/services/"+duplicateUID, testToken, map[string]any{
		"model": "orig-copy", "name": "orig (copy) v2", "protocol": "http",
		"base_url": "http://example.com", "api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("编辑复制服务 = %d, %v", code, out)
	}
	code, out = doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
	svcs = out["services"].([]any)
	for _, s := range svcs {
		row := s.(map[string]any)
		if row["uid"] == duplicateUID {
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
	code, _ = doJSON(t, ts, http.MethodPost, "/api/admin/services/"+originalUID+"/duplicate", "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("未认证复制应 401, got %d", code)
	}
}

func TestBulkUpdateServices(t *testing.T) {
	ts := newIntegrationServer(t)

	// 准备 3 个服务：1 个 http + 2 个 LLM，便于覆盖协议差异。全部显式启用。
	mk := func(serviceModel string, protocol string) map[string]any {
		return map[string]any{
			"name": serviceModel, "model": serviceModel, "protocol": protocol,
			"base_url": "http://x", "enabled": true,
		}
	}
	uids := make(map[string]string, 3)
	for _, s := range []struct{ id, proto string }{
		{"a", "http"}, {"b", "chat"}, {"c", "message"},
	} {
		if code, out := doJSON(t, ts, http.MethodPost, "/api/admin/services", testToken, mk(s.id, s.proto)); code != http.StatusCreated {
			t.Fatalf("创建 %s = %d, %v", s.id, code, out)
		} else {
			uids[s.id] = out["service"].(map[string]any)["uid"].(string)
		}
	}

	list := func() map[string]map[string]any {
		_, out := doJSON(t, ts, http.MethodGet, "/api/admin/services", testToken, nil)
		m := map[string]map[string]any{}
		for _, raw := range out["services"].([]any) {
			row := raw.(map[string]any)
			m[row["model"].(string)] = row
		}
		return m
	}

	// 批量禁用 b、c
	code, out := doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["b"], uids["c"]}, "patch": map[string]any{"enabled": false},
	})
	if code != http.StatusOK {
		t.Fatalf("批量禁用 = %d, %v", code, out)
	}
	rows := list()
	if rows["a"]["enabled"] != true || rows["b"]["enabled"] != false || rows["c"]["enabled"] != false {
		t.Errorf("批量禁用结果异常: a=%v b=%v c=%v", rows["a"]["enabled"], rows["b"]["enabled"], rows["c"]["enabled"])
	}

	// 批量设 interval + timeout + warning，a 未在 uids 中应保持不变
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["b"], uids["c"]}, "patch": map[string]any{"interval_sec": 30, "timeout_sec": 8, "warning_sec": 6},
	})
	if code != http.StatusOK {
		t.Fatal("批量设置字段失败")
	}
	rows = list()
	if int(rows["b"]["interval_sec"].(float64)) != 30 || int(rows["c"]["timeout_sec"].(float64)) != 8 || int(rows["b"]["warning_sec"].(float64)) != 6 {
		t.Errorf("字段未生效: b.interval=%v c.timeout=%v b.warning=%v", rows["b"]["interval_sec"], rows["c"]["timeout_sec"], rows["b"]["warning_sec"])
	}
	if int(rows["a"]["interval_sec"].(float64)) == 30 {
		t.Error("未选中的 a 不应被修改")
	}

	// 批量设 stream=true：http 服务 a 仍应保持 stream=nil（Normalize 清空）
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["a"], uids["b"]}, "patch": map[string]any{"stream": true},
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
		"uids": []string{uids["b"]}, "patch": map[string]any{},
	})
	if code != http.StatusOK {
		t.Errorf("空 patch 应成功，got %d", code)
	}

	// 不存在的 id → 404，整体不落盘
	code, out = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["b"], "nope"}, "patch": map[string]any{"enabled": true},
	})
	if code != http.StatusNotFound {
		t.Errorf("部分 id 缺失应 404，got %d", code)
	}
	rows = list()
	if rows["b"]["enabled"] != false {
		t.Error("404 时不应落盘任何修改")
	}

	// 空 uids → 400
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{}, "patch": map[string]any{"enabled": true},
	})
	if code != http.StatusBadRequest {
		t.Errorf("空 uids 应 400，got %d", code)
	}

	// 非法 interval → 400（updateConfig 校验）
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["b"]}, "patch": map[string]any{"interval_sec": 1},
	})
	if code != http.StatusBadRequest {
		t.Errorf("非法 interval 应 400，got %d", code)
	}

	// 非法 warning → 400（updateConfig 校验）
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", testToken, map[string]any{
		"uids": []string{uids["b"]}, "patch": map[string]any{"warning_sec": 301},
	})
	if code != http.StatusBadRequest {
		t.Errorf("非法 warning 应 400，got %d", code)
	}

	// 未认证 → 401
	code, _ = doJSON(t, ts, http.MethodPatch, "/api/admin/services", "", map[string]any{
		"uids": []string{uids["b"]}, "patch": map[string]any{"enabled": true},
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

	cfg := &settings.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{HistoryLen: 60},
		Services: []model.Service{{
			UID: "s1", Model: "s1", Name: "s1", Protocol: model.ProtocolHTTP,
			BaseURL: up.URL, Enabled: boolp(true),
		}},
	}
	ts := startIntegrationServer(t, cfg, filepath.Join(t.TempDir(), "config.yaml"), nil, nil)

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
