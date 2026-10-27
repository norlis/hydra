package proxy_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/norlis/httpgate/logging"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/proxy"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
	"go.opentelemetry.io/otel/attribute"
)

const (
	metricRequests = "hydra.proxy.requests"
	peerAddr       = "10.0.0.2:3128"
)

type fakeResolver struct{ ep *cluster.Endpoint }

func (f fakeResolver) EndpointFor(string) *cluster.Endpoint { return f.ep }

// fakeForwarder records which path the router chose and answers status.
type fakeForwarder struct {
	status            int
	peer              string
	decision          string
	external, viaPeer int
}

func (f *fakeForwarder) Serve(w http.ResponseWriter, _ *http.Request, route proxy.Route) {
	f.decision = route.Decision
	if route.Peer == "" {
		f.external++
	} else {
		f.viaPeer++
		f.peer = route.Peer
	}
	w.WriteHeader(f.status)
}

func newRouter(t *testing.T, resolver proxy.EndpointResolver, fwd *fakeForwarder) (*proxy.Router, *testlog.Recorder) {
	t.Helper()
	m, _ := newTestMetrics(t)
	rec := testlog.New()
	return proxy.NewRouter(topology.NetworkInterface{Name: "lo"}, resolver, fwd, m, rec.Logger()), rec
}

func TestRouter_Decision(t *testing.T) {
	t.Parallel()
	other := &cluster.Endpoint{NodeID: "n2::eth0", ProxyAddr: peerAddr}
	self := &cluster.Endpoint{NodeID: "self::lo", Iface: "lo", ProxyAddr: "10.0.0.1:3128", IsSelf: true}
	sibling := &cluster.Endpoint{NodeID: "self::utun32", Iface: "utun32", ProxyAddr: "10.0.0.1:3130", IsSelf: true}

	tests := []struct {
		name         string
		resolver     proxy.EndpointResolver
		entity       string
		noEntity     bool
		hop          bool
		wantPeer     string
		wantDecision string
	}{
		{"missing entity header routes locally", fakeResolver{other}, "", true, false, "", "local"},
		{"empty entity header routes locally", fakeResolver{other}, "", false, false, "", "local"},
		{"a 10000-character entity is resolved", fakeResolver{other}, strings.Repeat("x", 10_000), false, false, peerAddr, "peer"},
		{"nil endpoint routes locally", fakeResolver{nil}, "t1", false, false, "", "local"},
		{"an entity owned by this interface routes locally", fakeResolver{self}, "t1", false, false, "", "local"},
		{"an entity owned by another interface of this node is handed off to it", fakeResolver{sibling}, "t1", false, false, "10.0.0.1:3130", "handoff"},
		{"peer endpoint forwards to its proxy address", fakeResolver{other}, "t1", false, false, peerAddr, "peer"},
		{"client-set hop header bypasses the ring and routes locally", fakeResolver{other}, "t1", false, true, "", "hop-local"},
		{"nil resolver routes locally", nil, "t1", false, false, "", "local"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fwd := &fakeForwarder{status: http.StatusOK}
			r, _ := newRouter(t, tc.resolver, fwd)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
			if !tc.noEntity {
				req.Header.Set(proxy.EntityHeader, tc.entity)
			}
			if tc.hop {
				req.Header.Set(proxy.HopHeader, "1")
			}
			r.ServeHTTP(httptest.NewRecorder(), req)

			if tc.wantPeer == "" && (fwd.external != 1 || fwd.viaPeer != 0) {
				t.Errorf("external=%d viaPeer=%d, want local", fwd.external, fwd.viaPeer)
			}
			if tc.wantPeer != "" && (fwd.viaPeer != 1 || fwd.peer != tc.wantPeer) {
				t.Errorf("viaPeer=%d peer=%q, want %q", fwd.viaPeer, fwd.peer, tc.wantPeer)
			}
			if fwd.decision != tc.wantDecision {
				t.Errorf("decision = %q, want %q", fwd.decision, tc.wantDecision)
			}
		})
	}
}

func TestRouter_MetricsAndLog(t *testing.T) {
	t.Parallel()

	t.Run("an HTTP request via peer counts requests and emits the canonical log", func(t *testing.T) {
		t.Parallel()
		fwd := &fakeForwarder{status: http.StatusNotFound}
		m, reader := newTestMetrics(t)
		rec := testlog.New()
		r := proxy.NewRouter(topology.NetworkInterface{Name: "lo"}, fakeResolver{&cluster.Endpoint{ProxyAddr: peerAddr}}, fwd, m, rec.Logger())
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
		req.Header.Set(proxy.EntityHeader, "t1")
		r.ServeHTTP(httptest.NewRecorder(), req)

		got := counterValue(t, reader, metricRequests,
			attribute.String("method", http.MethodGet), attribute.String("status_class", "4xx"), attribute.String("decision", "peer"))
		if got != 1 {
			t.Errorf("requests{GET,4xx,peer} = %d, want 1", got)
		}
		var completed []testlog.Entry
		for _, e := range rec.Entries() {
			if e.Message == "request completed" {
				completed = append(completed, e)
			}
		}
		if len(completed) != 1 {
			t.Fatalf("'request completed' entries = %d, want 1", len(completed))
		}
		e := completed[0]
		if e.Level != slog.LevelInfo || e.String("decision") != "peer" || e.String("peer") != peerAddr || e.String(logging.KeyHTTPResponseStatusCode) != "404" {
			t.Errorf("entry = %v", e.Attrs)
		}
	})

	t.Run("a CONNECT request neither counts requests nor logs from the router", func(t *testing.T) {
		t.Parallel()
		fwd := &fakeForwarder{status: http.StatusOK}
		m, reader := newTestMetrics(t)
		rec := testlog.New()
		r := proxy.NewRouter(topology.NetworkInterface{Name: "lo"}, fakeResolver{nil}, fwd, m, rec.Logger())
		req := httptest.NewRequestWithContext(t.Context(), http.MethodConnect, "example.com:443", http.NoBody)
		r.ServeHTTP(httptest.NewRecorder(), req)

		if got := counterValue(t, reader, metricRequests); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
		for _, e := range rec.Entries() {
			if e.Message == "request completed" {
				t.Error("router logged 'request completed' for CONNECT")
			}
		}
		if fwd.external != 1 {
			t.Errorf("external = %d, want 1", fwd.external)
		}
	})
}
