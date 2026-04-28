package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
	"go.uber.org/zap"
)

type ProxyInfo struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

type TopologyHandler struct {
	render    presenters.Presenters
	discovery cluster.Discovery
	eventBus  bus.EventBus
	logger    *zap.Logger
}

func NewTopologyHandler(render presenters.Presenters, discovery cluster.Discovery, eventBus bus.EventBus, logger *zap.Logger) *TopologyHandler {
	return &TopologyHandler{render: render, discovery: discovery, eventBus: eventBus, logger: logger}
}

// Always include the local node first so the caller can tell which
// node served the request without having to inspect headers.
func (h *TopologyHandler) getAllNodes() []topology.Node {
	active := h.discovery.GetActiveNodes()
	local := h.discovery.GetLocalNode()

	nodes := make([]topology.Node, 0, len(active)+1)
	nodes = append(nodes, local)

	for _, n := range active {
		if n.ID == local.ID {
			continue
		}
		nodes = append(nodes, n)
	}

	return nodes
}

// Nodes
// @Summary Nodes
// @Description list of cluster nodes (local + active peers)
// @Tags topology
// @Accept json
// @Produce json
// @Success 200 {object}  []topology.Node ""
// @Failure 500 {object} problem.ProblemDetail "Internal error"
// @Router /api/nodes [get].
func (h *TopologyHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	h.render.JSON(w, r, h.getAllNodes())
}

// Proxies
// @Summary Proxies
// @Description list of all proxy addresses in the cluster
// @Tags topology
// @Accept json
// @Produce json
// @Success 200 {object} []ProxyInfo ""
// @Failure 500 {object} problem.ProblemDetail "Internal error"
// @Router /api/proxies [get]
func (h *TopologyHandler) Proxies(w http.ResponseWriter, r *http.Request) {
	nodes := h.getAllNodes()

	proxies := make([]ProxyInfo, 0, len(nodes))
	for _, n := range nodes {
		if len(n.Interfaces) > 0 {
			proxies = append(proxies, ProxyInfo{
				NodeID:  n.ID,
				Address: n.Interfaces[0].Address(),
			})
		}
	}

	h.render.JSON(w, r, proxies)
}

// Events
// @Summary SSE Cluster Events
// @Description Real-time stream of cluster membership events (node.joined, node.left, node.updated) via Server-Sent Events.
// @Tags topology
// @Produce text/event-stream
// @Success 200 {string} string "SSE stream of events"
// @Failure 500 {string} string "Streaming unsupported"
// @Router /api/events [get]
func (h *TopologyHandler) Events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	// SSE headers + proxy buffering bypass (e.g. nginx X-Accel-Buffering)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Clear the server WriteTimeout so long-lived SSE streams are not killed
	// by the global deadline (Go 1.20+ per-request override).
	_ = rc.SetWriteDeadline(time.Time{})

	if err := rc.Flush(); err != nil {
		h.logger.Error("streaming unsupported by ResponseWriter", zap.Error(err))
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to EventBus and ensure safe cleanup with defer
	ch := h.eventBus.Subscribe()
	defer h.eventBus.Unsubscribe(ch)

	h.logger.Debug("client subscribed to SSE events", zap.String("remote_addr", r.RemoteAddr))

	// Keep-Alive ping to prevent timeouts in AWS ALB or intermediary proxies
	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()

	ctx := r.Context()

	// Event loop
	for {
		select {
		case <-ctx.Done():
			// Client closed the connection (e.g., closed the browser tab)
			h.logger.Debug("client disconnected from SSE", zap.String("remote_addr", r.RemoteAddr))
			return

		case ev, ok := <-ch:
			if !ok {
				// Event bus was closed by the server
				h.logger.Warn("event bus channel closed, terminating SSE connection")
				return
			}

			// Serialize and send the event to the client
			data, err := json.Marshal(ev.Node) // (Or json.MarshalWrite if using json v2)
			if err != nil {
				h.logger.Error("failed to marshal event node", zap.Error(err))
				continue
			}

			payload := fmt.Sprintf("event: %s\ndata: %s\n\n", ev.Type.String(), string(data))

			if _, err := w.Write([]byte(payload)); err != nil {
				// If writing fails, the client disconnected (broken pipe)
				h.logger.Debug("failed to write event payload, dropping client", zap.Error(err))
				return
			}

			_ = rc.Flush()

		case <-keepAliveTicker.C:
			// Send an SSE comment. The browser ignores it silently,
			// but it tricks the Load Balancer into keeping the TCP connection alive.
			_, err := w.Write([]byte(":keepalive\n\n"))
			if err != nil {
				// If ping write fails, the connection is physically broken
				h.logger.Debug("failed to write keep-alive ping, dropping client", zap.Error(err))
				return
			}
			_ = rc.Flush()
		}
	}
}
