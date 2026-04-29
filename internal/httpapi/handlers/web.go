package handlers

import (
	"io/fs"
	"net/http"

	"github.com/norlis/hydra/web"
	"go.uber.org/zap"
)

type WebHandler struct {
	logger  *zap.Logger
	statics http.Handler
}

func NewWebHandler(logger *zap.Logger) *WebHandler {
	subFs, err := fs.Sub(web.Files, "assets")
	if err != nil {
		logger.Error("Failed to create sub filesystem for assets", zap.Error(err))
		return nil
	}

	return &WebHandler{
		logger:  logger,
		statics: http.FileServer(http.FS(subFs)),
	}
}

func (h *WebHandler) EventsPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.Files, "assets/index.html")
}

func (h *WebHandler) ServeAssets() http.Handler {
	return h.statics
}
