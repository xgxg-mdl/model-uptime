package integration_test

import (
	"net/http"
	"testing"
)

func TestPageConfig(t *testing.T) {
	ts := newIntegrationServer(t)
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
