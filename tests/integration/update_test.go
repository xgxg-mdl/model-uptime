package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/settings"
	"github.com/xgxg-mdl/model-uptime/internal/update"
)

func TestUpdateEndpoints(t *testing.T) {
	called := make(chan struct{}, 1)
	updateMux := http.NewServeMux()
	updateServer := httptest.NewServer(updateMux)
	defer updateServer.Close()
	updateMux.HandleFunc("/tags.atom", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>v1.1.0</title></entry></feed>`)
	})
	updateMux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"token":"registry-token"}`)
	})
	updateMux.HandleFunc("/manifests/v1.1.0", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer registry-token" {
			t.Errorf("registry auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Docker-Content-Digest", "sha256:release")
		w.WriteHeader(http.StatusOK)
	})
	updateMux.HandleFunc("/manifests/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:release")
		w.WriteHeader(http.StatusOK)
	})
	updateMux.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer update-secret" {
			t.Errorf("update request = %s auth=%q", r.Method, r.Header.Get("Authorization"))
		}
		called <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	})
	updateMux.HandleFunc("/v1/history", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"entries":[]}`)
	})

	cfg := &settings.Config{AdminToken: testToken, Page: model.PageConfig{HistoryLen: 60}}
	updates := update.New(update.Options{
		BuildInfo:        update.BuildInfo{Version: "v1.0.0", Commit: "abc123"},
		DeploymentTag:    "latest",
		UpdateURL:        updateServer.URL + "/trigger?async=true",
		UpdateToken:      "update-secret",
		FeedURL:          updateServer.URL + "/tags.atom",
		RegistryURL:      updateServer.URL + "/manifests/",
		RegistryTokenURL: updateServer.URL + "/token",
		TriggerDelay:     time.Millisecond,
		UpdateWindow:     50 * time.Millisecond,
		HistoryPollEvery: 5 * time.Millisecond,
	})
	ts := startIntegrationServer(t, cfg, filepath.Join(t.TempDir(), "config.yaml"), updates, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := updates.Close(ctx); err != nil {
			t.Errorf("关闭更新模块: %v", err)
		}
	})

	if code, _ := doJSON(t, ts, http.MethodGet, "/api/admin/update", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", code)
	}
	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/update", testToken, nil)
	if code != http.StatusOK || out["current_version"] != "v1.0.0" || out["latest_version"] != "v1.1.0" || out["update_available"] != true {
		t.Fatalf("update status = %d %+v", code, out)
	}
	code, out = doJSON(t, ts, http.MethodPost, "/api/admin/update", testToken, nil)
	if code != http.StatusAccepted || out["target_version"] != "v1.1.0" {
		t.Fatalf("start update = %d %+v", code, out)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("updater was not called")
	}
	if code, _ := doJSON(t, ts, http.MethodPost, "/api/admin/update", testToken, nil); code != http.StatusConflict {
		t.Fatalf("duplicate update = %d", code)
	}
	time.Sleep(60 * time.Millisecond) // 等待测试更新窗口关闭后台轮询。
}

func TestUpdateEndpointWithoutUpdater(t *testing.T) {
	ts := newIntegrationServer(t)
	code, out := doJSON(t, ts, http.MethodGet, "/api/admin/update", testToken, nil)
	if code != http.StatusServiceUnavailable || out["error"] == nil {
		t.Fatalf("missing updater = %d %+v", code, out)
	}
}
