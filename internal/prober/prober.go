// Package prober 实现协议探针：对配置的监控目标发起真实请求并判定可用性。
//
// 判定原则：HTTP 2xx + 有效协议响应（同步 JSON 或流式 SSE）= 可用。
// 任何失败都会记录脱敏后的错误信息，供状态页 tooltip 展示。
package prober

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lefachao/model-uptime/internal/model"
)

// Result 是一次探测的结果。
type Result struct {
	OK        bool
	LatencyMS int64
	Error     string
}

// maxBodyRead 限制响应体读取上限，防止异常端点返回超大响应拖垮探针。
const maxBodyRead = 1 << 20 // 1 MiB

// Probe 对服务执行一次探测。ctx 用于取消，超时由 svc.TimeoutSec 控制。
func Probe(ctx context.Context, svc *model.Service) Result {
	timeout := time.Duration(svc.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	fail := func(err error) Result {
		return Result{OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: sanitize(svc, err.Error())}
	}

	req, err := buildRequest(pctx, svc)
	if err != nil {
		return fail(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fail(fmt.Errorf("请求失败: %v", err))
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body)
	if err != nil {
		return fail(err)
	}

	if svc.Protocol == model.ProtocolHTTP {
		expect := svc.ExpectStatus
		if expect == 0 {
			expect = http.StatusOK
		}
		if resp.StatusCode != expect {
			return fail(fmt.Errorf("HTTP %d（期望 %d）: %s", resp.StatusCode, expect, previewBody(body)))
		}
		return Result{OK: true, LatencyMS: time.Since(start).Milliseconds()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Errorf("HTTP %d: %s", resp.StatusCode, previewBody(body)))
	}

	if svc.IsStreaming() {
		err = validateSSE(svc.Protocol, body)
	} else {
		err = validateBody(svc.Protocol, body)
	}
	if err != nil {
		return fail(err)
	}
	return Result{OK: true, LatencyMS: time.Since(start).Milliseconds()}
}

func readBody(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, maxBodyRead+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}
	if len(b) > maxBodyRead {
		return nil, fmt.Errorf("响应体超过 %d 字节限制", maxBodyRead)
	}
	return b, nil
}

// buildRequest 按协议构造请求：LLM 协议为 JSON POST，http 协议完全由配置决定。
func buildRequest(ctx context.Context, svc *model.Service) (*http.Request, error) {
	switch svc.Protocol {
	case model.ProtocolChat:
		return jsonRequest(ctx, svc, buildURL(svc, "chat/completions"), chatBody(svc), bearerHeaders(svc))
	case model.ProtocolResponse:
		return jsonRequest(ctx, svc, buildURL(svc, "responses"), responseBody(svc), bearerHeaders(svc))
	case model.ProtocolMessage:
		return jsonRequest(ctx, svc, buildURL(svc, "messages"), messageBody(svc), anthropicHeaders(svc))
	case model.ProtocolHTTP:
		return httpRequest(ctx, svc)
	default:
		return nil, fmt.Errorf("不支持的协议: %q", svc.Protocol)
	}
}

// buildURL 拼接端点路径：path 覆盖默认端点；base_url 未含 /v1 时补上 /v1/。
func buildURL(svc *model.Service, endpoint string) string {
	base := strings.TrimRight(svc.BaseURL, "/")
	if svc.Path != "" {
		return base + "/" + strings.TrimLeft(svc.Path, "/")
	}
	if strings.HasSuffix(base, "/v1") || strings.Contains(base, "/v1/") {
		return base + "/" + endpoint
	}
	return base + "/v1/" + endpoint
}

func chatBody(svc *model.Service) any {
	body := map[string]any{"model": svc.Model, "messages": []any{map[string]string{"role": "user", "content": "ping"}}, "max_tokens": 1}
	if svc.IsStreaming() {
		body["stream"] = true
	}
	return body
}

func responseBody(svc *model.Service) any {
	body := map[string]any{"model": svc.Model, "input": "ping"}
	if svc.IsStreaming() {
		body["stream"] = true
	}
	return body
}

func messageBody(svc *model.Service) any {
	body := map[string]any{"model": svc.Model, "max_tokens": 1, "messages": []any{map[string]string{"role": "user", "content": "ping"}}}
	if svc.IsStreaming() {
		body["stream"] = true
	}
	return body
}

func bearerHeaders(svc *model.Service) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if svc.APIKey != "" {
		h.Set("Authorization", "Bearer "+svc.APIKey)
	}
	return h
}

func anthropicHeaders(svc *model.Service) http.Header {
	h := bearerHeaders(svc)
	h.Set("anthropic-version", "2023-06-01")
	if svc.APIKey != "" {
		h.Set("x-api-key", svc.APIKey)
		h.Del("Authorization")
	}
	return h
}

func jsonRequest(ctx context.Context, svc *model.Service, url string, body any, h http.Header) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("构造请求体失败: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %v", err)
	}
	for k, vs := range h {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

func httpRequest(ctx context.Context, svc *model.Service) (*http.Request, error) {
	method := svc.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if svc.Body != "" {
		body = strings.NewReader(svc.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, svc.BaseURL, body)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %v", err)
	}
	for k, v := range svc.Headers {
		req.Header.Set(k, v)
	}
	if svc.APIKey != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+svc.APIKey)
	}
	return req, nil
}

// validateBody 判定同步响应体是否符合协议成功特征。
func validateBody(protocol string, body []byte) error {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("响应不是有效 JSON: %s", previewBody(body))
	}
	if e, ok := m["error"]; ok && e != nil {
		return fmt.Errorf("API 错误: %s", previewBody([]byte(fmt.Sprint(e))))
	}
	switch protocol {
	case model.ProtocolChat:
		if _, ok := m["choices"]; !ok {
			return errors.New("响应缺少 choices 字段")
		}
	case model.ProtocolResponse:
		if _, ok := m["output"]; !ok {
			return errors.New("响应缺少 output 字段")
		}
	case model.ProtocolMessage:
		if _, ok := m["content"]; !ok {
			return errors.New("响应缺少 content 字段")
		}
	}
	return nil
}

// validateSSE 解析完整 SSE 帧。探针只在看见一个能证明模型流已建立的协议事件后成功，
// 仍继续解析剩余帧以避免错误事件被首个成功事件掩盖。
func validateSSE(protocol string, body []byte) error {
	var valid bool
	for _, frame := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n") {
		var event string
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		if payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			return fmt.Errorf("流式事件不是有效 JSON: %s", previewBody([]byte(payload)))
		}
		e, hasError := m["error"]
		if (hasError && e != nil) || event == "error" || m["type"] == "error" {
			return fmt.Errorf("流式 API 错误: %s", previewBody([]byte(payload)))
		}
		if streamEventValid(protocol, m) {
			valid = true
		}
	}
	if !valid {
		return errors.New("流式响应未包含有效协议事件")
	}
	return nil
}

func streamEventValid(protocol string, m map[string]any) bool {
	switch protocol {
	case model.ProtocolChat:
		_, ok := m["choices"]
		return ok
	case model.ProtocolResponse:
		typ, _ := m["type"].(string)
		return strings.HasPrefix(typ, "response.")
	case model.ProtocolMessage:
		typ, _ := m["type"].(string)
		return typ == "message_start" || typ == "content_block_start" || typ == "content_block_delta"
	default:
		return false
	}
}

func previewBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// sanitize 确保错误消息不泄露 API key（可能出现在 URL、代理错误等场景）。
func sanitize(svc *model.Service, msg string) string {
	if svc.APIKey != "" {
		msg = strings.ReplaceAll(msg, svc.APIKey, "***")
	}
	return msg
}
