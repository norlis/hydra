package handlers

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/norlis/hydra/pkg/logger"
	"github.com/norlis/hydra/web"
)

type WebHandler struct {
	logger  *slog.Logger
	statics http.Handler
}

func NewWebHandler(log *slog.Logger) *WebHandler {
	subFs, err := fs.Sub(web.Files, "assets")
	if err != nil {
		log.Error("Failed to create sub filesystem for assets", logger.Err(err))
		return nil
	}

	return &WebHandler{
		logger:  log,
		statics: http.FileServer(http.FS(subFs)),
	}
}

func (h *WebHandler) EventsPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.Files, "assets/index.html")
}

func (h *WebHandler) ServeAssets() http.Handler {
	return h.statics
}
