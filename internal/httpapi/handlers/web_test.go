package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/norlis/hydra/internal/testlog"
)

func TestNewWebHandler(t *testing.T) {
	t.Parallel()

	rec := testlog.New()
	h, err := NewWebHandler(rec.Logger())
	if err != nil {
		t.Fatalf("NewWebHandler: %v", err)
	}
	if h == nil {
		t.Fatal("NewWebHandler returned a nil handler without an error")
	}

	t.Run("serves the embedded assets", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		h.ServeAssets().ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/index.js", http.NoBody))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /index.js: status %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("serves the events page", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		h.EventsPage(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events", http.NoBody))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /events: status %d, want %d", rr.Code, http.StatusOK)
		}
	})
}
