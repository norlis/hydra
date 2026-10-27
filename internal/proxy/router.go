package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/norlis/httpgate/logging"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/internal/topology"
)

// EntityHeader is the per-request hint the router uses to place the
// request on the consistent hash ring. Callers set it to a stable
// identifier (tenant id, user id, URL path, etc.).
const EntityHeader = "X-Entity-ID"

// Log field keys for standard concepts not exported by httpgate/logging
// (server.address) and recurring Hydra domain fields.
const (
	keyServerAddress = "server.address"
	keyEntityID      = "entity.id"
)

// Routing decisions. Kept as a closed enum so they survive as a low-
// cardinality Prometheus label.
const (
	decisionLocal    = "local"
	decisionPeer     = "peer"
	decisionHandoff  = "handoff"
	decisionHopLocal = "hop-local"
)

// EndpointResolver is the one method the router needs from the hash ring:
// which endpoint owns an entity. *cluster.Ring satisfies it; tests use a
// fake. A nil endpoint means "ring empty or entity unowned": process locally.
type EndpointResolver interface {
	EndpointFor(entityID string) *cluster.Endpoint
}

// Router is the data-plane http.Handler. One instance per bound interface. It
// decides whether the request belongs here (local processing) or to
// another peer (peer forwarding) and delegates the actual bytes to
// the Forwarder.
type Router struct {
	iface     topology.NetworkInterface
	ring      EndpointResolver
	forwarder Forwarder
	metrics   *metrics.Metrics
	log       *slog.Logger
}

func NewRouter(
	iface topology.NetworkInterface,
	ring EndpointResolver,
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
	// The trace context lives in req.Context() (set by the TraceContext
	// middleware); *Context log calls inject trace_id/span_id from it.
	route := r.route(req)
	r.logDecision(req, route)

	// CONNECT is hijacked by the forwarder; instrumentation lives in its
	// connect pipeline (connect_attempts + canonical completion log).
	// Wrapping ResponseWriter here would also mask the http.Hijacker
	// interface that the hijack path needs.
	if req.Method == http.MethodConnect {
		r.forwarder.Serve(w, req, route)
		return
	}

	start := time.Now()
	rec := newStatusRecorder(w)
	r.forwarder.Serve(rec, req, route)

	r.metrics.RecordRequest(req.Context(), req.Method, metrics.StatusClass(rec.status), route.Decision)
	// Canonical decision log per finished HTTP request. Mirror of
	// tunnel_done in forwarder.go: one structured line containing every
	// field needed for post-mortem and audit.
	r.log.InfoContext(req.Context(), "request completed",
		slog.String(logging.KeyHTTPRequestMethod, req.Method),
		slog.String(keyServerAddress, req.Host),
		slog.String(keyEntityID, req.Header.Get(EntityHeader)),
		slog.String("decision", route.Decision),
		slog.String("peer", route.Peer),
		slog.Int(logging.KeyHTTPResponseStatusCode, rec.status),
		slog.Int64(logging.KeyEventDuration, time.Since(start).Nanoseconds()),
	)
}

// logDecision emits one line per incoming request with the routing
// verdict. `decision` is one of:
//
//	local      -> processed by this node
//	peer       -> relayed to another node (peer address in "peer" field)
//	handoff    -> relayed to another interface of this node, so the entity
//	              always egresses through the interface that owns it
//	hop-local  -> already forwarded once (HopHeader set), kept local to
//	              avoid ping-pong loops during ring convergence
func (r *Router) logDecision(req *http.Request, route Route) {
	r.log.DebugContext(req.Context(), "proxy request",
		slog.String(logging.KeyHTTPRequestMethod, req.Method),
		slog.String(keyServerAddress, req.Host),
		slog.String(keyEntityID, req.Header.Get(EntityHeader)),
		slog.String("decision", route.Decision),
		slog.String("peer", route.Peer),
	)
}

// route resolves where the request egresses. It stays local when it was
// already relayed once (loop prevention), carries no entity, or the ring
// has no owner for it.
func (r *Router) route(req *http.Request) Route {
	if req.Header.Get(HopHeader) != "" {
		return Route{Decision: decisionHopLocal}
	}
	entity := req.Header.Get(EntityHeader)
	if entity == "" || r.ring == nil {
		return Route{Decision: decisionLocal}
	}
	ep := r.ring.EndpointFor(entity)
	switch {
	case ep == nil, ep.IsSelf && ep.Iface == r.iface.Name:
		return Route{Decision: decisionLocal}
	case ep.IsSelf:
		return Route{Peer: ep.ProxyAddr, Decision: decisionHandoff}
	default:
		return Route{Peer: ep.ProxyAddr, Decision: decisionPeer}
	}
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
	return s.ResponseWriter.Write(b) //nolint:wrapcheck // pass-through of the wrapped http.ResponseWriter; wrapping would break the ResponseWriter contract
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
