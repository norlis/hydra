package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The goroutine leak profile (Go 1.27) is the one most worth having on a
// long-running proxy; pin that the admin mux actually exposes it.
func TestMountAdminEndpoints_ServesGoroutineLeakProfile(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	MountAdminEndpoints(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/goroutineleak?debug=1", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
