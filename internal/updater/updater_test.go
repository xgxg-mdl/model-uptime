package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckSelectsLatestPublishedStableVersion(t *testing.T) {
	var manifestRequests atomic.Int32
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("/tags.atom", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
			<entry><title>v0.10.0</title></entry><entry><title>v0.9.1</title></entry>
			<entry><title>v0.11.0-rc.1</title></entry><entry><title>not-a-version</title></entry></feed>`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"token":"registry-token"}`)
	})
	mux.HandleFunc("/manifests/", func(w http.ResponseWriter, r *http.Request) {
		manifestRequests.Add(1)
		if r.Header.Get("Authorization") != "Bearer registry-token" {
			t.Errorf("registry auth = %q", r.Header.Get("Authorization"))
		}
		if strings.HasSuffix(r.URL.Path, "/v0.10.0") {
			http.NotFound(w, r) // tag 已创建，但镜像尚未发布
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:stable")
		w.WriteHeader(http.StatusOK)
	})

	svc := New(Options{
		BuildInfo:        BuildInfo{Version: "v0.9.0", Commit: "abc"},
		DeploymentTag:    "latest",
		UpdateURL:        ts.URL + "/update?async=true",
		UpdateToken:      "update-token",
		FeedURL:          ts.URL + "/tags.atom",
		RegistryURL:      ts.URL + "/manifests/",
		RegistryTokenURL: ts.URL + "/token",
	})
	status, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if status.LatestVersion != "v0.9.1" || !status.UpdateAvailable || !status.Enabled {
		t.Fatalf("unexpected status: %+v", status)
	}
	if manifestRequests.Load() != 3 {
		t.Fatalf("manifest requests = %d, want 3", manifestRequests.Load())
	}

	_, err = svc.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if manifestRequests.Load() != 3 {
		t.Fatal("cached check contacted registry")
	}
}

func TestCheckRejectsLatestDigestMismatch(t *testing.T) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("/tags.atom", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>v1.1.0</title></entry></feed>`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"token":"registry-token"}`)
	})
	mux.HandleFunc("/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/latest") {
			w.Header().Set("Docker-Content-Digest", "sha256:other")
		} else {
			w.Header().Set("Docker-Content-Digest", "sha256:stable")
		}
		w.WriteHeader(http.StatusOK)
	})
	svc := New(Options{
		BuildInfo: BuildInfo{Version: "v1.0.0"}, FeedURL: ts.URL + "/tags.atom",
		RegistryURL: ts.URL + "/manifests/", RegistryTokenURL: ts.URL + "/token",
	})
	if _, err := svc.Check(context.Background(), true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRefreshesRuntimeStateAfterRemoteRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("/tags.atom", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>v1.1.0</title></entry></feed>`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"token":"registry-token"}`)
	})
	mux.HandleFunc("/manifests/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:stable")
		w.WriteHeader(http.StatusOK)
	})
	svc := New(Options{
		BuildInfo: BuildInfo{Version: "v1.0.0"}, DeploymentTag: "latest",
		UpdateURL: ts.URL + "/update?async=true", UpdateToken: "secret",
		FeedURL: ts.URL + "/tags.atom", RegistryURL: ts.URL + "/manifests/", RegistryTokenURL: ts.URL + "/token",
	})
	result := make(chan Status, 1)
	errs := make(chan error, 1)
	go func() {
		status, err := svc.Check(context.Background(), true)
		result <- status
		errs <- err
	}()
	<-entered
	svc.mu.Lock()
	svc.updating = true
	svc.mu.Unlock()
	close(release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if status := <-result; !status.Updating {
		t.Fatalf("stale runtime state: %+v", status)
	}
}

func TestCheckReportsUnavailableSources(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer ts.Close()
	svc := New(Options{BuildInfo: BuildInfo{Version: "v1.0.0"}, FeedURL: ts.URL})
	if _, err := svc.Check(context.Background(), true); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartTriggersOnceWithAuthentication(t *testing.T) {
	var calls atomic.Int32
	var historyReady atomic.Bool
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer secret" || r.URL.Query().Get("async") != "true" {
			t.Errorf("request = %s auth=%q query=%q", r.Method, r.Header.Get("Authorization"), r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/v1/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("history auth = %q", r.Header.Get("Authorization"))
		}
		if !historyReady.Load() {
			fmt.Fprint(w, `{"entries":[]}`)
			return
		}
		fmt.Fprintf(w, `{"entries":[{"timestamp":%q,"updated":0,"failed":1}]}`, time.Now().UTC().Format(time.RFC3339Nano))
	})
	svc := New(Options{
		BuildInfo:        BuildInfo{Version: "v1.0.0"},
		DeploymentTag:    "latest",
		UpdateURL:        ts.URL + "/trigger?async=true",
		UpdateToken:      "secret",
		TriggerDelay:     time.Millisecond,
		UpdateWindow:     time.Second,
		HistoryPollEvery: time.Millisecond,
	})
	if err := svc.Start("v1.1.0"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := svc.Start("v1.1.0"); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("duplicate err = %v", err)
	}
	historyReady.Store(true)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		updating, lastErr := svc.updating, svc.lastUpdateError
		svc.mu.Unlock()
		if !updating {
			if !strings.Contains(lastErr, "failed for 1") {
				t.Fatalf("last error = %q", lastErr)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("update result was not observed")
}

func TestStartRejectsUnsafeDeploymentModes(t *testing.T) {
	tests := []struct {
		name string
		opt  Options
		want string
	}{
		{"dev build", Options{BuildInfo: BuildInfo{Version: "dev"}, DeploymentTag: "latest", UpdateURL: "http://updater", UpdateToken: "x"}, "Development builds"},
		{"fixed tag", Options{BuildInfo: BuildInfo{Version: "v1.0.0"}, DeploymentTag: "1.0.0", UpdateURL: "http://updater", UpdateToken: "x"}, "MODEL_UPTIME_TAG=latest"},
		{"missing updater", Options{BuildInfo: BuildInfo{Version: "v1.0.0"}, DeploymentTag: "latest"}, "not configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := New(tt.opt).Start("v1.1.0"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestNormalizeAndCompareVersion(t *testing.T) {
	if got := normalizeVersion(" 1.2.3 "); got != "v1.2.3" {
		t.Fatalf("normalize = %q", got)
	}
	if normalizeVersion("v1.02.3") != "" {
		t.Fatal("leading zeros should be rejected")
	}
	if normalizeVersion("v1.2.3-rc.1") != "" {
		t.Fatal("prerelease should be ignored")
	}
	if !isNewer("v2.0.0", "v1.99.99") || isNewer("v1.0.0", "v1.0.0") {
		t.Fatal("version comparison failed")
	}
}
