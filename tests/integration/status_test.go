package integration_test

import (
	"net/http"
	"testing"
)

var statusTimelineStates = map[string]bool{
	"healthy":     true,
	"slow":        true,
	"failing":     true,
	"probing":     true,
	"paused":      true,
	"unobserved":  true,
	"not-started": true,
}

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
	if svc["model"] != "s1" {
		t.Errorf("model = %v", svc["model"])
	}
	if svc["name"] != "svc-one" {
		t.Errorf("name = %v", svc["name"])
	}
	if _, exists := svc["id"]; exists {
		t.Errorf("公开状态不应暴露内部 uid/id: %v", svc)
	}
	if svc["uptime_pct"] == nil {
		t.Error("缺少 uptime_pct")
	}
	if got, ok := svc["interval_sec"].(float64); !ok || got != 60 {
		t.Errorf("interval_sec = %v，期望 60", svc["interval_sec"])
	}
	if got, ok := svc["warning_sec"].(float64); !ok || got != 30 {
		t.Errorf("warning_sec = %v，期望 30", svc["warning_sec"])
	}
	if _, ok := svc["last"]; !ok {
		t.Error("缺少兼容字段 last")
	}
	if _, ok := svc["history"].([]any); !ok {
		t.Errorf("history 应为数组，got %T", svc["history"])
	}
	timeline, ok := svc["timeline"].([]any)
	if !ok || len(timeline) != 60 {
		t.Fatalf("timeline 长度 = %d，期望 60；原始值 = %v", len(timeline), svc["timeline"])
	}
	for index, item := range timeline {
		slot, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("timeline[%d] = %T，期望对象", index, item)
		}
		start, startOK := slot["start_ts"].(float64)
		end, endOK := slot["end_ts"].(float64)
		status, statusOK := slot["status"].(string)
		_, countOK := slot["observation_count"].(float64)
		if !startOK || !endOK || !statusOK || !countOK {
			t.Errorf("timeline[%d] 缺少基础字段或字段类型错误: %v", index, slot)
			continue
		}
		if start >= end {
			t.Errorf("timeline[%d] 时间范围无效: start_ts=%v, end_ts=%v", index, start, end)
		}
		if !statusTimelineStates[status] {
			t.Errorf("timeline[%d].status = %q", index, status)
		}
	}
	if _, ok := out["page"]; ok {
		t.Errorf("状态数据不应混入 page 配置: %v", out["page"])
	}
	// 默认未暂停时 services[].pauses 不出现或为空。
	if _, hasPauses := svc["pauses"]; hasPauses {
		if pauses, ok := svc["pauses"].([]any); ok && len(pauses) != 0 {
			t.Errorf("未暂停时 pauses 应为空，got %v", pauses)
		}
	}
}
