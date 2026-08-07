package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"github.com/norlis/hydra/internal/topology"
	"go.opentelemetry.io/otel/metric/noop"
)

// findLogLine returns the first JSON log record in buf whose msg equals want.
func findLogLine(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec["msg"] == want {
			return rec
		}
	}
	t.Fatalf("no log line with msg=%q in:\n%s", want, buf.String())
	return nil
}

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
	var logBuf bytes.Buffer
	f := NewDualForwarder(iface, nil, []int{80, 443}, classifier, nil, limiter.New(0), 0,
		mtr, slog.New(slog.NewJSONHandler(&logBuf, nil)))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9/", http.NoBody) // loopback -> denied
	rec := httptest.NewRecorder()
	f.httpExternal(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "Egress proxying is denied to host '127.0.0.1'") {
		t.Errorf("unexpected deny body: %q", got)
	}

	rec2 := findLogLine(t, &logBuf, "http ip denied")
	if rec2["error_source"] != "policy" {
		t.Errorf("error_source = %v, want policy", rec2["error_source"])
	}
	if rec2["deny_reason"] != "loopback" {
		t.Errorf("deny_reason = %v, want loopback", rec2["deny_reason"])
	}
	if rec2["host"] != "127.0.0.1:9" {
		t.Errorf("host = %v, want 127.0.0.1:9", rec2["host"])
	}
}
