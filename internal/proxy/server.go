package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/network"
	"go.uber.org/fx"
	"go.uber.org/zap"
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
	log *zap.Logger,
) {
	ifaces := reg.All()
	servers := make([]*http.Server, 0, len(ifaces))

	for _, iface := range ifaces {
		forwarder := NewDualForwarder(iface, cfg.ProxyStripHeaders, log)
		router := NewRouter(iface, ring, forwarder, log)

		srv := &http.Server{
			Addr:              fmt.Sprintf(":%d", iface.ServicePort),
			Handler:           router,
			ReadHeaderTimeout: 30 * time.Second,
		}
		servers = append(servers, srv)
		log.Info("proxy configured",
			zap.String("addr", srv.Addr),
			zap.String("iface", iface.Name),
			zap.String("private_ip", iface.PrivateIP))
	}

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			for _, s := range servers {
				srv := s
				go func() {
					log.Info("proxy listening", zap.String("addr", srv.Addr))
					if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.Error("proxy server error",
							zap.String("addr", srv.Addr), zap.Error(err))
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
			for _, s := range servers {
				srv := s
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := srv.Shutdown(shutdownCtx); err != nil {
						log.Debug("graceful shutdown timeout, forcing close",
							zap.String("addr", srv.Addr), zap.Error(err))
						_ = srv.Close()
					}
				}()
			}
			wg.Wait()
			return nil
		},
	})
}