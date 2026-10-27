package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/version"
)

func serveSwagger(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(swaggerRoute, swaggerHandler())
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
	return rr
}

func TestSwagger_ServesGeneratedSpecWithControlPlaneRoutes(t *testing.T) {
	t.Parallel()
	rr := serveSwagger(t, "/swagger/doc.json")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, `"/api/nodes"`) {
		t.Fatalf("spec does not document /api/nodes: %s", body)
	}
}

func TestSwagger_ServesUIPage(t *testing.T) {
	t.Parallel()
	rr := serveSwagger(t, "/swagger/index.html")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "doc.json") {
		t.Fatal("UI page does not point at the generated spec")
	}
}

func TestSwagger_SpecReportsBuildVersion(t *testing.T) {
	t.Parallel()
	rr := serveSwagger(t, "/swagger/doc.json")
	var spec struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decoding spec: %v", err)
	}
	if spec.Info.Version != version.GitHash {
		t.Fatalf("info.version = %q, want build version %q", spec.Info.Version, version.GitHash)
	}
}
