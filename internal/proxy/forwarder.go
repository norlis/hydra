package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/norlis/hydra/internal/topology"
	"go.uber.org/zap"
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

// DualForwarder implements Forwarder for both HTTP and CONNECT methods,
// binding every outbound TCP socket to a specific local interface.
type DualForwarder struct {
	iface        topology.NetworkInterface
	dialer       *net.Dialer
	transport    *http.Transport
	stripHeaders []string
	log          *zap.Logger
}

// NewDualForwarder creates a forwarder pinned to iface. Every outbound
// TCP socket (tunnels + HTTP) is bound to iface.PrivateIP.
//
// stripHeaders is the extra list of headers to remove before writing
// to the upstream (internet) service. Control-plane headers
// (HopHeader, EntityHeader) are always stripped on external forward
// regardless of this list.
func NewDualForwarder(iface topology.NetworkInterface, stripHeaders []string, log *zap.Logger) *DualForwarder {
	local, _ := net.ResolveTCPAddr("tcp", iface.PrivateIP+":0")
	dialer := &net.Dialer{
		LocalAddr: local,
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	t := cleanhttp.DefaultTransport()
	t.DialContext = dialer.DialContext
	return &DualForwarder{
		iface:        iface,
		dialer:       dialer,
		transport:    t,
		stripHeaders: stripHeaders,
		log:          log.With(zap.String("iface", iface.Name), zap.String("ip", iface.PrivateIP)),
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
	remote, err := f.dialer.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		f.log.Error("tunnel dial failed", zap.String("target", r.Host), zap.Error(err))
		http.Error(w, "connection failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = remote.Close() }()

	// Hijack first, then write the CONNECT ack directly to the socket.
	// Using w.WriteHeader(200) would make Go's server emit framing
	// headers that are invalid on a tunnel response.
	client, clientBuf, err := hijackConn(w)
	if err != nil {
		f.log.Error("hijack failed", zap.Error(err))
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(connectEstablished); err != nil {
		return
	}

	splice(client, remote, readerOf(clientBuf), nil)
}

// tunnelViaPeer negotiates a CONNECT through peer before acking the
// client. Returns true if the tunnel was handled end-to-end, false if
// the peer couldn't be reached and we never wrote anything to the
// client (so the caller can fall back).
func (f *DualForwarder) tunnelViaPeer(w http.ResponseWriter, r *http.Request, peer string) bool {
	remote, err := f.dialer.DialContext(r.Context(), "tcp", peer)
	if err != nil {
		f.log.Warn("peer dial failed, falling back", zap.String("peer", peer), zap.Error(err))
		return false
	}

	fwd, err := http.NewRequestWithContext(r.Context(), http.MethodConnect, "http://"+r.Host, nil)
	if err != nil {
		_ = remote.Close()
		return false
	}
	fwd.Host = r.Host
	fwd.Header.Set(HopHeader, "1")

	if err := fwd.Write(remote); err != nil {
		_ = remote.Close()
		f.log.Warn("peer write failed, falling back", zap.String("peer", peer), zap.Error(err))
		return false
	}

	// Parse the peer's CONNECT response manually. http.ReadResponse
	// would latch onto any Transfer-Encoding/Content-Length the peer's
	// http.Server injects and then try to read a body out of the
	// tunnel — which of course isn't a body at all.
	remoteBuf := bufio.NewReader(remote)
	status, err := readConnectStatus(remoteBuf)
	if err != nil {
		_ = remote.Close()
		f.log.Warn("peer response failed, falling back",
			zap.String("peer", peer), zap.Error(err))
		return false
	}
	if status != http.StatusOK {
		_ = remote.Close()
		f.log.Warn("peer rejected tunnel, falling back",
			zap.String("peer", peer), zap.Int("status", status))
		return false
	}

	defer func() { _ = remote.Close() }()

	client, clientBuf, err := hijackConn(w)
	if err != nil {
		f.log.Error("hijack failed", zap.Error(err))
		return true
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(connectEstablished); err != nil {
		return true
	}

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
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			f.log.Error("upstream error", zap.Error(err))
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// stripControlHeaders removes hydra-internal routing headers plus any
// user-configured extras. Called only on the external path so the
// peer-to-peer hops preserve HopHeader and EntityHeader.
func (f *DualForwarder) stripControlHeaders(req *http.Request) {
	req.Header.Del(HopHeader)
	req.Header.Del(EntityHeader)
	for _, h := range f.stripHeaders {
		req.Header.Del(h)
	}
}

// httpViaPeer writes the request (in forward-proxy format, with an
// absolute URL) directly to the peer's proxy port, then streams the
// response back to the client.
func (f *DualForwarder) httpViaPeer(w http.ResponseWriter, r *http.Request, peer string) {
	conn, err := f.dialer.DialContext(r.Context(), "tcp", peer)
	if err != nil {
		f.log.Warn("peer dial failed", zap.String("peer", peer), zap.Error(err))
		http.Error(w, "peer unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = conn.Close() }()

	r.Header.Set(HopHeader, "1")
	if err := r.WriteProxy(conn); err != nil {
		f.log.Warn("peer write failed", zap.String("peer", peer), zap.Error(err))
		http.Error(w, "peer forward failed", http.StatusBadGateway)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), r)
	if err != nil {
		f.log.Warn("peer response failed", zap.String("peer", peer), zap.Error(err))
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
	return h.Hijack()
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