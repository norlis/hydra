package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/norlis/hydra/internal/proxy"
	"github.com/norlis/hydra/internal/proxy/conntrack"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const (
	metricAttempts = "hydra.proxy.connect_attempts"
	metricDenied   = "hydra.proxy.connect_denied"
	metricErrors   = "hydra.proxy.connect_errors"
	metricActive   = "hydra.proxy.active_tunnels"
	metricBytes    = "hydra.proxy.bytes"
	loopbackCIDR   = "127.0.0.0/8"
)

// stackOpts configures one proxy stack (forwarder behind an httptest server).
type stackOpts struct {
	peer         string        // "" → ForwardToExternal, else ForwardToPeer(peer)
	idle         time.Duration // tunnel idle timeout
	maxTunnels   int           // 0 → unlimited
	allowedPorts []int         // nil → every port
	denyLoopback bool          // use the default classifier (loopback denied)
	strip        []string      // extra headers to strip on external forward
	handshake    time.Duration // 0 → forwarder default
	basicAuth    bool          // require authUser/authPass on inbound requests
	peerAuth     string        // credential sent on hops to a peer ("" = none)
	wrap         func(http.Handler) http.Handler
}

type stack struct {
	srv     *httptest.Server
	limiter *limiter.Tunnel
	tracker *conntrack.Tracker
	reader  *sdkmetric.ManualReader
	logs    *testlog.Recorder
}

func (s *stack) addr() string { return s.srv.Listener.Addr().String() }

// newStack wires a DualForwarder pinned to 127.0.0.1 behind a real
// http.Server (needed for Hijacker) and returns the observable parts.
func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	var allow []string
	if !o.denyLoopback {
		allow = []string{loopbackCIDR}
	}
	classifier, err := ipcheck.New(nil, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, reader := newTestMetrics(t)
	logs := testlog.New()
	s := &stack{limiter: limiter.New(o.maxTunnels), tracker: conntrack.NewTracker(), reader: reader, logs: logs}
	fwd := proxy.NewDualForwarder(
		topology.NetworkInterface{Name: "lo", PrivateIP: "127.0.0.1"},
		proxy.ForwarderConfig{StripHeaders: o.strip, AllowedPorts: o.allowedPorts, IdleTimeout: o.idle, PeerAuth: o.peerAuth},
		proxy.ForwarderDeps{Classifier: classifier, Tracker: s.tracker, TunnelLimit: s.limiter, Metrics: m},
		logs.Logger(),
	)
	if o.handshake > 0 {
		proxy.SetHandshakeTimeout(fwd, o.handshake)
	}
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fwd.Serve(w, r, proxy.RouteFor(r, o.peer))
	})
	if o.basicAuth {
		wrapped, err := proxy.WrapAuth(proxy.AuthModeBasic, authUser, authPass, h, logs.Logger())
		if err != nil {
			t.Fatal(err)
		}
		h = wrapped
	}
	if o.wrap != nil {
		h = o.wrap(h)
	}
	s.srv = httptest.NewServer(h)
	t.Cleanup(s.srv.Close)
	return s
}

// listen opens a loopback TCP listener closed at test end.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// dial opens a loopback TCP connection closed at test end.
func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// echoListener accepts TCP connections and echoes everything back.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	ln := listen(t)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

// rawListener accepts connections and runs serve on each one.
func rawListener(t *testing.T, serve func(net.Conn)) net.Listener {
	t.Helper()
	ln := listen(t)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return ln
}

// closedPort returns an address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln := listen(t)
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// connectResp is the CONNECT response head with its body already drained.
type connectResp struct {
	Status     string
	StatusCode int
	Body       string
}

// connect sends a CONNECT for target through proxyAddr, with extra bytes in
// the same write as the headers, and returns the response plus the buffered
// reader positioned at the first tunnel byte.
func connect(t *testing.T, proxyAddr, target string, extra []byte) (net.Conn, *bufio.Reader, connectResp) {
	t.Helper()
	c := dial(t, proxyAddr)
	// Lost bytes must fail the test, not hang it.
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := c.Write(append([]byte(req), extra...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect}) //nolint:bodyclose // a 2xx CONNECT has no body: reading or closing it would drain the tunnel
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	out := connectResp{Status: resp.Status, StatusCode: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		out.Body = readBody(t, resp)
	}
	return c, br, out
}

// waitUntil polls cond every 5ms for up to 2s. It is the only polling wait
// in the suite: the tunnel's onClose runs asynchronously after the client
// closes, on real sockets, so synctest does not apply.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// releaseHeld closes a connection a silent fake peer is holding, so the
// proxy handler stuck on it unwinds before httptest.Server.Close waits for
// outstanding requests. Register it after newStack (cleanups run LIFO).
func releaseHeld(t *testing.T, held <-chan net.Conn) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case c := <-held:
			_ = c.Close()
		default:
		}
	})
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readExact(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("reading %d bytes: %v", n, err)
	}
	return buf
}

func findLog(entries []testlog.Entry, msg string) (testlog.Entry, bool) {
	for _, e := range entries {
		if e.Message == msg {
			return e, true
		}
	}
	return testlog.Entry{}, false
}

func TestTunnelExternal(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("h"), 1024)

	t.Run("opens a tunnel and echoes in both directions", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		echo := echoListener(t)
		c, br, resp := connect(t, s.addr(), echo.Addr().String(), nil)
		if resp.Status != "200 Connection established" {
			t.Fatalf("status = %q", resp.Status)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		if got := readExact(t, br, len(payload)); !bytes.Equal(got, payload) {
			t.Error("echo mismatch")
		}
	})

	t.Run("bytes sent together with the headers reach the destination", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		echo := echoListener(t)
		extra := []byte("early-bytes-in-same-segment")
		_, br, resp := connect(t, s.addr(), echo.Addr().String(), extra)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if got := readExact(t, br, len(extra)); !bytes.Equal(got, extra) {
			t.Errorf("got %q, want %q", got, extra)
		}
	})

	t.Run("disallowed port is rejected with 403 and counted as denied by port", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{allowedPorts: []int{443}})
		echo := echoListener(t)
		_, _, resp := connect(t, s.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		if got := counterValue(t, s.reader, metricDenied, attribute.String("reason", "port")); got != 1 {
			t.Errorf("connect_denied{port} = %d, want 1", got)
		}
		e, ok := findLog(s.logs.Entries(), "tunnel port denied")
		if !ok || e.Level != slog.LevelWarn || e.String("deny_reason") != "port_not_allowed" || e.String("error_source") != "policy" {
			t.Errorf("log = %+v (found=%v)", e, ok)
		}
	})

	t.Run("destination without port is rejected with 403", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{allowedPorts: []int{443}})
		_, _, resp := connect(t, s.addr(), "127.0.0.1", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("denied IP is rejected with 403 and a policy body", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{denyLoopback: true})
		echo := echoListener(t)
		_, _, resp := connect(t, s.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		const want = "Egress proxying is denied to host '127.0.0.1': default deny policy.\n"
		if got := resp.Body; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		if got := counterValue(t, s.reader, metricDenied, attribute.String("reason", "ip")); got != 1 {
			t.Errorf("connect_denied{ip} = %d, want 1", got)
		}
	})

	t.Run("closed destination yields 502 and a dial error metric", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		_, _, resp := connect(t, s.addr(), closedPort(t), nil)
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", resp.StatusCode)
		}
		if got := counterValue(t, s.reader, metricErrors, attribute.String("stage", "dial")); got != 1 {
			t.Errorf("connect_errors{dial} = %d, want 1", got)
		}
	})

	t.Run("second tunnel over the limit gets 503 and the slot returns when the first closes", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{maxTunnels: 1})
		echo := echoListener(t)
		first, _, resp := connect(t, s.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first status = %d", resp.StatusCode)
		}
		_, _, second := connect(t, s.addr(), echo.Addr().String(), nil)
		if second.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("second status = %d, want 503", second.StatusCode)
		}
		if got := counterValue(t, s.reader, metricDenied, attribute.String("reason", "tunnel_limit")); got != 1 {
			t.Errorf("connect_denied{tunnel_limit} = %d, want 1", got)
		}
		_ = first.Close()
		waitUntil(t, "limiter release", func() bool { return s.limiter.Active() == 0 })
	})

	t.Run("closing the tunnel zeroes active_tunnels, reports bytes and emits the canonical log", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		echo := echoListener(t)
		c, br, _ := connect(t, s.addr(), echo.Addr().String(), nil)
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		readExact(t, br, len(payload))
		if got := counterValue(t, s.reader, metricActive); got != 1 {
			t.Errorf("active_tunnels during = %d, want 1", got)
		}
		_ = c.Close()
		waitUntil(t, "tracker drain", func() bool { return s.tracker.Count() == 0 })

		if got := counterValue(t, s.reader, metricActive); got != 0 {
			t.Errorf("active_tunnels after = %d, want 0", got)
		}
		c2d := counterValue(t, s.reader, metricBytes, attribute.String("direction", "c2d"))
		d2c := counterValue(t, s.reader, metricBytes, attribute.String("direction", "d2c"))
		if c2d != int64(len(payload)) || d2c != int64(len(payload)) {
			t.Errorf("bytes c2d=%d d2c=%d, want %d each", c2d, d2c, len(payload))
		}
		if got := counterValue(t, s.reader, metricAttempts, attribute.String("result", "ok")); got != 1 {
			t.Errorf("connect_attempts{ok} = %d, want 1", got)
		}
		e, ok := findLog(s.logs.Entries(), "request completed")
		if !ok || e.String("decision") != "local" || e.String("peer") != "" || e.String("bytes_c2d") != "1024" {
			t.Errorf("canonical log = %+v (found=%v)", e, ok)
		}
	})

	t.Run("a tunnel relayed by a peer logs the hop-local decision on completion", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		echo := echoListener(t)
		c := dial(t, s.addr())
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		target := echo.Addr().String()
		req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n" + proxy.HopHeader + ": 1\r\n\r\n"
		if _, err := c.Write([]byte(req)); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect}) //nolint:bodyclose // a 2xx CONNECT has no body: reading or closing it would drain the tunnel
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT = %v, %v", resp, err)
		}
		_ = c.Close()
		waitUntil(t, "tracker drain", func() bool { return s.tracker.Count() == 0 })

		e, ok := findLog(s.logs.Entries(), "request completed")
		if !ok || e.String("decision") != "hop-local" {
			t.Errorf("canonical log = %+v (found=%v), want decision hop-local", e, ok)
		}
	})

	t.Run("idle watchdog closes an inactive tunnel", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{idle: 100 * time.Millisecond})
		echo := echoListener(t)
		_, br, resp := connect(t, s.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		// The blocking read is the wait: the server closes the tunnel on idle.
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			t.Errorf("read = %v, want EOF from idle close", err)
		}
	})
}
