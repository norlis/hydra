package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/norlis/httpgate/logging"
	"github.com/norlis/httpgate/trace"
	"github.com/norlis/hydra/internal/proxy/conntrack"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/pkg/logger"
)

// HopHeader marks a request that has already been forwarded once by a
// peer. Peers that see this header must process the request locally
// instead of rehashing, to avoid ping-pong loops during ring
// convergence windows.
const HopHeader = "X-Hydra-Hop"

// connectEstablished is the raw response sent to a CONNECT client once
// the tunnel is ready. We write it directly to the hijacked socket to
// bypass Go's http.Server which otherwise injects framing headers
// (Content-Length, Transfer-Encoding: chunked, Date) that are invalid
// on CONNECT 2xx responses and break strict HTTP/1.1 clients — in
// particular Go's own http.ReadResponse, which would try to parse the
// tunnel bytes as a chunked body.
var connectEstablished = []byte("HTTP/1.1 200 Connection established\r\n\r\n")

// defaultPeerHandshakeTimeout bounds how long a peer may take to answer
// the CONNECT or HTTP head of a relayed request. A peer that accepts the
// TCP connection and never replies would otherwise hold the client, a
// tunnel slot and a goroutine until the kernel keepalive gives up.
const defaultPeerHandshakeTimeout = 10 * time.Second

// Short enough that an unreachable peer fails before the client gives up,
// leaving time to fall back to a direct tunnel.
const peerDialTimeout = 3 * time.Second

// Structured failure fields for log-based failure analysis
// (stats count() by host, error_source, deny_reason).
const (
	keyErrorSource = "error_source"
	keyDenyReason  = "deny_reason"

	errSourcePolicy   = "policy"   // request refused by proxy policy
	errSourceUpstream = "upstream" // origin unreachable or errored
	errSourceProxy    = "proxy"    // internal proxy fault

	denyReasonPort = "port_not_allowed"
	denyReasonAuth = "auth_required"
)

// ForwarderConfig holds the per-node policy a DualForwarder applies.
type ForwarderConfig struct {
	// StripHeaders are extra request headers removed before writing to the
	// upstream (internet) service. Control-plane and RFC 7230 hop-by-hop
	// headers are always stripped regardless.
	StripHeaders []string
	// AllowedPorts caps which destination ports CONNECT may target; an
	// empty list means no restriction.
	AllowedPorts []int
	// IdleTimeout force-closes a tunnel without traffic in either direction
	// for this long; zero disables the watchdog.
	IdleTimeout time.Duration
	// PeerAuth is the Proxy-Authorization value sent on every hop to a
	// peer so the peer can authenticate this node like any client. Empty
	// when the cluster runs without proxy authentication.
	PeerAuth string
}

// ForwarderDeps groups the shared collaborators injected into every
// per-interface forwarder. Any of them may be nil.
type ForwarderDeps struct {
	Classifier  *ipcheck.Classifier // SSRF policy applied post-DNS, pre-SYN
	Tracker     *conntrack.Tracker  // registry of live tunnels for shutdown
	TunnelLimit *limiter.Tunnel     // global cap on concurrent tunnels
	Metrics     *metrics.Metrics    // data-plane instruments (nil-safe)
}

// DualForwarder implements Forwarder for both HTTP and CONNECT methods,
// binding every outbound TCP socket to a specific local interface.
type DualForwarder struct {
	iface        topology.NetworkInterface
	dialer       *net.Dialer
	peerDialer   *net.Dialer // peer dial without IP classification
	transport    *http.Transport
	stripHeaders []string
	allowedPorts map[int]struct{}
	classifier   *ipcheck.Classifier
	tracker      *conntrack.Tracker
	tunnelLimit  *limiter.Tunnel
	idleTimeout  time.Duration
	// handshakeTimeout caps the wait for a peer's response head.
	handshakeTimeout time.Duration
	peerAuth         string
	metrics          *metrics.Metrics
	log              *slog.Logger
}

// NewDualForwarder creates a forwarder pinned to iface. Every outbound
// TCP socket (tunnels + HTTP) is bound to iface.PrivateIP. The
// classifier validates the resolved destination IP via Dialer.Control
// (post-DNS, pre-SYN); peer dials skip it because peers are trusted.
func NewDualForwarder(iface topology.NetworkInterface, cfg ForwarderConfig, deps ForwarderDeps, log *slog.Logger) *DualForwarder {
	local, _ := net.ResolveTCPAddr("tcp", iface.PrivateIP+":0")

	dialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if deps.Classifier == nil {
				return nil
			}
			return deps.Classifier.Validate(network, address)
		},
	}
	peerDialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   peerDialTimeout,
		KeepAlive: 30 * time.Second,
	}

	t := cleanhttp.DefaultTransport()
	t.DialContext = dialer.DialContext

	ports := make(map[int]struct{}, len(cfg.AllowedPorts))
	for _, p := range cfg.AllowedPorts {
		ports[p] = struct{}{}
	}

	return &DualForwarder{
		iface:        iface,
		dialer:       dialer,
		peerDialer:   peerDialer,
		transport:    t,
		stripHeaders: cfg.StripHeaders,
		allowedPorts: ports,
		classifier:   deps.Classifier,
		tracker:      deps.Tracker,
		tunnelLimit:  deps.TunnelLimit,
		idleTimeout:  cfg.IdleTimeout,

		handshakeTimeout: defaultPeerHandshakeTimeout,
		peerAuth:         cfg.PeerAuth,
		metrics:          deps.Metrics,
		log:              log.With(slog.String("iface", iface.Name), slog.String("ip", iface.PrivateIP)),
	}
}

// Serve relays r. With peer == "" the request goes straight to its
// destination; otherwise it is relayed through the hydra peer listening at
// peer (host:port). CONNECT becomes a spliced tunnel, anything else is
// proxied as HTTP. A peer that cannot be reached or refuses a CONNECT is
// bypassed transparently with a direct tunnel, before any byte reaches
// the client.
func (f *DualForwarder) Serve(w http.ResponseWriter, r *http.Request, route Route) {
	if r.Method == http.MethodConnect {
		f.connect(w, r, route)
		return
	}
	if route.Peer != "" {
		f.httpViaPeer(w, r, route.Peer)
		return
	}
	f.httpExternal(w, r)
}

// -- CONNECT handling -------------------------------------------------

// connect runs the whole CONNECT pipeline: port policy, tunnel slot,
// remote socket (via peer with direct fallback, or direct), instrumentation,
// hijack, 200 to the client and splice until either side closes.
func (f *DualForwarder) connect(w http.ResponseWriter, r *http.Request, route Route) {
	setupStart := time.Now()
	ctx := r.Context()

	if !f.portAllowed(r.Host) {
		f.log.WarnContext(ctx, "tunnel port denied",
			slog.String(keyServerAddress, r.Host),
			slog.String(keyErrorSource, errSourcePolicy),
			slog.String(keyDenyReason, denyReasonPort))
		f.metrics.RecordDenied(ctx, "port")
		http.Error(w, "destination port not allowed", http.StatusForbidden)
		return
	}

	// Reserve a tunnel slot before any dial so an over-cap node fails
	// fast and never burns sockets it can't keep open. Released by the
	// InstrumentedConn close callback once the remote socket exists.
	if !f.acquireTunnelSlot(ctx, w) {
		return
	}
	releaseSlot := true
	defer func() {
		if releaseSlot && f.tunnelLimit != nil {
			f.tunnelLimit.Release()
		}
	}()

	entity := r.Header.Get(EntityHeader)
	rawRemote, pending, via, ok := f.dialRemote(ctx, w, r.Host, route.Peer, entity)
	if !ok {
		return
	}

	// Wrap the remote conn BEFORE hijacking the client so byte counters,
	// lifetime and the slot release are attributed to this tunnel from
	// the moment the upstream socket exists.
	remote := f.newInstrumented(ctx, rawRemote, tunnelInfo{dstHost: r.Host, via: via, decision: route.Decision, entity: entity})
	releaseSlot = false
	defer func() { _ = remote.Close() }()

	client, clientBuf, err := hijackConn(w)
	if err != nil {
		f.log.ErrorContext(ctx, "hijack failed", slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		f.metrics.RecordError(ctx, "hijack")
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(connectEstablished); err != nil {
		f.metrics.RecordError(ctx, "handshake")
		return
	}

	f.metrics.RecordTunnelStarted(ctx, time.Since(setupStart))

	// Both pre-buffered sides feed the splice so bytes that arrived in
	// the same TCP segment as the headers are not dropped.
	var remoteSrc io.Reader = remote
	if len(pending) > 0 {
		remoteSrc = io.MultiReader(bytes.NewReader(pending), remote)
	}
	splice(client, remote, readerOf(clientBuf), remoteSrc)
}

// dialRemote obtains the remote end of a tunnel to host. With a peer it
// first tries dialViaPeer and falls back to a direct dial when the peer is
// unusable. On a direct-dial failure it answers the client (403 for a
// policy denial, 502 otherwise) and returns ok == false. via is the peer
// address actually used, or "" for a direct tunnel; pending holds any
// bytes the peer sent right after its 200 and is nil for direct tunnels.
func (f *DualForwarder) dialRemote(ctx context.Context, w http.ResponseWriter, host, peer, entity string) (remote net.Conn, pending []byte, via string, ok bool) {
	if peer != "" {
		if conn, pending, ok := f.dialViaPeer(ctx, peer, host, entity); ok {
			return conn, pending, peer, true
		}
	}
	conn, err := f.dialer.DialContext(ctx, "tcp", host)
	if err == nil {
		return conn, nil, "", true
	}
	if ipcheck.IsDenied(err) {
		f.log.WarnContext(ctx, "tunnel ip denied",
			slog.String(keyServerAddress, host),
			slog.String(keyErrorSource, errSourcePolicy),
			slog.String(keyDenyReason, ipcheck.Reason(err)),
			logger.Err(err))
		f.metrics.RecordDenied(ctx, "ip")
		http.Error(w, denyBody(host, err), http.StatusForbidden)
		return nil, nil, "", false
	}
	f.log.ErrorContext(ctx, "tunnel dial failed",
		slog.String(keyServerAddress, host),
		slog.String(keyErrorSource, errSourceUpstream),
		logger.Err(err))
	f.metrics.RecordError(ctx, "dial")
	http.Error(w, "connection failed", http.StatusBadGateway)
	return nil, nil, "", false
}

// newInstrumented wraps a remote conn so byte counts, duration, idle
// watchdog, and a canonical decision log are tied to its lifetime.
// peer is empty for direct upstream tunnels and set to the peer's
// proxy address when the tunnel is relayed via another node; the
// metric labels are the same in both cases (active_tunnels and
// bytes_total intentionally have no peer/direct dimension to keep
// cardinality stable), the distinction lives only in the log line.
//
// The close callback also releases the tunnel-limit slot acquired
// before the dial — keeping that release here means it fires exactly
// once per tunnel even if the caller forgets to defer it.
// tunnelInfo is what the completion log reports about one tunnel. via is
// the peer actually used: empty after a fallback to a direct dial, which is
// how the log tells a peer decision that could not be honoured.
type tunnelInfo struct {
	dstHost, via, decision, entity string
}

func (f *DualForwarder) newInstrumented(ctx context.Context, c net.Conn, t tunnelInfo) net.Conn {
	// If nothing observes the conn, skip the wrapper — but only when
	// no idle watchdog is configured either, since the watchdog itself
	// is implemented inside InstrumentedConn.
	if f.tracker == nil && f.metrics == nil && f.tunnelLimit == nil && f.idleTimeout <= 0 {
		return c
	}
	onClose := func(s conntrack.Stats) {
		if f.tunnelLimit != nil {
			f.tunnelLimit.Release()
		}
		// Close fires from whichever goroutine drove the tear-down
		// (splice, deferred Close, or tracker.CloseAll on shutdown).
		// None of those carry a useful request context, so use
		// Background — the metrics SDK does not require trace context.
		f.metrics.RecordTunnelEnded(context.Background(), s.Duration, s.BytesOut, s.BytesIn)
		// Canonical completion log: one line per finished tunnel with every
		// field needed for billing, audit, and debug. Logged via the request
		// ctx so trace_id/span_id inject despite the async teardown.
		f.log.InfoContext(ctx, "request completed",
			slog.String(logging.KeyHTTPRequestMethod, http.MethodConnect),
			slog.String(keyServerAddress, t.dstHost),
			slog.String(keyEntityID, t.entity),
			slog.String("peer", t.via),
			slog.String("decision", t.decision),
			slog.Int64("bytes_d2c", s.BytesIn),
			slog.Int64("bytes_c2d", s.BytesOut),
			slog.Int64(logging.KeyEventDuration, s.Duration.Nanoseconds()),
		)
	}
	wrapped := conntrack.WrapWithIdle(c, f.idleTimeout, onClose)
	if f.tracker != nil {
		f.tracker.Add(wrapped)
	}
	return wrapped
}

// acquireTunnelSlot reserves capacity from the global tunnel limiter,
// returning false (and writing 503 to w) when the cap is hit. A nil
// limiter or zero cap is treated as unlimited.
func (f *DualForwarder) acquireTunnelSlot(ctx context.Context, w http.ResponseWriter) bool {
	if f.tunnelLimit == nil {
		return true
	}
	if f.tunnelLimit.Acquire() {
		return true
	}
	f.metrics.RecordDenied(ctx, "tunnel_limit")
	http.Error(w, "tunnel limit reached", http.StatusServiceUnavailable)
	return false
}

// hostOnly returns the host without its port, if present.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// denyBody renders the client-facing 403 body for an egress policy denial,
// in the style of a forward-proxy deny notice. The resolved IP and granular
// reason stay in the logs; the client sees only the requested host and the
// rule category, so the proxy does not leak internal DNS resolution.
func denyBody(host string, err error) string {
	rule := "default deny policy"
	var de *ipcheck.DeniedError
	if errors.As(err, &de) && de.Reason == ipcheck.DenyConfigured {
		rule = "configured deny rule"
	}
	return fmt.Sprintf("Egress proxying is denied to host '%s': %s.", hostOnly(host), rule)
}

// dialViaPeer opens a TCP connection to peer, negotiates a CONNECT for
// host and returns the connection positioned at the first tunnel byte.
// ok is false (and everything is logged and closed) when the peer could
// not be reached, did not answer within the handshake timeout or refused:
// callers fall back to a direct tunnel. The peer connection is closed if
// the client's context ends during the handshake.
func (f *DualForwarder) dialViaPeer(ctx context.Context, peer, host, entity string) (net.Conn, []byte, bool) {
	rawRemote, err := f.peerDialer.DialContext(ctx, "tcp", peer)
	if err != nil {
		f.log.WarnContext(ctx, "peer dial failed, falling back", slog.String("peer", peer), logger.Err(err))
		return nil, nil, false
	}
	// After the hijack the request context is only cancelled when the
	// handler returns, so this hook is harmless once the tunnel is up.
	stopWatch := context.AfterFunc(ctx, func() { _ = rawRemote.Close() })
	defer stopWatch()

	fwd, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+host, http.NoBody)
	if err != nil {
		_ = rawRemote.Close()
		return nil, nil, false
	}
	fwd.Host = host
	fwd.Header.Set(HopHeader, "1")
	if entity != "" {
		// Lets the owner's completion log name the entity it egressed for.
		fwd.Header.Set(EntityHeader, entity)
	}
	f.setPeerAuth(fwd.Header)
	setPeerTrace(fwd.Header, ctx)

	if err := fwd.Write(rawRemote); err != nil {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer write failed, falling back", slog.String("peer", peer), logger.Err(err))
		return nil, nil, false
	}

	// Parse the peer's CONNECT response manually. http.ReadResponse
	// would latch onto any Transfer-Encoding/Content-Length the peer's
	// http.Server injects and then try to read a body out of the
	// tunnel — which of course isn't a body at all.
	remoteBuf := bufio.NewReader(rawRemote)
	_ = rawRemote.SetReadDeadline(time.Now().Add(f.handshakeTimeout))
	status, err := readConnectStatus(remoteBuf)
	_ = rawRemote.SetReadDeadline(time.Time{})
	if err != nil {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer response failed, falling back",
			slog.String("peer", peer), logger.Err(err))
		return nil, nil, false
	}
	if status != http.StatusOK {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer rejected tunnel, falling back",
			slog.String("peer", peer), slog.Int(logging.KeyHTTPResponseStatusCode, status))
		return nil, nil, false
	}
	// Hand back only the bytes already buffered: reading the rest through
	// this bufio.Reader would bypass the instrumented conn, losing the d2c
	// byte count and the idle watchdog's view of downstream traffic.
	pending, _ := remoteBuf.Peek(remoteBuf.Buffered())
	return rawRemote, bytes.Clone(pending), true
}

// -- HTTP handling ----------------------------------------------------

func (f *DualForwarder) httpExternal(w http.ResponseWriter, r *http.Request) {
	rp := &httputil.ReverseProxy{
		// Rewrite (not the deprecated Director) so the proxy never adds
		// X-Forwarded-*: a forward proxy must not reveal its clients to
		// the origin.
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			if out.URL.Host == "" {
				out.URL.Host = pr.In.Host
			}
			if out.URL.Scheme == "" {
				out.URL.Scheme = "http"
			}
			f.stripControlHeaders(out)
		},
		Transport: f.transport,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if ipcheck.IsDenied(err) {
				f.log.WarnContext(req.Context(), "http ip denied",
					slog.String(keyServerAddress, req.Host),
					slog.String(keyErrorSource, errSourcePolicy),
					slog.String(keyDenyReason, ipcheck.Reason(err)),
					logger.Err(err))
				f.metrics.RecordDenied(req.Context(), "ip")
				http.Error(w, denyBody(req.Host, err), http.StatusForbidden)
				return
			}
			f.log.ErrorContext(req.Context(), "upstream error",
				slog.String(keyServerAddress, req.Host),
				slog.String(keyErrorSource, errSourceUpstream),
				logger.Err(err))
			f.metrics.RecordError(req.Context(), "upstream")
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// hopByHopHeaders are the connection-scoped headers defined by RFC 7230
// §6.1. A proxy MUST NOT forward them to the upstream.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// stripControlHeaders removes hydra-internal routing headers, RFC 7230
// hop-by-hop headers, and any user-configured extras. Called only on
// the external path so peer-to-peer hops preserve everything they need.
func (f *DualForwarder) stripControlHeaders(req *http.Request) {
	// Headers listed in Connection are also hop-by-hop per the RFC.
	if c := req.Header.Get("Connection"); c != "" {
		for name := range strings.SplitSeq(c, ",") {
			req.Header.Del(strings.TrimSpace(name))
		}
	}
	for _, h := range hopByHopHeaders {
		req.Header.Del(h)
	}
	req.Header.Del(HopHeader)
	req.Header.Del(EntityHeader)
	req.Header.Del(trace.Header) // internal trace: never leaks to external origins
	for _, h := range f.stripHeaders {
		req.Header.Del(h)
	}
}

// setPeerAuth replaces any Proxy-Authorization with this node's own
// credential for the peer, or removes the header when the cluster runs
// without proxy authentication.
func (f *DualForwarder) setPeerAuth(h http.Header) {
	if f.peerAuth == "" {
		h.Del("Proxy-Authorization")
		return
	}
	h.Set("Proxy-Authorization", f.peerAuth)
}

// setPeerTrace propagates the request's trace to a peer hop via the W3C
// traceparent header. No-op when the context carries no trace. Peer hops only
// — the external forward strips traceparent (stripControlHeaders).
func setPeerTrace(h http.Header, ctx context.Context) {
	if tc, ok := trace.FromContext(ctx); ok {
		h.Set(trace.Header, tc.Traceparent())
	}
}

// portAllowed returns true if the host:port pair targets a destination
// port present in the allowlist. Empty allowlist means "no restriction".
func (f *DualForwarder) portAllowed(hostPort string) bool {
	if len(f.allowedPorts) == 0 {
		return true
	}
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	_, ok := f.allowedPorts[p]
	return ok
}

// httpViaPeer writes the request (in forward-proxy format, with an
// absolute URL) directly to the peer's proxy port, then streams the
// response back to the client.
func (f *DualForwarder) httpViaPeer(w http.ResponseWriter, r *http.Request, peer string) {
	ctx := r.Context()
	conn, err := f.peerDialer.DialContext(ctx, "tcp", peer)
	if err != nil {
		f.log.WarnContext(ctx, "peer dial failed",
			slog.String("peer", peer), slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		http.Error(w, "peer unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = conn.Close() }()
	// Tie the peer connection to the client's request: when the client
	// cancels or disconnects, closing conn unblocks ReadResponse / Copy.
	stopWatch := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopWatch()

	// Proxy-Authorization is hop-by-hop (RFC 7230 §6.1): the client's
	// credential never travels on; the peer authenticates this node.
	f.setPeerAuth(r.Header)
	r.Header.Set(HopHeader, "1")
	setPeerTrace(r.Header, ctx)
	if err := r.WriteProxy(conn); err != nil {
		f.log.WarnContext(ctx, "peer write failed",
			slog.String("peer", peer), slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		http.Error(w, "peer forward failed", http.StatusBadGateway)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(f.handshakeTimeout))
	resp, err := http.ReadResponse(bufio.NewReader(conn), r)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		f.log.WarnContext(ctx, "peer response failed",
			slog.String("peer", peer), slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		http.Error(w, "peer forward failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// -- helpers ----------------------------------------------------------

var errNotHijackable = errors.New("response writer does not support hijacking")

// hijackConn takes over the underlying TCP conn for the response. It
// returns both the raw conn and the bufio.ReadWriter that the http
// server may have already read into — callers must use the bufio as
// the read source to avoid losing pre-buffered bytes.
func hijackConn(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errNotHijackable
	}
	conn, buf, err := h.Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack: %w", err)
	}
	return conn, buf, nil
}

// readConnectStatus parses a CONNECT-response status line and consumes
// its header block, leaving the bufio.Reader positioned right at the
// first tunnel byte. Unlike http.ReadResponse it never tries to build
// a Body — the tunnel has none.
func readConnectStatus(br *bufio.Reader) (int, error) {
	tp := textproto.NewReader(br)
	line, err := tp.ReadLine()
	if err != nil {
		return 0, fmt.Errorf("read status line: %w", err)
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return 0, fmt.Errorf("malformed status line: %q", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("malformed status code %q: %w", parts[1], err)
	}
	if _, err := tp.ReadMIMEHeader(); err != nil {
		return code, fmt.Errorf("read headers: %w", err)
	}
	return code, nil
}

// readerOf returns the read half of a bufio.ReadWriter, or nil if the
// ReadWriter itself is nil (defensive — Go's Hijack always returns a
// non-nil one, but splice handles nil gracefully anyway).
func readerOf(rw *bufio.ReadWriter) io.Reader {
	if rw == nil {
		return nil
	}
	return rw.Reader
}

// splice copies bytes in both directions between client and remote.
// It returns only after both goroutines have exited: the first one to
// finish force-closes both conns, unblocking the other, so no goroutine
// is left waiting on a half-dead tunnel.
//
// clientBuf / remoteBuf, when non-nil, are consumed as the read source
// before falling through to the raw conn — this preserves bytes that
// were pre-buffered during header parsing on either end.
func splice(client, remote net.Conn, clientBuf, remoteBuf io.Reader) {
	if clientBuf == nil {
		clientBuf = client
	}
	if remoteBuf == nil {
		remoteBuf = remote
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, clientBuf)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, remoteBuf)
		done <- struct{}{}
	}()

	<-done
	_ = client.Close()
	_ = remote.Close()
	<-done
}
