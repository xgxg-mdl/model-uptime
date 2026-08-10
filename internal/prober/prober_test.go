package prober

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
)

func boolp(b bool) *bool { return &b }

// newSvc 构造一个可探测的服务。
func newSvc(id, protocol, baseURL string) *model.Service {
	return &model.Service{
		ID: id, Name: id, Protocol: protocol, Model: "test-model",
		BaseURL: baseURL, APIKey: "sk-test-key",
		IntervalSec: 60, TimeoutSec: 5, Enabled: boolp(true),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("写入响应失败: %v", err)
	}
}

func TestChatProtocol(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "test-model" {
			t.Errorf("请求体 model = %v", body["model"])
		}
		writeJSON(t, w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}}})
	}))
	defer srv.Close()

	res := Probe(context.Background(), newSvc("s1", model.ProtocolChat, srv.URL))
	if !res.OK {
		t.Fatalf("chat 应成功: %+v", res)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("请求路径 = %q，期望 /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if res.LatencyMS <= 0 {
		t.Errorf("应有耗时: %d", res.LatencyMS)
	}
}

func TestResponseProtocol(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(t, w, map[string]any{
			"id":     "resp_123",
			"output": []any{map[string]any{"type": "message", "content": []any{}}},
		})
	}))
	defer srv.Close()

	res := Probe(context.Background(), newSvc("s1", model.ProtocolResponse, srv.URL))
	if !res.OK {
		t.Fatalf("response 应成功: %+v", res)
	}
	if gotPath != "/v1/responses" {
		t.Errorf("请求路径 = %q", gotPath)
	}
}

func TestMessageProtocolHeaders(t *testing.T) {
	var gotPath, gotKey, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotVer = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		if r.Header.Get("Authorization") != "" {
			t.Error("Anthropic 不应携带 Bearer Authorization")
		}
		writeJSON(t, w, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
	}))
	defer srv.Close()

	res := Probe(context.Background(), newSvc("s1", model.ProtocolMessage, srv.URL))
	if !res.OK {
		t.Fatalf("message 应成功: %+v", res)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("请求路径 = %q", gotPath)
	}
	if gotKey != "sk-test-key" || gotVer != "2023-06-01" {
		t.Errorf("x-api-key=%q anthropic-version=%q", gotKey, gotVer)
	}
}

func TestHTTPProtocol(t *testing.T) {
	var gotPath, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCustom = r.URL.Path, r.Header.Get("X-Custom")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	svc := newSvc("s1", model.ProtocolHTTP, srv.URL+"/health")
	svc.Method = "POST"
	svc.Headers = map[string]string{"X-Custom": "v1"}
	svc.Body = `{"a":1}`

	res := Probe(context.Background(), svc)
	if !res.OK {
		t.Fatalf("http 应成功: %+v", res)
	}
	if gotPath != "/health" || gotCustom != "v1" {
		t.Errorf("path=%q custom=%q", gotPath, gotCustom)
	}
}

func TestHTTPProtocolUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	svc := newSvc("s1", model.ProtocolHTTP, srv.URL)
	svc.ExpectStatus = 200
	res := Probe(context.Background(), svc)
	if res.OK {
		t.Fatal("404 应判定失败")
	}
	if !strings.Contains(res.Error, "404") {
		t.Errorf("错误应包含状态码: %q", res.Error)
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name string
		hdl  http.HandlerFunc
		svc  *model.Service
		want string
	}{
		{"HTTP 500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
			newSvc("s", model.ProtocolChat, ""), "HTTP 500"},
		{"非 JSON 响应", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("gateway error")) },
			newSvc("s", model.ProtocolChat, ""), "不是有效 JSON"},
		{"缺 choices", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, map[string]any{"foo": 1}) },
			newSvc("s", model.ProtocolChat, ""), "choices"},
		{"API 层错误字段", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, map[string]any{"error": "rate limited"}) },
			newSvc("s", model.ProtocolChat, ""), "API 错误"},
		{"缺 output", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, map[string]any{"id": "x"}) },
			newSvc("s", model.ProtocolResponse, ""), "output"},
		{"缺 content", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, map[string]any{"stop_reason": "x"}) },
			newSvc("s", model.ProtocolMessage, ""), "content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.hdl)
			defer srv.Close()
			tc.svc.BaseURL = srv.URL
			res := Probe(context.Background(), tc.svc)
			if res.OK {
				t.Fatalf("%s: 应失败", tc.name)
			}
			if !strings.Contains(res.Error, tc.want) {
				t.Errorf("%s: 错误 %q 应包含 %q", tc.name, res.Error, tc.want)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	svc := newSvc("s1", model.ProtocolHTTP, srv.URL)
	svc.TimeoutSec = 1
	start := time.Now()
	res := Probe(context.Background(), svc)
	if res.OK {
		t.Fatal("超时应判定失败")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("超时控制失效，耗时 %v", elapsed)
	}
}

func TestPathOverride(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		writeJSON(t, w, map[string]any{"choices": []any{}})
	}))
	defer srv.Close()

	// path 完全覆盖默认端点
	svc := newSvc("s1", model.ProtocolChat, srv.URL)
	svc.Path = "custom/endpoint"
	res := Probe(context.Background(), svc)
	if !res.OK {
		t.Fatal(res.Error)
	}
	if got != "/custom/endpoint" {
		t.Errorf("path 覆盖后 = %q", got)
	}
}

func TestBaseURLWithV1NoDoublePrefix(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		writeJSON(t, w, map[string]any{"choices": []any{}})
	}))
	defer srv.Close()

	svc := newSvc("s1", model.ProtocolChat, srv.URL+"/v1")
	res := Probe(context.Background(), svc)
	if !res.OK {
		t.Fatal(res.Error)
	}
	if got != "/v1/chat/completions" {
		t.Errorf("base 含 /v1 时路径 = %q", got)
	}
}

func TestErrorDoesNotLeakAPIKey(t *testing.T) {
	// 连接失败时，错误若带 key 需被脱敏
	svc := newSvc("s1", model.ProtocolChat, "http://127.0.0.1:1/v1")
	svc.APIKey = "super-secret-key"
	res := Probe(context.Background(), svc)
	if res.OK {
		t.Fatal("应失败")
	}
	if strings.Contains(res.Error, "super-secret-key") {
		t.Errorf("错误消息泄露了 API key: %q", res.Error)
	}
}
