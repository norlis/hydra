package proxy_test

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/norlis/httpgate/middleware"
	"github.com/norlis/httpgate/trace"
	"github.com/norlis/hydra/internal/proxy"
	"go.opentelemetry.io/otel/attribute"
)

// headerCapture records the headers of every request reaching a stack.
type headerCapture struct {
	mu   sync.Mutex
	seen []http.Header
}

func (h *headerCapture) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, r.Header.Clone())
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (h *headerCapture) last(t *testing.T) http.Header {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.seen) == 0 {
		t.Fatal("peer received no request")
	}
	return h.seen[len(h.seen)-1]
}

// drainRequest consumes a CONNECT request head from c.
func drainRequest(c net.Conn) {
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			return
		}
	}
}

func TestTunnelViaPeer(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("p"), 512)

	t.Run("echoes through two hops and the peer receives the hop header", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr()})
		echo := echoListener(t)
		c, br, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		if got := readExact(t, br, len(payload)); !bytes.Equal(got, payload) {
			t.Error("echo mismatch through peer")
		}
		if got := capt.last(t).Get(proxy.HopHeader); got != "1" {
			t.Errorf("peer saw %s=%q, want 1", proxy.HopHeader, got)
		}
		if capt.last(t).Get(trace.Header) != "" {
			t.Error("traceparent sent without a trace in context")
		}
	})

	t.Run("a tunnel relayed through a peer counts the bytes in both directions", func(t *testing.T) {
		t.Parallel()
		peer := newStack(t, stackOpts{})
		local := newStack(t, stackOpts{peer: peer.addr()})
		echo := echoListener(t)
		c, br, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		readExact(t, br, len(payload))
		_ = c.Close()
		waitUntil(t, "tracker drain", func() bool { return local.tracker.Count() == 0 })

		c2d := counterValue(t, local.reader, metricBytes, attribute.String("direction", "c2d"))
		d2c := counterValue(t, local.reader, metricBytes, attribute.String("direction", "d2c"))
		if c2d != int64(len(payload)) || d2c != int64(len(payload)) {
			t.Errorf("bytes c2d=%d d2c=%d, want %d each", c2d, d2c, len(payload))
		}
		e, ok := findLog(local.logs.Entries(), "request completed")
		if !ok || e.String("bytes_d2c") != strconv.Itoa(len(payload)) || e.String("decision") != "peer" {
			t.Errorf("canonical log = %+v (found=%v), want bytes_d2c=%d decision=peer", e.Attrs, ok, len(payload))
		}
	})

	t.Run("a download through a peer keeps the tunnel alive under the idle watchdog", func(t *testing.T) {
		t.Parallel()
		// The destination streams without the client ever writing: only
		// downstream reads prove the tunnel is active.
		const ticks, every = 8, 50 * time.Millisecond
		dst := listen(t)
		go func() {
			conn, err := dst.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			for range ticks {
				if _, err := conn.Write([]byte("x")); err != nil {
					return
				}
				time.Sleep(every)
			}
		}()
		peer := newStack(t, stackOpts{})
		local := newStack(t, stackOpts{peer: peer.addr(), idle: 3 * every})
		_, br, resp := connect(t, local.addr(), dst.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		readExact(t, br, ticks) // fails the test if the watchdog cut the tunnel early
	})

	t.Run("the relayed CONNECT carries the entity so the owner can log it", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr()})
		echo := echoListener(t)
		c := dial(t, local.addr())
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		target := echo.Addr().String()
		req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n" + proxy.EntityHeader + ": tenant-42\r\n\r\n"
		if _, err := c.Write([]byte(req)); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect}) //nolint:bodyclose // a 2xx CONNECT has no body: reading or closing it would drain the tunnel
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT = %v, %v", resp, err)
		}
		if got := capt.last(t).Get(proxy.EntityHeader); got != "tenant-42" {
			t.Errorf("peer saw %s=%q, want tenant-42", proxy.EntityHeader, got)
		}
	})

	t.Run("with basic auth on both nodes the hop carries this node's credentials and the tunnel works", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{basicAuth: true, wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr(), basicAuth: true, peerAuth: proxy.BasicCredential(authUser, authPass)})
		echo := echoListener(t)
		c := dial(t, local.addr())
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		target := echo.Addr().String()
		_, _ = c.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: " + basicHeader(authUser, authPass) + "\r\n\r\n"))
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect}) //nolint:bodyclose // 2xx CONNECT has no body
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 through an authenticated peer", resp.StatusCode)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		if got := readExact(t, br, len(payload)); !bytes.Equal(got, payload) {
			t.Error("echo mismatch through authenticated peer")
		}
		if got := capt.last(t).Get("Proxy-Authorization"); got != basicHeader(authUser, authPass) {
			t.Errorf("peer saw Proxy-Authorization=%q, want this node's credential", got)
		}
	})

	t.Run("with TraceContext in front the peer receives traceparent", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr(), wrap: middleware.TraceContext()})
		echo := echoListener(t)
		_, _, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if capt.last(t).Get(trace.Header) == "" {
			t.Error("peer did not receive traceparent")
		}
	})

	t.Run("bytes the peer sends with the 200 reach the client", func(t *testing.T) {
		t.Parallel()
		early := []byte("EARLY-FROM-PEER")
		eager := rawListener(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			drainRequest(c)
			_, _ = c.Write(append([]byte("HTTP/1.1 200 Connection established\r\n\r\n"), early...))
			_, _ = io.Copy(c, c)
		})
		local := newStack(t, stackOpts{peer: eager.Addr().String()})
		_, br, resp := connect(t, local.addr(), "203.0.113.10:443", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if got := readExact(t, br, len(early)); !bytes.Equal(got, early) {
			t.Errorf("got %q, want %q", got, early)
		}
	})

	t.Run("a disallowed port is rejected before contacting the peer", func(t *testing.T) {
		t.Parallel()
		var accepted atomic.Int32
		peer := rawListener(t, func(c net.Conn) {
			accepted.Add(1)
			_ = c.Close()
		})
		local := newStack(t, stackOpts{peer: peer.Addr().String(), allowedPorts: []int{443}})
		echo := echoListener(t)
		_, _, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		if got := accepted.Load(); got != 0 {
			t.Errorf("peer accepted %d connections, want 0", got)
		}
		if got := counterValue(t, local.reader, metricDenied, attribute.String("reason", "port")); got != 1 {
			t.Errorf("connect_denied{port} = %d, want 1", got)
		}
	})

	t.Run("peer rejecting with 403 falls back to direct and echoes", func(t *testing.T) {
		t.Parallel()
		rejecting := rawListener(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			drainRequest(c)
			_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		})
		local := newStack(t, stackOpts{peer: rejecting.Addr().String()})
		echo := echoListener(t)
		c, br, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		if got := readExact(t, br, len(payload)); !bytes.Equal(got, payload) {
			t.Error("echo mismatch after fallback")
		}
		if _, ok := findLog(local.logs.Entries(), "peer rejected tunnel, falling back"); !ok {
			t.Error("missing fallback log")
		}
		if got := counterValue(t, local.reader, metricAttempts, attribute.String("result", "ok")); got != 1 {
			t.Errorf("connect_attempts{ok} = %d, want 1", got)
		}
	})

	t.Run("peer refusing the connection falls back to direct", func(t *testing.T) {
		t.Parallel()
		local := newStack(t, stackOpts{peer: closedPort(t)})
		echo := echoListener(t)
		_, _, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 via fallback", resp.StatusCode)
		}
		if _, ok := findLog(local.logs.Entries(), "peer dial failed, falling back"); !ok {
			t.Error("missing fallback log")
		}
	})

	t.Run("peer answering truncated headers falls back to direct", func(t *testing.T) {
		t.Parallel()
		truncating := rawListener(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			drainRequest(c)
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\n"))
		})
		local := newStack(t, stackOpts{peer: truncating.Addr().String()})
		echo := echoListener(t)
		_, _, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 via fallback", resp.StatusCode)
		}
		if _, ok := findLog(local.logs.Entries(), "peer response failed, falling back"); !ok {
			t.Error("missing fallback log")
		}
	})

	t.Run("the slot is released after a fallback", func(t *testing.T) {
		t.Parallel()
		rejecting := rawListener(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			drainRequest(c)
			_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		})
		local := newStack(t, stackOpts{peer: rejecting.Addr().String(), maxTunnels: 1})
		echo := echoListener(t)
		c, _, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if local.limiter.Active() != 1 {
			t.Errorf("Active during tunnel = %d, want 1", local.limiter.Active())
		}
		_ = c.Close()
		waitUntil(t, "limiter release", func() bool { return local.limiter.Active() == 0 })
	})

	t.Run("a silent peer times out during the handshake and falls back to direct", func(t *testing.T) {
		t.Parallel()
		held := make(chan net.Conn, 1)
		silent := rawListener(t, func(c net.Conn) { held <- c })
		local := newStack(t, stackOpts{peer: silent.Addr().String(), maxTunnels: 1, handshake: 200 * time.Millisecond})
		releaseHeld(t, held) // registered after newStack so it runs before the server's Close
		echo := echoListener(t)

		start := time.Now()
		c, br, resp := connect(t, local.addr(), echo.Addr().String(), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 via fallback", resp.StatusCode)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("fallback took %v, want roughly the handshake timeout", elapsed)
		}
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
		if got := readExact(t, br, len(payload)); !bytes.Equal(got, payload) {
			t.Error("echo mismatch after fallback")
		}
		if _, ok := findLog(local.logs.Entries(), "peer response failed, falling back"); !ok {
			t.Error("missing fallback log")
		}
	})
}
