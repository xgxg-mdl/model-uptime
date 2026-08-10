// Package prober 实现协议探针：对配置的监控目标发起真实请求并判定可用性。
//
// 判定原则：HTTP 2xx + 响应体 JSON 可解析 + 协议关键字段存在 = 可用。
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
		// 防御：未归一化或误配 0 超时会导致立即超时，回退默认 15s
		timeout = 15 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	fail := func(err error) Result {
		return Result{
			OK:        false,
			LatencyMS: time.Since(start).Milliseconds(),
			Error:     sanitize(svc, err.Error()),
		}
	}

	req, err := buildRequest(pctx, svc)
	if err != nil {
		return fail(err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fail(fmt.Errorf("请求失败: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fail(fmt.Errorf("读取响应失败: %v", err))
	}

	if svc.Protocol == model.ProtocolHTTP {
		// 通用 HTTP 探针：只看状态码与配置期望值是否一致，不解析响应体
		expect := svc.ExpectStatus
		if expect == 0 {
			expect = http.StatusOK // 未配置时默认期望 200
		}
		if resp.StatusCode != expect {
			return fail(fmt.Errorf("HTTP %d（期望 %d）: %s", resp.StatusCode, expect, previewBody(body)))
		}
		return Result{OK: true, LatencyMS: time.Since(start).Milliseconds()}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Errorf("HTTP %d: %s", resp.StatusCode, previewBody(body)))
	}
	if err := validateBody(svc.Protocol, body); err != nil {
		return fail(err)
	}
	return Result{OK: true, LatencyMS: time.Since(start).Milliseconds()}
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

// buildURL 拼接端点路径：
//   - 配置了 path 时完全覆盖默认端点；
//   - base_url 已含 /v1（如 https://api.openai.com/v1）直接拼接；
//   - 否则补上 /v1/。
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

// chatBody OpenAI Chat Completions 的最小探测体。
func chatBody(svc *model.Service) any {
	return map[string]any{
		"model": svc.Model,
		"messages": []any{
			map[string]string{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	}
}

// responseBody OpenAI Responses API 的最小探测体。
func responseBody(svc *model.Service) any {
	return map[string]any{
		"model": svc.Model,
		"input": "ping",
	}
}

// messageBody Anthropic Messages API 的最小探测体。
func messageBody(svc *model.Service) any {
	return map[string]any{
		"model":     svc.Model,
		"max_tokens": 1,
		"messages": []any{
			map[string]string{"role": "user", "content": "ping"},
		},
	}
}

// bearerHeaders 适用于 OpenAI 系协议。
func bearerHeaders(svc *model.Service) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if svc.APIKey != "" {
		h.Set("Authorization", "Bearer "+svc.APIKey)
	}
	return h
}

// anthropicHeaders 适用于 Anthropic Messages 协议（x-api-key + 版本头）。
func anthropicHeaders(svc *model.Service) http.Header {
	h := bearerHeaders(svc)
	h.Set("anthropic-version", "2023-06-01")
	if svc.APIKey != "" {
		h.Set("x-api-key", svc.APIKey)
		h.Del("Authorization") // Anthropic 用 x-api-key，不走 Bearer
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

// httpRequest 通用 HTTP 探针：method/headers/body/expect_status 全部来自配置。
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

// validateBody 判定响应体是否符合协议成功特征。
func validateBody(protocol string, body []byte) error {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("响应不是有效 JSON: %s", previewBody(body))
	}
	// API 层错误（如 401 却返回 200 的网关）同样视为失败
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

// previewBody 截断响应体用于错误消息展示。
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
