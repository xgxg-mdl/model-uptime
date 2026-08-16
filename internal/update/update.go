// Package update 检查已发布版本，并通过隔离的容器更新器触发部署更新。
package update

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultFeedURL       = "https://github.com/xgxg-mdl/model-uptime/tags.atom"
	defaultRegistryURL   = "https://ghcr.io/v2/xgxg-mdl/model-uptime/manifests/"
	defaultRegistryToken = "https://ghcr.io/token?service=ghcr.io&scope=repository:xgxg-mdl/model-uptime:pull"
	manifestAccept       = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
)

var stableVersionRE = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// BuildInfo 描述当前运行中的镜像版本。
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// Status 是管理 API 使用的版本与更新状态快照。
type Status struct {
	Enabled         bool      `json:"enabled"`
	DisabledReason  string    `json:"disabled_reason,omitempty"`
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	DeploymentTag   string    `json:"deployment_tag"`
	Commit          string    `json:"commit,omitempty"`
	BuiltAt         string    `json:"built_at,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	Updating        bool      `json:"updating"`
	LastUpdateError string    `json:"last_update_error,omitempty"`
}

// Options 提供可替换的端点，便于测试以及私有镜像部署。
type Options struct {
	Context          context.Context
	BuildInfo        BuildInfo
	DeploymentTag    string
	UpdateURL        string
	UpdateToken      string
	FeedURL          string
	RegistryURL      string
	RegistryTokenURL string
	HTTPClient       *http.Client
	UpdateHTTPClient *http.Client
	CacheTTL         time.Duration
	TriggerDelay     time.Duration
	UpdateWindow     time.Duration
	HistoryPollEvery time.Duration
	Logger           *slog.Logger
}

// Service 持有版本检查缓存和单次更新互斥状态。
type Service struct {
	opt Options
	ctx context.Context
	// cancel 与 wg 让后台更新流程成为应用生命周期的一部分，而不是游离 goroutine。
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu              sync.Mutex
	cachedLatest    string
	cachedCheckedAt time.Time
	updating        bool
	lastUpdateError string
	closed          bool
}

// New 创建更新服务。配置不完整时仍可读取版本状态，但不能触发更新。
func New(opt Options) *Service {
	if opt.Context == nil {
		opt.Context = context.Background()
	}
	if opt.FeedURL == "" {
		opt.FeedURL = defaultFeedURL
	}
	if opt.RegistryURL == "" {
		opt.RegistryURL = defaultRegistryURL
	}
	if opt.RegistryTokenURL == "" {
		opt.RegistryTokenURL = defaultRegistryToken
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if opt.UpdateHTTPClient == nil {
		opt.UpdateHTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	if opt.CacheTTL <= 0 {
		opt.CacheTTL = 5 * time.Minute
	}
	if opt.TriggerDelay <= 0 {
		opt.TriggerDelay = 500 * time.Millisecond
	}
	if opt.UpdateWindow <= 0 {
		opt.UpdateWindow = 10 * time.Minute
	}
	if opt.HistoryPollEvery <= 0 {
		opt.HistoryPollEvery = 2 * time.Second
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	opt.BuildInfo.Version = normalizeVersion(opt.BuildInfo.Version)
	opt.DeploymentTag = strings.TrimSpace(opt.DeploymentTag)
	ctx, cancel := context.WithCancel(opt.Context)
	return &Service{opt: opt, ctx: ctx, cancel: cancel}
}

// Check 返回版本状态。force 会跳过进程内缓存。
func (s *Service) Check(ctx context.Context, force bool) (Status, error) {
	s.mu.Lock()
	latest, checkedAt := s.cachedLatest, s.cachedCheckedAt
	updating, lastErr := s.updating, s.lastUpdateError
	s.mu.Unlock()

	if force || latest == "" || time.Since(checkedAt) >= s.opt.CacheTTL {
		var err error
		latest, err = s.latestPublished(ctx)
		if err != nil {
			return s.status("", time.Time{}, updating, lastErr), err
		}
		checkedAt = time.Now()
		s.mu.Lock()
		s.cachedLatest, s.cachedCheckedAt = latest, checkedAt
		s.mu.Unlock()
	}
	// 远端检查期间可能刚好开始或结束更新，返回前重新读取运行状态。
	s.mu.Lock()
	updating, lastErr = s.updating, s.lastUpdateError
	s.mu.Unlock()
	return s.status(latest, checkedAt, updating, lastErr), nil
}

// Start 验证更新条件并异步调用 sidecar。延迟触发让 API 的 202 响应先抵达浏览器。
func (s *Service) Start(latest string) error {
	latest = normalizeVersion(latest)
	enabled, reason := s.enabled()
	if !enabled {
		return errors.New(reason)
	}
	if latest == "" || !isNewer(latest, s.opt.BuildInfo.Version) {
		return errors.New("already running the latest version")
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("the update service is shutting down")
	}
	if s.updating {
		s.mu.Unlock()
		return errors.New("an update is already in progress")
	}
	s.updating = true
	s.lastUpdateError = ""
	s.mu.Unlock()
	s.opt.Logger.Info("已接受容器更新请求", "target_version", latest)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		delay := time.NewTimer(s.opt.TriggerDelay)
		select {
		case <-delay.C:
		case <-s.ctx.Done():
			if !delay.Stop() {
				<-delay.C
			}
			s.finishUpdate(s.ctx.Err())
			return
		}
		startedAt := time.Now().UTC()
		ctx, cancel := context.WithTimeout(s.ctx, s.opt.UpdateWindow)
		defer cancel()
		err := s.trigger(ctx)
		if err != nil {
			s.opt.Logger.Error("容器更新器请求失败", "target_version", latest, "err", err)
			s.finishUpdate(err)
			return
		}
		s.opt.Logger.Info("容器更新器已接受请求", "target_version", latest)
		if err := s.waitForResult(ctx, startedAt); err != nil {
			s.opt.Logger.Error("容器更新未完成", "target_version", latest, "err", err)
			s.finishUpdate(err)
		}
	}()
	return nil
}

// Close 取消后台更新并等待退出。调用方给出的 ctx 只限制等待时间，
// 不会覆盖已经发出的根取消信号。
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) status(latest string, checkedAt time.Time, updating bool, lastErr string) Status {
	enabled, reason := s.enabled()
	return Status{
		Enabled:         enabled,
		DisabledReason:  reason,
		CurrentVersion:  s.opt.BuildInfo.Version,
		LatestVersion:   latest,
		UpdateAvailable: isNewer(latest, s.opt.BuildInfo.Version),
		DeploymentTag:   s.opt.DeploymentTag,
		Commit:          s.opt.BuildInfo.Commit,
		BuiltAt:         s.opt.BuildInfo.BuiltAt,
		CheckedAt:       checkedAt,
		Updating:        updating,
		LastUpdateError: lastErr,
	}
}

func (s *Service) enabled() (bool, string) {
	if s.opt.BuildInfo.Version == "" {
		return false, "Development builds do not support one-click updates."
	}
	if s.opt.DeploymentTag != "latest" {
		return false, "One-click updates require MODEL_UPTIME_TAG=latest."
	}
	if strings.TrimSpace(s.opt.UpdateURL) == "" || strings.TrimSpace(s.opt.UpdateToken) == "" {
		return false, "The update service is not configured."
	}
	updateURL, err := url.Parse(s.opt.UpdateURL)
	if err != nil || updateURL.Scheme != "http" || updateURL.Host == "" || updateURL.Query().Get("async") != "true" {
		return false, "The update service must use an internal HTTP URL with async=true."
	}
	return true, ""
}

func (s *Service) latestPublished(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opt.FeedURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create the release check request: %w", err)
	}
	req.Header.Set("Accept", "application/atom+xml")
	resp, err := s.opt.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to read GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("failed to read GitHub releases: HTTP %d", resp.StatusCode)
	}

	var feed struct {
		Entries []struct {
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&feed); err != nil {
		return "", fmt.Errorf("failed to parse GitHub releases: %w", err)
	}
	versions := make([]string, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		if v := normalizeVersion(strings.TrimSpace(entry.Title)); v != "" {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return "", errors.New("GitHub did not return a stable release")
	}
	sort.Slice(versions, func(i, j int) bool { return compareVersion(versions[i], versions[j]) > 0 })

	token, err := s.registryToken(ctx)
	if err != nil {
		return "", err
	}
	for _, version := range versions {
		digest, ready, err := s.manifestDigest(ctx, token, version)
		if err != nil {
			return "", err
		}
		if ready {
			latestDigest, latestReady, err := s.manifestDigest(ctx, token, "latest")
			if err != nil {
				return "", err
			}
			if !latestReady {
				return "", errors.New("the GHCR latest image has not been published")
			}
			if digest != latestDigest {
				return "", fmt.Errorf("the GHCR latest image does not match stable release %s", version)
			}
			return version, nil
		}
	}
	return "", errors.New("GHCR does not contain a published stable release image")
}

func (s *Service) registryToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opt.RegistryTokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create the GHCR authentication request: %w", err)
	}
	resp, err := s.opt.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to authenticate with GHCR: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("failed to authenticate with GHCR: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil || data.Token == "" {
		return "", errors.New("GHCR returned an invalid authentication response")
	}
	return data.Token, nil
}

func (s *Service) manifestDigest(ctx context.Context, token, version string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opt.RegistryURL+url.PathEscape(version), nil)
	if err != nil {
		return "", false, fmt.Errorf("failed to create the GHCR image check request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestAccept)
	resp, err := s.opt.HTTPClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("failed to check the GHCR image: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode/100 != 2 {
		return "", false, fmt.Errorf("failed to check the GHCR image: HTTP %d", resp.StatusCode)
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return "", false, errors.New("GHCR image response did not include a content digest")
	}
	return digest, true, nil
}

func (s *Service) trigger(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opt.UpdateURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create the update request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.opt.UpdateToken)
	resp, err := s.opt.UpdateHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("update service request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("update service rejected the request: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Service) waitForResult(ctx context.Context, startedAt time.Time) error {
	historyURL, err := url.Parse(s.opt.UpdateURL)
	if err != nil {
		return fmt.Errorf("failed to parse the update service URL: %w", err)
	}
	historyURL.Path = "/v1/history"
	query := historyURL.Query()
	for key := range query {
		query.Del(key)
	}
	query.Set("since", startedAt.Format(time.RFC3339Nano))
	query.Set("limit", "10")
	historyURL.RawQuery = query.Encode()

	ticker := time.NewTicker(s.opt.HistoryPollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("the update did not complete within the update window; check the updater logs")
		case <-ticker.C:
			result, found, err := s.latestHistory(ctx, historyURL.String(), startedAt)
			if err != nil {
				continue // sidecar 短暂不可达时继续等待，最终由更新窗口给出稳定错误。
			}
			if !found {
				continue
			}
			if result.Failed > 0 {
				return fmt.Errorf("the container update failed for %d item(s); check the updater logs", result.Failed)
			}
			if result.Updated == 0 {
				return errors.New("the update service completed without updating a container")
			}
			// 成功更新会终止当前进程；若仍在运行，保持锁直到容器切换完成。
		}
	}
}

type historyEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Updated   int       `json:"updated"`
	Failed    int       `json:"failed"`
}

func (s *Service) latestHistory(ctx context.Context, endpoint string, startedAt time.Time) (historyEntry, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return historyEntry{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.opt.UpdateToken)
	resp, err := s.opt.HTTPClient.Do(req)
	if err != nil {
		return historyEntry{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return historyEntry{}, false, fmt.Errorf("history request returned HTTP %d", resp.StatusCode)
	}
	var data struct {
		Entries []historyEntry `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return historyEntry{}, false, err
	}
	for i := len(data.Entries) - 1; i >= 0; i-- {
		if !data.Entries[i].Timestamp.Before(startedAt) {
			return data.Entries[i], true, nil
		}
	}
	return historyEntry{}, false, nil
}

func (s *Service) finishUpdate(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updating = false
	if err != nil {
		s.lastUpdateError = err.Error()
	}
}

func normalizeVersion(v string) string {
	m := stableVersionRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	parts := make([]string, 3)
	for i := range parts {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return ""
		}
		parts[i] = strconv.FormatUint(n, 10)
	}
	return "v" + strings.Join(parts, ".")
}

func isNewer(candidate, current string) bool {
	return compareVersion(candidate, current) > 0
}

func compareVersion(a, b string) int {
	ma, mb := stableVersionRE.FindStringSubmatch(a), stableVersionRE.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0
	}
	for i := 1; i <= 3; i++ {
		ai, _ := strconv.ParseUint(ma[i], 10, 64)
		bi, _ := strconv.ParseUint(mb[i], 10, 64)
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}
