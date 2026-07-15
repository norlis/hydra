package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/norlis/hydra/internal/bus"
	hlogger "github.com/norlis/hydra/pkg/logger"
)

// EventsHandler streams cluster membership events over SSE. It is wired
// directly (not through the middleware chain) because the chain wraps the
// ResponseWriter, which would break flushing.
type EventsHandler struct {
	eventBus bus.EventBus
	logger   *slog.Logger
}

func NewEventsHandler(eventBus bus.EventBus, logger *slog.Logger) *EventsHandler {
	return &EventsHandler{eventBus: eventBus, logger: logger}
}

// Events
// @Summary SSE Cluster Events
// @Description Real-time stream of cluster membership events (node.joined, node.left, node.updated) via Server-Sent Events.
// @Tags topology
// @Produce text/event-stream
// @Success 200 {string} string "SSE stream of events"
// @Failure 500 {string} string "Streaming unsupported"
// @Router /api/events [get].
func (h *EventsHandler) Events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Clear the server WriteTimeout so long-lived SSE streams survive.
	_ = rc.SetWriteDeadline(time.Time{})

	if err := rc.Flush(); err != nil {
		// Deliberate exception to the problem+json rule: an SSE client
		// expects text/event-stream, not application/problem+json. Only
		// reachable if the writer exposes no Flusher (never with net/http).
		h.logger.Error("streaming unsupported by ResponseWriter", hlogger.Err(err))
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := h.eventBus.Subscribe()
	defer h.eventBus.Unsubscribe(ch)

	h.logger.Debug("client subscribed to SSE events", slog.String("remote_addr", r.RemoteAddr))

	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	_ = rc.Flush()

	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			h.logger.Debug("client disconnected from SSE", slog.String("remote_addr", r.RemoteAddr))
			return

		case ev, ok := <-ch:
			if !ok {
				h.logger.Warn("event bus channel closed, terminating SSE connection")
				return
			}
			data, err := json.Marshal(ev.Node)
			if err != nil {
				h.logger.Error("failed to marshal event node", hlogger.Err(err))
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type.String(), data); err != nil {
				h.logger.Debug("failed to write event payload, dropping client", hlogger.Err(err))
				return
			}
			if err := rc.Flush(); err != nil {
				h.logger.Debug("flush failed after event, dropping client", hlogger.Err(err))
				return
			}

		case <-keepAliveTicker.C:
			if _, err := fmt.Fprint(w, ":ping\n\n"); err != nil {
				h.logger.Debug("failed to write keep-alive ping, dropping client", hlogger.Err(err))
				return
			}
			if err := rc.Flush(); err != nil {
				h.logger.Debug("flush failed after ping, dropping client", hlogger.Err(err))
				return
			}
		}
	}
}
