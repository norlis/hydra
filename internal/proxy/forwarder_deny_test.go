package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestHostOnly(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"169.254.169.254:443": "169.254.169.254",
		"example.com:80":      "example.com",
		"169.254.169.254":     "169.254.169.254", // no port
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDenyBody(t *testing.T) {
	t.Parallel()
	ip := netip.MustParseAddr("169.254.169.254")

	def := &ipcheck.DeniedError{IP: ip, Reason: ipcheck.DenyLinkLocal}
	if got, want := denyBody("169.254.169.254:443", def),
		"Egress proxying is denied to host '169.254.169.254': default deny policy."; got != want {
		t.Errorf("default rule: got %q", got)
	}

	cfg := &ipcheck.DeniedError{IP: ip, Reason: ipcheck.DenyConfigured}
	if got, want := denyBody("10.1.2.3:80", cfg),
		"Egress proxying is denied to host '10.1.2.3': configured deny rule."; got != want {
		t.Errorf("configured rule: got %q", got)
	}
}

// End-to-end: a real DualForwarder handling an HTTP request to a denied IP
// (loopback) must answer 403 with the deny body, exercising the actual
// ReverseProxy -> Transport -> Dialer.Control chain. Proven to reproduce the
// current 502 bug as the red baseline.
func TestHTTPExternalDeniedReturns403(t *testing.T) {
	t.Parallel()
	mtr, err := metrics.New(noop.NewMeterProvider())
	if err != nil {
		t.Fatal(err)
	}
	classifier, err := ipcheck.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	iface := topology.NetworkInterface{Name: "t", PrivateIP: "127.0.0.1", ServicePort: 3128}
	logs := testlog.New()
	f := NewDualForwarder(iface,
		ForwarderConfig{AllowedPorts: []int{80, 443}},
		ForwarderDeps{Classifier: classifier, TunnelLimit: limiter.New(0), Metrics: mtr},
		logs.Logger())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/", http.NoBody) // loopback -> denied
	rec := httptest.NewRecorder()
	f.httpExternal(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "Egress proxying is denied to host '127.0.0.1'") {
		t.Errorf("unexpected deny body: %q", got)
	}

	var denied []testlog.Entry
	for _, e := range logs.Entries() {
		if e.Message == "http ip denied" {
			denied = append(denied, e)
		}
	}
	if len(denied) != 1 {
		t.Fatalf("logged %d \"http ip denied\" entries, want exactly 1: %+v", len(denied), logs.Entries())
	}
	for key, want := range map[string]string{
		"error_source":   "policy",
		"deny_reason":    "loopback",
		"server.address": "127.0.0.1:9",
	} {
		if got := denied[0].String(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
