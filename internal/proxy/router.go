package proxy

import (
	"net/http"

	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
	"go.uber.org/zap"
)

// EntityHeader is the per-request hint the router uses to place the
// request on the consistent hash ring. Callers set it to a stable
// identifier (tenant id, user id, URL path, etc.).
const EntityHeader = "X-Entity-ID"

// Router implements MeshRouter. One instance per bound interface. It
// decides whether the request belongs here (local processing) or to
// another peer (peer forwarding) and delegates the actual bytes to
// the Forwarder.
type Router struct {
	iface     topology.NetworkInterface
	ring      *cluster.Ring
	forwarder Forwarder
	log       *zap.Logger
}

func NewRouter(
	iface topology.NetworkInterface,
	ring *cluster.Ring,
	forwarder Forwarder,
	log *zap.Logger,
) *Router {
	return &Router{
		iface:     iface,
		ring:      ring,
		forwarder: forwarder,
		log:       log.With(zap.String("iface", iface.Name)),
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	peer := r.peerFor(req)
	r.logDecision(req, peer)
	if peer != "" {
		r.forwarder.ForwardToPeer(peer, w, req)
		return
	}
	r.forwarder.ForwardToExternal(w, req)
}

// logDecision emits one line per incoming request with the routing
// verdict. `decision` is one of:
//
//	local      -> processed by this node
//	peer       -> relayed to another node (peer address in "peer" field)
//	hop-local  -> already forwarded once (HopHeader set), kept local to
//	              avoid ping-pong loops during ring convergence
func (r *Router) logDecision(req *http.Request, peer string) {
	decision := "local"
	switch {
	case req.Header.Get(HopHeader) != "":
		decision = "hop-local"
	case peer != "":
		decision = "peer"
	}
	r.log.Info("proxy request",
		zap.String("method", req.Method),
		zap.String("host", req.Host),
		zap.String("entity_id", req.Header.Get(EntityHeader)),
		zap.String("decision", decision),
		zap.String("peer", peer),
	)
}

// peerFor returns the peer address that owns this request, or empty
// when the request must be processed locally. Requests are kept local
// when:
//   - they are already forwarded (HopHeader set), to avoid loops;
//   - no entityID was provided by the client;
//   - the ring is empty (cluster not converged yet);
//   - the ring selects this node.
func (r *Router) peerFor(req *http.Request) string {
	if req.Header.Get(HopHeader) != "" {
		return ""
	}
	entity := req.Header.Get(EntityHeader)
	if entity == "" || r.ring == nil || r.ring.Len() == 0 {
		return ""
	}
	ep := r.ring.EndpointFor(entity)
	if ep == nil || ep.IsSelf {
		return ""
	}
	return ep.ProxyAddr
}