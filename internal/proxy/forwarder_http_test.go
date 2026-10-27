package proxy_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/norlis/httpgate/trace"
	"github.com/norlis/hydra/internal/proxy"
)

const stripMe = "X-Strip-Me"

// origin returns an HTTP server that echoes the request headers it saw as
// JSON so the test can assert what the proxy forwarded.
func origin(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func viaProxy(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()
	u, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	t.Cleanup(tr.CloseIdleConnections)
	// A proxy that never answers must fail the test, not hang it.
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// doGet performs the request and returns the status plus the headers the
// origin reported (decoded from its JSON body). The body is closed here.
func doGet(t *testing.T, client *http.Client, target string, headers map[string]string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var seen http.Header
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &seen)
	return resp.StatusCode, seen
}

func TestHTTPExternal(t *testing.T) {
	t.Parallel()

	t.Run("forwards and returns the origin status and body", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		o := origin(t, http.StatusTeapot)
		status, seen := doGet(t, viaProxy(t, s.addr()), o.URL, map[string]string{"X-Keep": "yes"})
		if status != http.StatusTeapot || seen.Get("X-Keep") != "yes" {
			t.Errorf("status=%d seen=%v", status, seen)
		}
	})

	t.Run("strips control, hop-by-hop and Connection-listed headers", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{strip: []string{stripMe}})
		o := origin(t, http.StatusOK)
		_, seen := doGet(t, viaProxy(t, s.addr()), o.URL, map[string]string{
			proxy.HopHeader:       "1",
			proxy.EntityHeader:    "tenant-1",
			trace.Header:          "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			"Proxy-Authorization": "Basic xyz",
			"Connection":          "X-Custom",
			"X-Custom":            "1",
			stripMe:               "1",
			"X-Keep":              "yes",
		})
		for _, h := range []string{proxy.HopHeader, proxy.EntityHeader, trace.Header, "Proxy-Authorization", "X-Custom", stripMe} {
			if v := seen.Get(h); v != "" {
				t.Errorf("origin received %s=%q, want stripped", h, v)
			}
		}
		if seen.Get("X-Keep") != "yes" {
			t.Error("X-Keep was stripped")
		}
	})

	t.Run("does not reveal the client address to the origin", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		o := origin(t, http.StatusOK)
		_, seen := doGet(t, viaProxy(t, s.addr()), o.URL, nil)
		for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
			if v := seen.Get(h); v != "" {
				t.Errorf("origin received %s=%q, want nothing", h, v)
			}
		}
	})

	t.Run("unreachable origin yields 502 upstream error", func(t *testing.T) {
		t.Parallel()
		s := newStack(t, stackOpts{})
		status, _ := doGet(t, viaProxy(t, s.addr()), "http://"+closedPort(t)+"/", nil)
		if status != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", status)
		}
	})
}

func TestHTTPViaPeer(t *testing.T) {
	t.Parallel()

	t.Run("peer status and body reach the client and the peer sees hop but no Proxy-Authorization", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr()})
		o := origin(t, http.StatusTeapot)
		status, seen := doGet(t, viaProxy(t, local.addr()), o.URL, map[string]string{"Proxy-Authorization": "Basic xyz", "X-Keep": "yes"})
		if status != http.StatusTeapot || seen.Get("X-Keep") != "yes" {
			t.Errorf("status=%d seen=%v", status, seen)
		}
		h := capt.last(t)
		if h.Get(proxy.HopHeader) != "1" || h.Get("Proxy-Authorization") != "" {
			t.Errorf("peer saw hop=%q auth=%q", h.Get(proxy.HopHeader), h.Get("Proxy-Authorization"))
		}
	})

	t.Run("with peer credentials configured the hop replaces the client's Proxy-Authorization", func(t *testing.T) {
		t.Parallel()
		capt := &headerCapture{}
		peer := newStack(t, stackOpts{basicAuth: true, wrap: capt.wrap})
		local := newStack(t, stackOpts{peer: peer.addr(), peerAuth: proxy.BasicCredential(authUser, authPass)})
		o := origin(t, http.StatusTeapot)
		status, _ := doGet(t, viaProxy(t, local.addr()), o.URL, map[string]string{"Proxy-Authorization": "Basic client-secret"})
		if status != http.StatusTeapot {
			t.Errorf("status = %d, want 418 through an authenticated peer", status)
		}
		if got := capt.last(t).Get("Proxy-Authorization"); got != basicHeader(authUser, authPass) {
			t.Errorf("peer saw Proxy-Authorization=%q, want this node's credential", got)
		}
	})

	t.Run("unreachable peer yields 502 peer unreachable", func(t *testing.T) {
		t.Parallel()
		local := newStack(t, stackOpts{peer: closedPort(t)})
		o := origin(t, http.StatusOK)
		status, _ := doGet(t, viaProxy(t, local.addr()), o.URL, nil)
		if status != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", status)
		}
	})

	t.Run("a silent peer on the HTTP path yields 502 after the handshake timeout", func(t *testing.T) {
		t.Parallel()
		held := make(chan net.Conn, 1)
		silent := rawListener(t, func(c net.Conn) { held <- c })
		local := newStack(t, stackOpts{peer: silent.Addr().String(), handshake: 200 * time.Millisecond})
		releaseHeld(t, held)
		o := origin(t, http.StatusOK)
		start := time.Now()
		status, _ := doGet(t, viaProxy(t, local.addr()), o.URL, nil)
		if status != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", status)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("took %v, want roughly the handshake timeout", elapsed)
		}
	})

	t.Run("cancelling the client request closes the connection to the peer", func(t *testing.T) {
		t.Parallel()
		got := make(chan struct{}, 1)
		closed := make(chan struct{}, 1)
		held := make(chan net.Conn, 1)
		peer := rawListener(t, func(c net.Conn) {
			held <- c
			drainRequest(c)
			got <- struct{}{}
			_, _ = io.Copy(io.Discard, c) // returns when the proxy closes its side
			closed <- struct{}{}
		})
		local := newStack(t, stackOpts{peer: peer.Addr().String()})
		releaseHeld(t, held)
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		errCh := make(chan error, 1)
		go func() {
			resp, err := viaProxy(t, local.addr()).Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			errCh <- err
		}()
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("peer never received the request")
		}
		cancel()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("proxy kept the peer connection open after the client cancelled")
		}
		<-errCh
	})

	t.Run("peer headers reach the client and hop-by-hop ones are dropped by net/http", func(t *testing.T) {
		t.Parallel()
		fakePeer := rawListener(t, func(c net.Conn) {
			defer func() { _ = c.Close() }()
			drainRequest(c)
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nConnection: close\r\nX-Peer: 1\r\nContent-Length: 2\r\n\r\nok"))
		})
		local := newStack(t, stackOpts{peer: fakePeer.Addr().String()})
		c := dial(t, local.addr())
		_, _ = c.Write([]byte("GET http://example.invalid/ HTTP/1.1\r\nHost: example.invalid\r\n\r\n"))
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.Header.Get("X-Peer") != "1" || readBody(t, resp) != "ok" {
			t.Error("peer response not relayed")
		}
		// httpViaPeer copies Connection: close verbatim, but the proxy's
		// http.Server drops hop-by-hop headers and keeps our connection open.
		if resp.Header.Get("Connection") != "" || resp.Close {
			t.Errorf("Connection = %q close=%v, want hop-by-hop dropped", resp.Header.Get("Connection"), resp.Close)
		}
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := br.ReadByte(); errors.Is(err, io.EOF) {
			t.Error("proxy closed the keep-alive connection")
		}
	})
}
