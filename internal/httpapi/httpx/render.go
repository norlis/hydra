// Package httpx wraps httpgate's free presenter functions behind a small
// injectable struct: handlers keep h.render.Error / h.render.JSON ergonomics
// and 5xx responses are logged via the injected logger.
package httpx

import (
	"log/slog"
	"net/http"

	"github.com/norlis/httpgate/presenter"
)

type Render struct {
	log *slog.Logger
}

func New(log *slog.Logger) *Render {
	return &Render{log: log}
}

// Error writes an RFC 9457 problem+json response. 5xx faults are logged
// via the injected logger (4xx are not). Caller opts win.
func (rd *Render) Error(w http.ResponseWriter, r *http.Request, err error, opts ...presenter.ErrorOption) {
	all := make([]presenter.ErrorOption, 0, len(opts)+1)
	all = append(all, presenter.WithLogger(rd.log))
	all = append(all, opts...)
	presenter.Error(w, r, err, all...)
}

// JSON marshals v as application/json (default 200 OK).
func (rd *Render) JSON(w http.ResponseWriter, r *http.Request, v any, opts ...presenter.ResponseOption) {
	presenter.JSON(w, r, v, opts...)
}
