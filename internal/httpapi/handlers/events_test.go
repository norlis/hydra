package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/bus"
)

// A canceled request context makes Events return right after the initial
// SSE handshake, so we can assert headers + retry deterministically.
func TestEventsHandshake(t *testing.T) {
	t.Parallel()
	h := NewEventsHandler(bus.NewEventBus(slog.New(slog.DiscardHandler)), slogDiscard())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events", http.NoBody)
	rec := httptest.NewRecorder()

	h.Events(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "retry: 5000") {
		t.Errorf("body missing initial retry line: %q", rec.Body.String())
	}
	// The UI is served from this same origin; the stream must not be
	// readable by arbitrary web pages that can reach the control port.
	if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want no CORS header", v)
	}
}
