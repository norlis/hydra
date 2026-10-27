package handlers_test

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"github.com/norlis/hydra/internal/httpapi/httpx"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/topology"
)

// stubDiscovery serves fixed topology data; Start/Stop/Subscribe are unused.
type stubDiscovery struct {
	local  topology.Node
	active []topology.Node
}

func (s stubDiscovery) Start() error                       { return nil }
func (s stubDiscovery) Stop() error                        { return nil }
func (s stubDiscovery) GetActiveNodes() []topology.Node    { return s.active }
func (s stubDiscovery) GetLocalNode() topology.Node        { return s.local }
func (s stubDiscovery) Subscribe() <-chan bus.ClusterEvent { return nil }

func newTopology(t *testing.T, d stubDiscovery) *http.ServeMux {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	classifier, err := ipcheck.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.NewTopologyHandler(d, classifier, httpx.New(log), log)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func get(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))
	return rr
}

func TestTopologyHandler_Nodes(t *testing.T) {
	t.Parallel()
	local := topology.Node{ID: "self", Interfaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1", ServicePort: 3128}, {Name: "eth1", PrivateIP: "10.0.1.1", ServicePort: 3129}}}
	peer := topology.Node{ID: "n2", Interfaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.2", ServicePort: 3128}}}
	mux := newTopology(t, stubDiscovery{local: local, active: []topology.Node{peer, local}})

	t.Run("local node comes first and is not duplicated", func(t *testing.T) {
		t.Parallel()
		rr := get(t, mux, "/api/nodes")
		var nodes []topology.Node
		if err := json.Unmarshal(rr.Body.Bytes(), &nodes); err != nil {
			t.Fatalf("status=%d body=%s: %v", rr.Code, rr.Body.String(), err)
		}
		if len(nodes) != 2 || nodes[0].ID != "self" || nodes[1].ID != "n2" {
			t.Errorf("nodes = %+v", nodes)
		}
	})

	t.Run("Proxies lists one host:port entry per interface", func(t *testing.T) {
		t.Parallel()
		rr := get(t, mux, "/api/proxies")
		var proxies []handlers.ProxyInfo
		if err := json.Unmarshal(rr.Body.Bytes(), &proxies); err != nil {
			t.Fatal(err)
		}
		want := []handlers.ProxyInfo{{NodeID: "self", Address: "10.0.0.1:3128"}, {NodeID: "self", Address: "10.0.1.1:3129"}, {NodeID: "n2", Address: "10.0.0.2:3128"}}
		if len(proxies) != len(want) {
			t.Fatalf("proxies = %+v", proxies)
		}
		for i := range want {
			if proxies[i] != want[i] {
				t.Errorf("proxies[%d] = %+v, want %+v", i, proxies[i], want[i])
			}
		}
	})
}

// meshWith returns a discovery whose only node exposes the given proxy address.
func meshWith(ip string, port int) stubDiscovery {
	return stubDiscovery{local: topology.Node{ID: "self", Interfaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: ip, ServicePort: port}}}}
}

func TestTopologyHandler_TestProxy_Validation(t *testing.T) {
	t.Parallel()
	mux := newTopology(t, meshWith("127.0.0.1", 1))

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"missing address is rejected with 400", "", http.StatusBadRequest},
		{"address with @ is rejected with 400", "address=user@host:1", http.StatusBadRequest},
		{"address with / is rejected with 400", "address=host:1/path", http.StatusBadRequest},
		{"address with ? is rejected with 400", "address=host:1%3Fx", http.StatusBadRequest},
		{"address with # is rejected with 400", "address=host:1%23frag", http.StatusBadRequest},
		{"address that is not a mesh proxy is rejected with 403", "address=169.254.169.254:80", http.StatusForbidden},
		{"mesh address with a different port is rejected with 403", "address=127.0.0.1:2", http.StatusForbidden},
		{"target with a non-http scheme is rejected with 400", "address=127.0.0.1:1&target=ftp://8.8.8.8/", http.StatusBadRequest},
		{"target without a host is rejected with 400", "address=127.0.0.1:1&target=http:///x", http.StatusBadRequest},
		{"target in a link-local range is rejected with 403", "address=127.0.0.1:1&target=http://169.254.169.254/latest/meta-data/", http.StatusForbidden},
		{"target in a private range is rejected with 403", "address=127.0.0.1:1&target=https://10.0.0.5/", http.StatusForbidden},
		{"target host resolving to loopback is rejected with 403", "address=127.0.0.1:1&target=http://localhost/", http.StatusForbidden},
		{"IPv4-mapped IPv6 target is rejected with 403", "address=127.0.0.1:1&target=http://[::ffff:127.0.0.1]/", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := get(t, mux, "/api/proxies/test?"+tc.query)
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}

	t.Run("a redirect from the target is reported, not followed", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		// Stands in for a mesh proxy whose upstream answers with a redirect
		// to a destination the egress policy would deny.
		proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}))
		t.Cleanup(proxySrv.Close)
		host, portStr, _ := net.SplitHostPort(proxySrv.Listener.Addr().String())
		port, _ := strconv.Atoi(portStr)
		mux := newTopology(t, meshWith(host, port))

		rr := get(t, mux, "/api/proxies/test?address="+proxySrv.Listener.Addr().String()+"&target=http://8.8.8.8/")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", rr.Code, rr.Body.String())
		}
		var out struct {
			Success bool `json:"success"`
			Status  int  `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Success || out.Status != http.StatusFound {
			t.Errorf("out = %+v, want success=false status=302", out)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("proxy received %d requests, want 1 (redirect must not be followed)", n)
		}
	})

	t.Run("mesh address with unreachable proxy and public target answers 200 with success=false", func(t *testing.T) {
		t.Parallel()
		rr := get(t, mux, "/api/proxies/test?address=127.0.0.1:1&target=http://8.8.8.8/")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", rr.Code, rr.Body.String())
		}
		var out struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Success || out.Error == "" {
			t.Errorf("out = %+v, want success=false with error text", out)
		}
	})
}
