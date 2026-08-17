package httpserver

import (
	"net/http"
	"strings"

	"github.com/xgxg-mdl/model-uptime/internal/model"
	"github.com/xgxg-mdl/model-uptime/internal/notification"
)

func (s *Server) handleGetPage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.admin.Snapshot().Page)
}

func (s *Server) handleUpdatePage(w http.ResponseWriter, r *http.Request) {
	var page model.PageConfig
	if err := decodeJSON(w, r, &page); err != nil {
		writeDecodeError(w, err)
		return
	}
	updated, err := s.admin.UpdatePage(page)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleGetTelegram(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, telegramView(s.admin.Snapshot().Telegram))
}

func (s *Server) handleUpdateTelegram(w http.ResponseWriter, r *http.Request) {
	var telegram notification.Config
	if err := decodeJSON(w, r, &telegram); err != nil {
		writeDecodeError(w, err)
		return
	}
	updated, err := s.admin.UpdateTelegram(telegram)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, telegramView(updated))
}

func (s *Server) handleTestTelegram(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SubscriptionID string `json:"subscription_id"`
		Kind           string `json:"kind"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(request.SubscriptionID) == "" {
		writeError(w, http.StatusBadRequest, "subscription_id 不能为空")
		return
	}
	var err error
	if request.Kind == "daily" {
		err = s.admin.SendDailyTestNotification(r.Context(), request.SubscriptionID)
	} else if request.Kind != "" && request.Kind != "event" {
		writeError(w, http.StatusBadRequest, "kind 仅支持 event 或 daily")
		return
	} else {
		err = s.admin.SendTestNotification(r.Context(), request.SubscriptionID)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func telegramView(config notification.Config) map[string]any {
	token := ""
	if config.BotToken != "" {
		token = "****"
	}
	return map[string]any{
		"bot_token":        token,
		"token_configured": config.BotToken != "",
		"subscriptions":    config.Subscriptions,
		"templates": map[string]string{
			"zh": notification.TemplateForLanguage("zh"),
			"en": notification.TemplateForLanguage("en"),
		},
	}
}
