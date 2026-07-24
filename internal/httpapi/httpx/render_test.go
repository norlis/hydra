package httpx

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/norlis/httpgate/presenter"
)

func newRender(w *bytes.Buffer) *Render {
	return New(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestErrorProblemJSON(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	rd := newRender(&logBuf)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)

	rd.Error(rec, req, errors.New("bad input"), presenter.WithStatus(http.StatusBadRequest))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

// 4xx must NOT be logged; only 5xx are.
func TestErrorLogsOnly5xx(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	rd := newRender(&logBuf)
	req := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)

	rd.Error(httptest.NewRecorder(), req, errors.New("client"), presenter.WithStatus(http.StatusBadRequest))
	if logBuf.Len() != 0 {
		t.Errorf("4xx must not log, got %q", logBuf.String())
	}

	rd.Error(httptest.NewRecorder(), req, errors.New("boom"), presenter.WithStatus(http.StatusInternalServerError))
	if logBuf.Len() == 0 {
		t.Error("5xx must be logged")
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	rd := New(slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", http.NoBody)

	rd.JSON(rec, req, map[string]string{"a": "b"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"a":"b"`) {
		t.Errorf("body = %q, want the map", rec.Body.String())
	}
}
