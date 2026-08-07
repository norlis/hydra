package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/internal/topology"
)

// EntityHeader is the per-request hint the router uses to place the
// request on the consistent hash ring. Callers set it to a stable
// identifier (tenant id, user id, URL path, etc.).
const EntityHeader = "X-Entity-ID"

// Routing decisions. Kept as a closed enum so they survive as a low-
// cardinality Prometheus label.
const (
	decisionLocal    = "local"
	decisionPeer     = "peer"
	decisionHopLocal = "hop-local"
)

// Router implements MeshRouter. One instance per bound interface. It
// decides whether the request belongs here (local processing) or to
// another peer (peer forwarding) and delegates the actual bytes to
// the Forwarder.
type Router struct {
	iface     topology.NetworkInterface
	ring      *cluster.Ring
	forwarder Forwarder
	metrics   *metrics.Metrics
	log       *slog.Logger
}

func NewRouter(
	iface topology.NetworkInterface,
	ring *cluster.Ring,
	forwarder Forwarder,
	mtr *metrics.Metrics,
	log *slog.Logger,
) *Router {
	return &Router{
		iface:     iface,
		ring:      ring,
		forwarder: forwarder,
		metrics:   mtr,
		log:       log.With(slog.String("iface", iface.Name)),
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// One correlation id per request, threaded via context so the forwarder's
	// per-request logs join the router's under the same request_id.
	req = req.WithContext(contextWithRequestID(req.Context(), newRequestID()))
	rlog := reqLog(r.log, req.Context())

	peer := r.peerFor(req)
	decision := r.decisionFor(req, peer)
	r.logDecision(rlog, req, peer, decision)

	// CONNECT is hijacked by the forwarder; instrumentation lives in
	// tunnelExternal/tunnelViaPeer (connect_attempts_total + canonical
	// tunnel_done log). Wrapping ResponseWriter here would also mask
	// the http.Hijacker interface that the hijack path needs.
	if req.Method == http.MethodConnect {
		r.dispatch(w, req, peer)
		return
	}

	start := time.Now()
	rec := newStatusRecorder(w)
	r.dispatch(rec, req, peer)

	r.metrics.RecordRequest(req.Context(), req.Method, metrics.StatusClass(rec.status), decision)
	// Canonical decision log per finished HTTP request. Mirror of
	// tunnel_done in forwarder.go: one structured line containing every
	// field needed for post-mortem and audit.
	rlog.Info("http_done",
		slog.String("event", "http"),
		slog.String("method", req.Method),
		slog.String("host", req.Host),
		slog.String("entity_id", req.Header.Get(EntityHeader)),
		slog.String("decision", decision),
		slog.String("peer", peer),
		slog.Int("status", rec.status),
		slog.Duration("duration", time.Since(start)),
	)
}

func (r *Router) dispatch(w http.ResponseWriter, req *http.Request, peer string) {
	if peer != "" {
		r.forwarder.ForwardToPeer(peer, w, req)
		return
	}
	r.forwarder.ForwardToExternal(w, req)
}

// decisionFor returns the canonical routing-decision string for this
// request. Kept separate so logDecision and metric labels never drift.
func (r *Router) decisionFor(req *http.Request, peer string) string {
	switch {
	case req.Header.Get(HopHeader) != "":
		return decisionHopLocal
	case peer != "":
		return decisionPeer
	default:
		return decisionLocal
	}
}

// logDecision emits one line per incoming request with the routing
// verdict. `decision` is one of:
//
//	local      -> processed by this node
//	peer       -> relayed to another node (peer address in "peer" field)
//	hop-local  -> already forwarded once (HopHeader set), kept local to
//	              avoid ping-pong loops during ring convergence
func (r *Router) logDecision(l *slog.Logger, req *http.Request, peer, decision string) {
	l.Info("proxy request",
		slog.String("method", req.Method),
		slog.String("host", req.Host),
		slog.String("entity_id", req.Header.Get(EntityHeader)),
		slog.String("decision", decision),
		slog.String("peer", peer),
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

// statusRecorder is a minimal ResponseWriter wrapper that captures the
// final status code so the router can emit it on the canonical log
// line and as a Prometheus label. Used only on the non-CONNECT path —
// CONNECT hijacks the conn and never writes a status through the
// ResponseWriter at all.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	// Default to 200 because http.ResponseWriter.Write implicitly
	// writes a 200 header when WriteHeader was never called. Without
	// this default, a successful Write-only response would log status=0.
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush proxies http.Flusher when the underlying writer supports it.
// httputil.ReverseProxy expects Flush to work for streaming responses,
// so a wrapper that swallows it would cause buffered upstreams to
// stall the client.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
