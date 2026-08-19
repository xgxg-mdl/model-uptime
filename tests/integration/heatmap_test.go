package integration_test

import (
	"net/http"
	"testing"
)

func TestPublicHeatmapEndpoint(t *testing.T) {
	ts := newIntegrationServer(t)
	code, output := doJSON(t, ts, http.MethodGet, "/api/heatmap?range=30d", "", nil)
	if code != http.StatusOK {
		t.Fatalf("heatmap status = %d: %v", code, output)
	}
	if output["range"] != "30d" || output["timezone"] != "Asia/Shanghai" {
		t.Fatalf("热力图范围或时区错误: %v", output)
	}
	if _, ok := output["page"]; ok {
		t.Fatalf("热力图数据不应混入 page 配置: %v", output["page"])
	}
	rows, ok := output["rows"].([]any)
	if !ok || len(rows) != 30 {
		t.Fatalf("30d 视图行数 = %v", output["rows"])
	}
	services, ok := output["services"].([]any)
	if !ok || len(services) != 1 {
		t.Fatalf("热力图服务 = %v", output["services"])
	}
	service := services[0].(map[string]any)
	cells, ok := service["cells"].([]any)
	if !ok || len(cells) != 30*24 {
		t.Fatalf("30d 视图单元格数 = %v", service["cells"])
	}

	for _, test := range []struct {
		rangeName string
		rows      int
		cells     int
		bucketSec float64
	}{
		{"1d", 4, 96, 900},
		{"7d", 7, 168, 3600},
		{"30d", 30, 720, 3600},
	} {
		code, rolling := doJSON(t, ts, http.MethodGet, "/api/heatmap?range="+test.rangeName, "", nil)
		if code != http.StatusOK || rolling["range"] != test.rangeName {
			t.Fatalf("%s 热力图响应 = %d %v", test.rangeName, code, rolling)
		}
		rollingRows, rowsOK := rolling["rows"].([]any)
		rollingServices, servicesOK := rolling["services"].([]any)
		if !rowsOK || !servicesOK || len(rollingServices) != 1 || len(rollingRows) != test.rows || rolling["bucket_sec"] != test.bucketSec {
			t.Fatalf("%s 热力图尺寸错误: %v", test.rangeName, rolling)
		}
		rollingCells, cellsOK := rollingServices[0].(map[string]any)["cells"].([]any)
		if !cellsOK || len(rollingCells) != test.cells {
			t.Fatalf("%s 热力图格数 = %v", test.rangeName, rollingServices[0])
		}
	}

	for _, test := range []struct {
		legacy    string
		canonical string
	}{
		{legacy: "day", canonical: "1d"},
		{legacy: "week", canonical: "7d"},
		{legacy: "month", canonical: "30d"},
	} {
		legacyCode, legacy := doJSON(t, ts, http.MethodGet, "/api/heatmap?range="+test.legacy, "", nil)
		if legacyCode != http.StatusOK || legacy["range"] != test.canonical {
			t.Fatalf("旧范围 %q 响应 = %d %v", test.legacy, legacyCode, legacy)
		}
	}

	invalidCode, _ := doJSON(t, ts, http.MethodGet, "/api/heatmap?range=year", "", nil)
	if invalidCode != http.StatusBadRequest {
		t.Fatalf("非法范围 year 状态码 = %d", invalidCode)
	}
}
