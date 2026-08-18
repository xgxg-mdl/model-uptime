package integration_test

import (
	"net/http"
	"testing"
)

func TestPublicHeatmapEndpoint(t *testing.T) {
	ts := newIntegrationServer(t)
	code, output := doJSON(t, ts, http.MethodGet, "/api/heatmap?range=month", "", nil)
	if code != http.StatusOK {
		t.Fatalf("heatmap status = %d: %v", code, output)
	}
	if output["range"] != "month" || output["timezone"] != "Asia/Shanghai" {
		t.Fatalf("热力图范围或时区错误: %v", output)
	}
	rows, ok := output["rows"].([]any)
	if !ok || len(rows) != 30 {
		t.Fatalf("月视图行数 = %v", output["rows"])
	}
	services, ok := output["services"].([]any)
	if !ok || len(services) != 1 {
		t.Fatalf("热力图服务 = %v", output["services"])
	}
	service := services[0].(map[string]any)
	cells, ok := service["cells"].([]any)
	if !ok || len(cells) != 720 {
		t.Fatalf("月视图单元格数 = %v", service["cells"])
	}

	invalidCode, _ := doJSON(t, ts, http.MethodGet, "/api/heatmap?range=year", "", nil)
	if invalidCode != http.StatusBadRequest {
		t.Fatalf("非法范围状态码 = %d", invalidCode)
	}
}
