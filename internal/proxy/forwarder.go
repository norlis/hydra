package proxy

import (
	"bufio"
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
	metrics      *metrics.Metrics
	log          *slog.Logger
}

// NewDualForwarder creates a forwarder pinned to iface. Every outbound
// TCP socket (tunnels + HTTP) is bound to iface.PrivateIP.
//
// stripHeaders is the extra list of headers to remove before writing
// to the upstream (internet) service. Control-plane headers
// (HopHeader, EntityHeader) and RFC 7230 hop-by-hop headers are always
// stripped on external forward regardless of this list.
//
// allowedPorts caps which destination ports CONNECT may target;
// requests to other ports are rejected with 403 before any dial.
//
// classifier validates the resolved destination IP via Dialer.Control
// (post-DNS, pre-SYN), preventing SSRF to RFC1918, IMDS, and friends.
func NewDualForwarder(
	iface topology.NetworkInterface,
	stripHeaders []string,
	allowedPorts []int,
	classifier *ipcheck.Classifier,
	tracker *conntrack.Tracker,
	tunnelLimit *limiter.Tunnel,
	idleTimeout time.Duration,
	mtr *metrics.Metrics,
	log *slog.Logger,
) *DualForwarder {
	local, _ := net.ResolveTCPAddr("tcp", iface.PrivateIP+":0")

	// External dialer: validates destination IP via Control before connect.
	dialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if classifier == nil {
				return nil
			}
			return classifier.Validate(network, address)
		},
	}
	// Peer dialer: NO classification (we trust our own peers).
	peerDialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	t := cleanhttp.DefaultTransport()
	t.DialContext = dialer.DialContext

	ports := make(map[int]struct{}, len(allowedPorts))
	for _, p := range allowedPorts {
		ports[p] = struct{}{}
	}

	return &DualForwarder{
		iface:        iface,
		dialer:       dialer,
		peerDialer:   peerDialer,
		transport:    t,
		stripHeaders: stripHeaders,
		allowedPorts: ports,
		classifier:   classifier,
		tracker:      tracker,
		tunnelLimit:  tunnelLimit,
		idleTimeout:  idleTimeout,
		metrics:      mtr,
		log:          log.With(slog.String("iface", iface.Name), slog.String("ip", iface.PrivateIP)),
	}
}

// ForwardToExternal sends the request to its original destination. For
// CONNECT it opens a raw TCP tunnel to r.Host; for plain HTTP it acts
// as a forward proxy using the pinned transport.
func (f *DualForwarder) ForwardToExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.tunnelExternal(w, r)
		return
	}
	f.httpExternal(w, r)
}

// ForwardToPeer relays the request through another hydra node. For
// CONNECT it negotiates a CONNECT on the peer's proxy port and splices
// the tunnels; for plain HTTP it rewrites the request in forward-proxy
// form and sends it via the peer. Any peer-side failure during CONNECT
// negotiation falls back to ForwardToExternal transparently (before any
// bytes reach the client).
func (f *DualForwarder) ForwardToPeer(peer string, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		if f.tunnelViaPeer(w, r, peer) {
			return
		}
		f.tunnelExternal(w, r)
		return
	}
	f.httpViaPeer(w, r, peer)
}

// -- CONNECT handling -------------------------------------------------

func (f *DualForwarder) tunnelExternal(w http.ResponseWriter, r *http.Request) {
	setupStart := time.Now()
	ctx := r.Context()

	if !f.portAllowed(r.Host) {
		f.log.WarnContext(ctx, "tunnel port denied",
			slog.String(keyServerAddress, r.Host),
			slog.String(keyErrorSource, errSourcePolicy),
			slog.String(keyDenyReason, denyReasonPort))
		f.recordDeny(ctx, "port")
		http.Error(w, "destination port not allowed", http.StatusForbidden)
		return
	}

	// Reserve a tunnel slot before the dial so an over-cap node fails
	// fast and never burns sockets it can't keep open. Released by the
	// InstrumentedConn close callback so the count drops exactly when
	// the tunnel does.
	if !f.acquireTunnelSlot(ctx, w) {
		return
	}
	releaseSlot := true
	defer func() {
		// If we never made it to newInstrumented (which transfers the
		// release responsibility to the close callback), release here.
		if releaseSlot && f.tunnelLimit != nil {
			f.tunnelLimit.Release()
		}
	}()

	rawRemote, err := f.dialer.DialContext(ctx, "tcp", r.Host)
	if err != nil {
		if ipcheck.IsDenied(err) {
			f.log.WarnContext(ctx, "tunnel ip denied",
				slog.String(keyServerAddress, r.Host),
				slog.String(keyErrorSource, errSourcePolicy),
				slog.String(keyDenyReason, ipcheck.Reason(err)),
				logger.Err(err))
			f.recordDeny(ctx, "ip")
			http.Error(w, denyBody(r.Host, err), http.StatusForbidden)
			return
		}
		f.log.ErrorContext(ctx, "tunnel dial failed",
			slog.String(keyServerAddress, r.Host),
			slog.String(keyErrorSource, errSourceUpstream),
			logger.Err(err))
		f.recordError(ctx, "dial")
		http.Error(w, "connection failed", http.StatusBadGateway)
		return
	}

	// Wrap the remote conn with instrumentation BEFORE hijacking the
	// client, so byte counters and lifetime are attributed to this
	// tunnel from the moment the upstream socket exists.
	remote := f.newInstrumented(rawRemote, r.Host, "", ctx)
	// Past this point, the tunnel-slot release lives in the
	// InstrumentedConn's close callback. Suppress the function-level
	// release so we don't double-decrement.
	releaseSlot = false
	defer func() { _ = remote.Close() }()

	client, clientBuf, err := hijackConn(w)
	if err != nil {
		f.log.ErrorContext(ctx, "hijack failed", slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		f.recordError(ctx, "hijack")
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(connectEstablished); err != nil {
		f.recordError(ctx, "handshake")
		return
	}

	f.metrics.RecordTunnelStarted(ctx, time.Since(setupStart))

	splice(client, remote, readerOf(clientBuf), nil)
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
func (f *DualForwarder) newInstrumented(c net.Conn, dstHost, peer string, ctx context.Context) net.Conn {
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
		decision := decisionLocal
		if peer != "" {
			decision = decisionPeer
		}
		// Canonical completion log: one line per finished tunnel with every
		// field needed for billing, audit, and debug. Logged via the request
		// ctx so trace_id/span_id inject despite the async teardown.
		f.log.InfoContext(ctx, "request completed",
			slog.String(logging.KeyHTTPRequestMethod, http.MethodConnect),
			slog.String(keyServerAddress, dstHost),
			slog.String("peer", peer),
			slog.String("decision", decision),
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
	f.recordDeny(ctx, "tunnel_limit")
	http.Error(w, "tunnel limit reached", http.StatusServiceUnavailable)
	return false
}

// recordDeny / recordError are thin wrappers around the metrics facade
// kept for readability at call sites. The facade is itself nil-safe,
// so no extra guard is needed here.
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

func (f *DualForwarder) recordDeny(ctx context.Context, reason string) {
	f.metrics.RecordDenied(ctx, reason)
}

func (f *DualForwarder) recordError(ctx context.Context, stage string) {
	f.metrics.RecordError(ctx, stage)
}

// tunnelViaPeer negotiates a CONNECT through peer before acking the
// client. Returns true if the tunnel was handled end-to-end, false if
// the peer couldn't be reached and we never wrote anything to the
// client (so the caller can fall back).
func (f *DualForwarder) tunnelViaPeer(w http.ResponseWriter, r *http.Request, peer string) bool {
	setupStart := time.Now()

	ctx := r.Context()

	// Same admission control as the direct branch. Acquired here so a
	// fully-loaded node short-circuits before any peer I/O.
	if !f.acquireTunnelSlot(ctx, w) {
		return true
	}
	releaseSlot := true
	defer func() {
		if releaseSlot && f.tunnelLimit != nil {
			f.tunnelLimit.Release()
		}
	}()

	rawRemote, err := f.peerDialer.DialContext(ctx, "tcp", peer)
	if err != nil {
		f.log.WarnContext(ctx, "peer dial failed, falling back", slog.String("peer", peer), logger.Err(err))
		return false
	}

	fwd, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+r.Host, http.NoBody)
	if err != nil {
		_ = rawRemote.Close()
		return false
	}
	fwd.Host = r.Host
	fwd.Header.Set(HopHeader, "1")
	setPeerTrace(fwd.Header, ctx)

	if err := fwd.Write(rawRemote); err != nil {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer write failed, falling back", slog.String("peer", peer), logger.Err(err))
		return false
	}

	// Parse the peer's CONNECT response manually. http.ReadResponse
	// would latch onto any Transfer-Encoding/Content-Length the peer's
	// http.Server injects and then try to read a body out of the
	// tunnel — which of course isn't a body at all.
	remoteBuf := bufio.NewReader(rawRemote)
	status, err := readConnectStatus(remoteBuf)
	if err != nil {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer response failed, falling back",
			slog.String("peer", peer), logger.Err(err))
		return false
	}
	if status != http.StatusOK {
		_ = rawRemote.Close()
		f.log.WarnContext(ctx, "peer rejected tunnel, falling back",
			slog.String("peer", peer), slog.Int(logging.KeyHTTPResponseStatusCode, status))
		return false
	}

	// From here on we're committed: wrap the peer conn for tracking
	// before any client-visible side effect, so active_tunnels and
	// bytes_total stay accurate even if the hijack below fails.
	remote := f.newInstrumented(rawRemote, r.Host, peer, ctx)
	// Slot release moves to the InstrumentedConn close callback.
	releaseSlot = false
	defer func() { _ = remote.Close() }()

	client, clientBuf, err := hijackConn(w)
	if err != nil {
		f.log.ErrorContext(ctx, "hijack failed", slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		f.recordError(ctx, "hijack")
		return true
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(connectEstablished); err != nil {
		f.recordError(ctx, "handshake")
		return true
	}

	f.metrics.RecordTunnelStarted(ctx, time.Since(setupStart))

	// Drain both pre-buffered sides into the splice so bytes that
	// arrived inside the same TCP segment as the headers don't get
	// silently dropped.
	splice(client, remote, readerOf(clientBuf), remoteBuf)
	return true
}

// -- HTTP handling ----------------------------------------------------

func (f *DualForwarder) httpExternal(w http.ResponseWriter, r *http.Request) {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if req.URL.Host == "" {
				req.URL.Host = req.Host
			}
			if req.URL.Scheme == "" {
				req.URL.Scheme = "http"
			}
			f.stripControlHeaders(req)
		},
		Transport: f.transport,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if ipcheck.IsDenied(err) {
				f.log.WarnContext(req.Context(), "http ip denied",
					slog.String(keyServerAddress, req.Host),
					slog.String(keyErrorSource, errSourcePolicy),
					slog.String(keyDenyReason, ipcheck.Reason(err)),
					logger.Err(err))
				f.recordDeny(req.Context(), "ip")
				http.Error(w, denyBody(req.Host, err), http.StatusForbidden)
				return
			}
			f.log.ErrorContext(req.Context(), "upstream error",
				slog.String(keyServerAddress, req.Host),
				slog.String(keyErrorSource, errSourceUpstream),
				logger.Err(err))
			f.recordError(req.Context(), "upstream")
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

	// Strip the credential before relaying to the peer: Proxy-Authorization
	// is hop-by-hop per RFC 7230 §6.1, and the peer trusts us as the
	// upstream hop (HopHeader is its auth-bypass signal).
	r.Header.Del("Proxy-Authorization")
	r.Header.Set(HopHeader, "1")
	setPeerTrace(r.Header, ctx)
	if err := r.WriteProxy(conn); err != nil {
		f.log.WarnContext(ctx, "peer write failed",
			slog.String("peer", peer), slog.String(keyErrorSource, errSourceProxy), logger.Err(err))
		http.Error(w, "peer forward failed", http.StatusBadGateway)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), r)
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
