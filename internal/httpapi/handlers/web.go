package handlers

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/norlis/hydra/web"
)

type WebHandler struct {
	logger  *slog.Logger
	statics http.Handler
}

// NewWebHandler serves the embedded UI. The error is returned, not logged,
// so a broken embed fails the fx graph at startup instead of panicking on
// the first request.
func NewWebHandler(log *slog.Logger) (*WebHandler, error) {
	subFs, err := fs.Sub(web.Files, "assets")
	if err != nil {
		return nil, fmt.Errorf("embedded assets: %w", err)
	}

	return &WebHandler{
		logger:  log,
		statics: http.FileServer(http.FS(subFs)),
	}, nil
}

func (h *WebHandler) EventsPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.Files, "assets/index.html")
}

func (h *WebHandler) ServeAssets() http.Handler {
	return h.statics
}
