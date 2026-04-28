package handlers

import (
	"net/http"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
)

type ProxyInfo struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

type TopologyHandler struct {
	render    presenters.Presenters
	discovery cluster.Discovery
}

func NewTopologyHandler(render presenters.Presenters, discovery cluster.Discovery) *TopologyHandler {
	return &TopologyHandler{render: render, discovery: discovery}
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
