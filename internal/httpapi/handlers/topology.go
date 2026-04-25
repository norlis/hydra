package handlers

import (
	"net/http"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
)

type TopologyHandler struct {
	render    presenters.Presenters
	discovery cluster.Discovery
}

func NewTopologyHandler(render presenters.Presenters, discovery cluster.Discovery) *TopologyHandler {
	return &TopologyHandler{render: render, discovery: discovery}
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
	active := h.discovery.GetActiveNodes()
	local := h.discovery.GetLocalNode()

	// Always include the local node first so the caller can tell which
	// node served the request without having to inspect headers.
	nodes := make([]topology.Node, 0, len(active)+1)
	nodes = append(nodes, local)
	for _, n := range active {
		if n.ID == local.ID {
			continue
		}
		nodes = append(nodes, n)
	}

	h.render.JSON(w, r, nodes)
}
