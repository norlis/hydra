package handlers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/norlis/httpgate/presenter"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/httpx"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
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
	errUnknownProxy     = errors.New("address is not a proxy of this mesh")
	errInvalidTarget    = errors.New("invalid target: only http or https URLs with a host are accepted")
)

type TopologyHandler struct {
	discovery  cluster.Discovery
	classifier *ipcheck.Classifier
	render     *httpx.Render
	logger     *slog.Logger
}

// NewTopologyHandler serves the topology routes. The classifier is the same
// egress policy the data plane enforces, so the control plane cannot be
// used to reach destinations the proxies would refuse.
func NewTopologyHandler(discovery cluster.Discovery, classifier *ipcheck.Classifier, render *httpx.Render, logger *slog.Logger) *TopologyHandler {
	return &TopologyHandler{discovery: discovery, classifier: classifier, render: render, logger: logger}
}

// Register mounts the topology JSON routes on the chained /api/ sub-mux.
func (h *TopologyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes", h.Nodes)
	mux.HandleFunc("GET /api/proxies", h.Proxies)
	mux.HandleFunc("GET /api/proxies/test", h.TestProxy)
}

// getAllNodes returns the local node plus active peers, sorted by ID.
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

	// memberlist reshuffles its member list every probe round, and clients
	// behind a load balancer reach a different node on each call: sort the
	// whole list so every member returns the same order.
	slices.SortFunc(nodes, func(a, b topology.Node) int {
		return cmp.Compare(a.ID, b.ID)
	})

	return nodes
}

// Nodes
// @Summary Nodes
// @Description list of cluster nodes (local + active peers), sorted by node ID
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
// @Description list of all proxy addresses in the cluster, sorted by node ID
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
// @Description The proxy must be a member of this mesh (see /api/proxies) and
// @Description the target is subject to the same egress policy as the data plane.
// @Param address query string true  "Proxy address (host:port, e.g. 192.168.1.10:3128)"
// @Param target  query string false "Target URL to fetch through the proxy (default: https://checkip.amazonaws.com/)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} problem.Detail "Malformed address or target"
// @Failure 403 {object} problem.Detail "Address outside the mesh or target denied by egress policy"
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

	if !h.isMeshProxy(proxyAddr) {
		h.render.Error(w, r, errUnknownProxy, presenter.WithStatus(http.StatusForbidden))
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

	if err := h.checkTarget(r.Context(), target); err != nil {
		switch {
		case errors.Is(err, errInvalidTarget):
			h.render.Error(w, r, err, presenter.WithStatus(http.StatusBadRequest))
		case ipcheck.IsDenied(err):
			h.render.Error(w, r, err, presenter.WithStatus(http.StatusForbidden))
		default:
			// Resolution failed: the test itself failed, same shape as a dial error.
			h.render.JSON(w, r, map[string]any{"proxy": proxyAddr, "success": false, "error": err.Error()})
		}
		return
	}

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
		// A redirect would carry the request to a host that never went
		// through the egress check above; report it instead of following.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
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

// isMeshProxy reports whether addr is one of the proxy endpoints the cluster
// currently advertises. Anything else would turn this endpoint into an
// open connect-to-anything primitive.
func (h *TopologyHandler) isMeshProxy(addr string) bool {
	for _, n := range h.getAllNodes() {
		for _, e := range n.Interfaces {
			if e.Address() == addr {
				return true
			}
		}
	}
	return false
}

// checkTarget resolves the target host and classifies every address with the
// data-plane egress policy. The peer proxy checks again at dial time; this
// pass rejects obvious abuse before any connection leaves the control plane.
func (h *TopologyHandler) checkTarget(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errInvalidTarget
	}

	// LookupNetIP returns IP literals as-is without touching DNS.
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("resolve target host: %w", err)
	}
	for _, ip := range ips {
		if d := h.classifier.Classify(ip); d != ipcheck.Allow {
			return &ipcheck.DeniedError{IP: ip.Unmap(), Reason: d}
		}
	}
	return nil
}
