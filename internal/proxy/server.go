package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/proxy/conntrack"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/pkg/logger"
	"go.uber.org/fx"
)

// proxyMaxHeaderBytes caps the total size of incoming request headers.
// 16 KiB is the standard upper bound; larger headers indicate bombing
// or misconfigured clients.
const proxyMaxHeaderBytes = 16 << 10

// TCP keepalive parameters applied to every conn accepted on the proxy
// data plane. Go enables keepalive on accepted TCP conns by default,
// but the default idle (15s) is too aggressive for long CONNECT
// tunnels. We pick values that detect a wedged client within roughly
// a minute (Idle + Interval×Count) without reacting to brief network
// blips:
//
//	first probe after 30s of idle, then every 10s, drop after 3 misses.
//
// Configured via net.ListenConfig.KeepAliveConfig (Go 1.23+) so the
// settings flow into every accepted conn without a custom Listener
// wrapper.
const (
	proxyKeepAliveIdle     = 30 * time.Second
	proxyKeepAliveInterval = 10 * time.Second
	proxyKeepAliveCount    = 3
)

// StartProxyServers launches one HTTP server per discovered interface.
// Each server listens on iface.ServicePort and binds outgoing
// connections to iface.PrivateIP, so traffic routed through a
// specific NIC leaves through that NIC.
func StartProxyServers(
	lc fx.Lifecycle,
	cfg *hydra.Config,
	reg *network.Registry,
	ring *cluster.Ring,
	tracker *conntrack.Tracker,
	tunnelLimit *limiter.Tunnel,
	mtr *metrics.Metrics,
	log *slog.Logger,
) error {
	ifaces := reg.All()

	// Collect this node's local IPs so the classifier can deny
	// self-connections (a request that resolves back to us).
	localIPs := make([]netip.Addr, 0, len(ifaces))
	for _, iface := range ifaces {
		if ip, err := netip.ParseAddr(iface.PrivateIP); err == nil {
			localIPs = append(localIPs, ip)
		}
	}

	classifier, err := ipcheck.New(cfg.ProxyDenyCIDR, cfg.ProxyAllowCIDR, localIPs)
	if err != nil {
		return fmt.Errorf("proxy: building IP classifier: %w", err)
	}

	// Single ListenConfig shared by every NIC. Building it once keeps
	// keepalive settings consistent across listeners and lets us bind
	// synchronously below so a port collision fails fx startup instead
	// of erroring inside an OnStart goroutine.
	listenCfg := net.ListenConfig{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     proxyKeepAliveIdle,
			Interval: proxyKeepAliveInterval,
			Count:    proxyKeepAliveCount,
		},
	}

	type boundServer struct {
		srv *http.Server
		ln  net.Listener
	}
	servers := make([]boundServer, 0, len(ifaces))
	// Roll back any listeners already bound when a later one fails so
	// we don't leak ports on startup error.
	rollback := func() {
		for _, b := range servers {
			_ = b.ln.Close()
		}
	}
	for _, iface := range ifaces {
		forwarder := NewDualForwarder(
			iface,
			cfg.ProxyStripHeaders,
			cfg.ProxyAllowedPorts,
			classifier,
			tracker,
			tunnelLimit,
			cfg.ProxyIdleTimeout,
			mtr,
			log,
		)
		router := NewRouter(iface, ring, forwarder, mtr, log)

		// Auth wraps the router so 407 short-circuits before any
		// routing decision touches the request. With AuthModeNone the
		// wrapper is a no-op and the handler is the bare router.
		handler, err := WrapAuth(cfg.ProxyAuthMode, cfg.ProxyAuthUser, cfg.ProxyAuthPass, router)
		if err != nil {
			rollback()
			return fmt.Errorf("proxy: building auth handler: %w", err)
		}

		addr := fmt.Sprintf(":%d", iface.ServicePort)
		ln, err := listenCfg.Listen(context.Background(), "tcp", addr)
		if err != nil {
			rollback()
			return fmt.Errorf("proxy: listening on %s: %w", addr, err)
		}

		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       300 * time.Second,
			MaxHeaderBytes:    proxyMaxHeaderBytes,
			// Disable HTTP/2 explicitly: HTTP/2 doesn't expose Hijacker
			// and breaks the CONNECT tunneling we rely on.
			TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		}
		servers = append(servers, boundServer{srv: srv, ln: ln})
		log.Info("proxy configured",
			slog.String("addr", srv.Addr),
			slog.String("iface", iface.Name),
			slog.String("private_ip", iface.PrivateIP))
	}

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			for _, b := range servers {
				go func() {
					log.Info("proxy listening", slog.String("addr", b.srv.Addr))
					if err := b.srv.Serve(b.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.Error("proxy server error",
							slog.String("addr", b.srv.Addr), logger.Err(err))
					}
				}()
			}
			return nil
		},
		OnStop: func(_ context.Context) error {
			// Short graceful window — CONNECT tunnels are hijacked and
			// Shutdown cannot close them. After the window we force
			// Close() to drop them.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			var wg sync.WaitGroup
			for _, b := range servers {
				wg.Go(func() {
					if err := b.srv.Shutdown(shutdownCtx); err != nil {
						log.Debug("graceful shutdown timeout, forcing close",
							slog.String("addr", b.srv.Addr), logger.Err(err))
						_ = b.srv.Close()
					}
				})
			}
			wg.Wait()

			// http.Server.Shutdown/Close can't reach hijacked CONNECT
			// tunnels. The tracker holds every InstrumentedConn we wrapped
			// on the upstream side; closing them unblocks the splice
			// goroutines and lets the process exit promptly.
			if tracker != nil {
				if n := tracker.CloseAll(); n > 0 {
					log.Info("forced close of active tunnels", slog.Int("count", n))
				}
			}
			return nil
		},
	})
	return nil
}
