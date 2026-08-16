package integration_test

import (
	"net/http"
	"testing"
)

func TestStatusEndpoint(t *testing.T) {
	ts := newIntegrationServer(t)
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
