package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/norlis/httpgate/presenter"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/httpx"
	"github.com/norlis/hydra/internal/topology"
	hlogger "github.com/norlis/hydra/pkg/logger"
)

type ProxyInfo struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

var (
	errMissingAddress   = errors.New("missing address parameter")
	errInvalidProxyAddr = errors.New("invalid proxy address")
)

type TopologyHandler struct {
	discovery cluster.Discovery
	render    *httpx.Render
	logger    *slog.Logger
}

func NewTopologyHandler(discovery cluster.Discovery, render *httpx.Render, logger *slog.Logger) *TopologyHandler {
	return &TopologyHandler{discovery: discovery, render: render, logger: logger}
}

// Register mounts the topology JSON routes on the chained /api/ sub-mux.
func (h *TopologyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes", h.Nodes)
	mux.HandleFunc("GET /api/proxies", h.Proxies)
	mux.HandleFunc("GET /api/proxies/test", h.TestProxy)
}

// Always include the local node first so the caller can tell which
// node served the request without inspecting headers.
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
// @Failure 500 {object} problem.Detail "Internal error"
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
// @Failure 500 {object} problem.Detail "Internal error"
// @Router /api/proxies [get].
func (h *TopologyHandler) Proxies(w http.ResponseWriter, r *http.Request) {
	nodes := h.getAllNodes()

	proxies := make([]ProxyInfo, 0, len(nodes))
	for _, n := range nodes {
		for _, e := range n.Interfaces {
			proxies = append(proxies, ProxyInfo{
				NodeID:  n.ID,
				Address: e.Address(),
			})
		}
	}

	h.render.JSON(w, r, proxies)
}

// TestProxy
// @Summary Test a proxy
// @Description Checks if a proxy address is operational by routing a request through it.
// @Description The target defaults to https://checkip.amazonaws.com (returns the egress IP).
// @Description Note: requires outbound internet access; will fail in air-gapped networks.
// @Tags topology
// @Produce json
// @Param address query string true  "Proxy address (host:port, e.g. 192.168.1.10:3128)"
// @Param target  query string false "Target URL to fetch through the proxy (default: https://checkip.amazonaws.com/)"
// @Success 200 {object} map[string]interface{}
// @Router /api/proxies/test [get].
func (h *TopologyHandler) TestProxy(w http.ResponseWriter, r *http.Request) {
	proxyAddr := r.URL.Query().Get("address")
	if proxyAddr == "" {
		h.render.Error(w, r, errMissingAddress, presenter.WithStatus(http.StatusBadRequest))
		return
	}

	if strings.ContainsAny(proxyAddr, "@/?#") {
		h.render.Error(w, r, errInvalidProxyAddr, presenter.WithStatus(http.StatusBadRequest))
		return
	}

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		h.render.Error(w, r, errInvalidProxyAddr, presenter.WithStatus(http.StatusBadRequest))
		return
	}

	target := r.URL.Query().Get("target")
	if target == "" {
		target = "https://checkip.amazonaws.com/"
	}

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		// 5xx: pass the real error so render logs it and reports it as detail.
		h.render.Error(w, r, err, presenter.WithStatus(http.StatusInternalServerError))
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		h.logger.Warn("proxy test failed", slog.String("proxy", proxyAddr), hlogger.Err(err))
		h.render.JSON(w, r, map[string]any{
			"proxy":   proxyAddr,
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))

	h.render.JSON(w, r, map[string]any{
		"proxy":   proxyAddr,
		"success": resp.StatusCode == http.StatusOK,
		"ip":      strings.TrimSpace(string(body)),
		"status":  resp.StatusCode,
	})
}
