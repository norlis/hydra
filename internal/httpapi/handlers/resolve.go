package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/norlis/httpgate/presenter"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/httpx"
)

const (
	// maxResolveBody caps the request body so a client cannot exhaust
	// memory reading it; 1 MiB comfortably holds the id limit below.
	maxResolveBody = 1 << 20
	// maxResolveIDs caps how many distinct entity ids one request resolves.
	maxResolveIDs = 10000
	// nodeIDSep separates a ring virtual id ("dev1::bridge100") into its
	// logical node name and interface name.
	nodeIDSep = "::"
)

var (
	errNotJSONArray = errors.New("request body is not a JSON array of strings")
	errTooManyIDs   = errors.New("too many entity ids (max 10000)")
	errRingNotReady = errors.New("ring not ready: cluster has not converged")
)

// EntityResolver is the subset of *cluster.Ring the resolve handler needs.
// Declared consumer-side so tests can substitute a fake without standing
// up a real gossip ring.
type EntityResolver interface {
	EndpointFor(entityID string) *cluster.Endpoint
	Len() int
}

// resolvedNode is the per-entity result: the logical node that owns the
// entity plus the proxy address reachable for it.
type resolvedNode struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

// ResolveHandler answers "which node owns these entity ids?" using the
// same consistent-hash ring the data plane routes with, so the answer
// never diverges from live routing.
type ResolveHandler struct {
	ring   EntityResolver
	render *httpx.Render
	logger *slog.Logger
}

// NewResolveHandler takes the concrete *cluster.Ring (fx-provided in
// cluster/module.go) and stores it behind EntityResolver.
func NewResolveHandler(ring *cluster.Ring, render *httpx.Render, logger *slog.Logger) *ResolveHandler {
	return &ResolveHandler{ring: ring, render: render, logger: logger}
}

// Register mounts the resolve route on the chained /api/ sub-mux.
func (h *ResolveHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/resolve", h.Resolve)
}

// Resolve
// @Summary Resolve entity ids to nodes
// @Description Maps each entity id to the node that owns it on the consistent-hash ring.
// @Description Input is a JSON array of strings; empty and duplicate ids are dropped.
// @Description Values are hashed verbatim (no trim/lowercase) to match live routing.
// @Tags topology
// @Accept json
// @Produce json
// @Param ids body []string true "Entity ids to resolve"
// @Success 200 {object} map[string]resolvedNode
// @Failure 400 {object} map[string]string "Invalid body or too many ids"
// @Failure 503 {object} map[string]string "Ring not converged"
// @Router /api/resolve [post].
func (h *ResolveHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxResolveBody)

	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		h.render.Error(w, r, errNotJSONArray, presenter.WithStatus(http.StatusBadRequest))
		return
	}

	// Guard before doing any work: an empty ring resolves nothing.
	if h.ring.Len() == 0 {
		h.logger.Debug("resolve rejected: ring has not converged", slog.Int("requested", len(ids)))
		h.render.Error(w, r, errRingNotReady, presenter.WithStatus(http.StatusServiceUnavailable))
		return
	}

	// List hygiene: drop empty ids and dedup. Values are NOT trimmed or
	// lowercased — they must hash exactly as the live X-Entity-ID header
	// would, or this preview would diverge from real routing.
	result := make(map[string]resolvedNode, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, seen := result[id]; seen {
			continue
		}
		if len(result) >= maxResolveIDs {
			h.render.Error(w, r, errTooManyIDs, presenter.WithStatus(http.StatusBadRequest))
			return
		}
		ep := h.ring.EndpointFor(id)
		if ep == nil {
			// Ring is non-empty (guarded above), so this is unexpected;
			// skip rather than emit a partial/null entry.
			continue
		}
		result[id] = resolvedNode{NodeID: logicalNodeID(ep.NodeID), Address: ep.ProxyAddr}
	}

	h.render.JSON(w, r, result)
}

// logicalNodeID returns the node name from a ring virtual id, dropping
// the "::iface" suffix ("dev1::bridge100" -> "dev1"). Ids without the
// separator are returned unchanged.
func logicalNodeID(virtualID string) string {
	if node, _, found := strings.Cut(virtualID, nodeIDSep); found {
		return node
	}
	return virtualID
}
