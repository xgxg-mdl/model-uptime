package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/heatmap"
	"github.com/xgxg-mdl/model-uptime/internal/httpserver"
	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/monitor"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
	"github.com/xgxg-mdl/model-uptime/internal/storage/sqlite"
)

const testToken = "test-token-123"

func boolp(b bool) *bool { return &b }

func newIntegrationServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	cfg := &settings.Config{
		AdminToken: testToken,
		Page:       model.PageConfig{HistoryLen: 60, RefreshSec: 5},
		Services: []model.Service{{
			UID: "s1", Model: "s1", Name: "svc-one", Protocol: model.ProtocolHTTP,
			BaseURL: "http://example.com", IntervalSec: 60, Enabled: boolp(true),
		}},
	}
	return startIntegrationServer(t, cfg, filepath.Join(dir, "config.yaml"), nil, nil)
}

func startIntegrationServer(
	t *testing.T,
	cfg *settings.Config,
	configPath string,
	updates httpserver.UpdateProvider,
	notifications admin.Notifications,
) *httptest.Server {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	sch := monitor.New(st, nil)
	manager, err := admin.New(admin.Options{
		Initial:       cfg,
		AdminToken:    cfg.AdminToken,
		Repository:    settings.NewFileRepository(configPath),
		Monitor:       sch,
		Notifications: notifications,
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("创建管理模块: %v", err)
	}
	snapshot := manager.Snapshot()
	if err := sch.Reload(snapshot.Services, snapshot.Page); err != nil {
		_ = st.Close()
		t.Fatalf("加载监控配置: %v", err)
	}
	heatmapService, err := heatmap.New(st, sch)
	if err != nil {
		_ = st.Close()
		t.Fatalf("创建热力图模块: %v", err)
	}
	srv, err := httpserver.New(httpserver.Options{Admin: manager, Status: sch, Heatmap: heatmapService, Updater: updates})
	if err != nil {
		_ = st.Close()
		t.Fatalf("创建 HTTP server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := sch.Stop(ctx); err != nil {
			t.Errorf("停止监控器: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Errorf("关闭 store: %v", err)
		}
	})
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
