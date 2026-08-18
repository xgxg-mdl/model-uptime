// Package httpserver 提供公开状态页、管理接口和嵌入式静态资源的 HTTP 传输层。
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/heatmap"
	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/update"
)

const maxJSONBody = 1 << 20

type StatusProvider interface {
	Snapshot() model.StatusResponse
}

type HeatmapProvider interface {
	Build(context.Context, string) (heatmap.Response, error)
}

type UpdateProvider interface {
	Check(context.Context, bool) (update.Status, error)
	Start(string) error
}

type Options struct {
	Admin   *admin.Manager
	Status  StatusProvider
	Heatmap HeatmapProvider
	Updater UpdateProvider
	Assets  fs.FS
	Logger  *slog.Logger
}

type Server struct {
	admin   *admin.Manager
	status  StatusProvider
	heatmap HeatmapProvider
	updater UpdateProvider
	assets  fs.FS
	logger  *slog.Logger
}

func New(options Options) (*Server, error) {
	if options.Admin == nil {
		return nil, errors.New("admin manager is required")
	}
	if options.Status == nil {
		return nil, errors.New("status provider is required")
	}
	if options.Heatmap == nil {
		return nil, errors.New("heatmap provider is required")
	}
	if options.Assets == nil {
		options.Assets = webAssets()
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Server{
		admin: options.Admin, status: options.Status, heatmap: options.Heatmap, updater: options.Updater,
		assets: options.Assets, logger: options.Logger,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/heatmap", s.handleHeatmap)
	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	mux.HandleFunc("GET /api/admin/setup-status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/admin/setup", s.handleSetup)
	mux.Handle("GET /api/admin/services", s.requireAuth(s.handleListServices))
	mux.Handle("POST /api/admin/services", s.requireAuth(s.handleCreateService))
	mux.Handle("PATCH /api/admin/services", s.requireAuth(s.handleBulkUpdateServices))
	mux.Handle("PUT /api/admin/services/{id}", s.requireAuth(s.handleUpdateService))
	mux.Handle("DELETE /api/admin/services/{id}", s.requireAuth(s.handleDeleteService))
	mux.Handle("POST /api/admin/services/{id}/test", s.requireAuth(s.handleTestService))
	mux.Handle("POST /api/admin/services/{id}/duplicate", s.requireAuth(s.handleDuplicateService))
	mux.Handle("GET /api/admin/page", s.requireAuth(s.handleGetPage))
	mux.Handle("PUT /api/admin/page", s.requireAuth(s.handleUpdatePage))
	mux.Handle("GET /api/admin/telegram", s.requireAuth(s.handleGetTelegram))
	mux.Handle("PUT /api/admin/telegram", s.requireAuth(s.handleUpdateTelegram))
	mux.Handle("POST /api/admin/telegram/test", s.requireAuth(s.handleTestTelegram))
	mux.Handle("GET /api/admin/update", s.requireAuth(s.handleGetUpdate))
	mux.Handle("POST /api/admin/update/check", s.requireAuth(s.handleCheckUpdate))
	mux.Handle("POST /api/admin/update", s.requireAuth(s.handleStartUpdate))
	mux.HandleFunc("GET /admin", s.handleAdminPage)
	mux.HandleFunc("GET /heatmap", s.handleHeatmapPage)
	mux.Handle("GET /", s.staticHandler())
	return s.securityHeaders(s.logRequests(mux))
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.status.Snapshot())
}

func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = heatmap.RangeWeek
	}
	response, err := s.heatmap.Build(r.Context(), rangeName)
	if errors.Is(err, heatmap.ErrInvalidRange) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("构建公开热力图失败", "range", rangeName, "err", err)
		writeError(w, http.StatusInternalServerError, "热力图暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, "admin/index.html")
}

func (s *Server) handleHeatmapPage(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, "heatmap/index.html")
}

func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.FS(s.assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAssetCache(w.Header(), r.URL.Path)
		files.ServeHTTP(w, r)
	})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	setAssetCache(w.Header(), r.URL.Path)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func setAssetCache(header http.Header, path string) {
	switch {
	case path == "/" || path == "/index.html" || path == "/admin" || path == "/admin/" || path == "/heatmap" || path == "/heatmap/" || strings.HasSuffix(path, ".html"):
		header.Set("Cache-Control", "no-store")
	case strings.HasPrefix(path, "/fonts/"):
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		header.Set("Cache-Control", "public, max-age=300, must-revalidate")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	reader := http.MaxBytesReader(w, r.Body, maxJSONBody)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("请求体只能包含一个 JSON 值")
		}
		return err
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(w, http.StatusRequestEntityTooLarge, "请求体超过 1 MiB 限制")
		return
	}
	writeError(w, http.StatusBadRequest, "请求体不是有效 JSON")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Default().Debug("写入 JSON 响应失败", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeAdminError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch admin.KindOf(err) {
	case admin.ErrorInvalid:
		status = http.StatusBadRequest
	case admin.ErrorNotFound:
		status = http.StatusNotFound
	case admin.ErrorConflict:
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.bytes,
			"duration", time.Since(startedAt),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
		r.status = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(body)
	r.bytes += written
	return written, err
}

func errorMessage(prefix string, err error) string {
	return fmt.Sprintf("%s: %v", prefix, err)
}
