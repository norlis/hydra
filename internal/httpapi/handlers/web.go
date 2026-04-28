package handlers

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/google/uuid"
	"github.com/norlis/hydra/web"
	"go.uber.org/zap"
)

type WebHandler struct {
	logger  *zap.Logger
	tpl     *template.Template
	statics http.Handler
}

func NewWebHandler(logger *zap.Logger) *WebHandler {
	tpl, err := template.ParseFS(web.Files, "assets/templates/events.html")
	if err != nil {
		logger.Error("ErrParseTemplate", zap.Error(err))
		return nil
	}

	subFs, err := fs.Sub(web.Files, "assets")
	if err != nil {
		logger.Error("Failed to create sub filesystem for assets", zap.Error(err))
		return nil
	}

	return &WebHandler{
		logger:  logger,
		tpl:     tpl,
		statics: http.FileServer(http.FS(subFs)),
	}
}

// EventsPage  HTML para visualizar los eventos SSE en tiempo real.
func (h *WebHandler) EventsPage(w http.ResponseWriter, r *http.Request) {
	data := struct {
		ClientID string
	}{
		ClientID: uuid.NewString(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := h.tpl.Execute(w, data)
	if err != nil {
		h.logger.Error("Failed to execute events template", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (h *WebHandler) ServeAssets() http.Handler {
	return h.statics
}
