package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/httpx"
)

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeResolver implements EntityResolver deterministically for tests.
type fakeResolver struct {
	n        int
	byEntity map[string]*cluster.Endpoint
}

func (f *fakeResolver) Len() int { return f.n }

func (f *fakeResolver) EndpointFor(entityID string) *cluster.Endpoint {
	if f.n == 0 {
		return nil
	}
	if ep, ok := f.byEntity[entityID]; ok {
		return ep
	}
	// Default: echo the id as a virtual node id so tests can assert the "::" split.
	return &cluster.Endpoint{NodeID: "node-" + entityID + "::eth0", ProxyAddr: "10.0.0.1:3128"}
}

func newHandler(f *fakeResolver) *ResolveHandler {
	return &ResolveHandler{ring: f, render: httpx.New(slogDiscard()), logger: slogDiscard()}
}

func post(t *testing.T, h *ResolveHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/resolve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Resolve(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]resolvedNode {
	t.Helper()
	var m map[string]resolvedNode
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON response %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestResolveRingEmptyReturns503(t *testing.T) {
	t.Parallel()
	rec := post(t, newHandler(&fakeResolver{n: 0}), `["a"]`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body %q", rec.Code, rec.Body.String())
	}
}

func TestResolveMapping(t *testing.T) {
	t.Parallel()
	f := &fakeResolver{n: 2, byEntity: map[string]*cluster.Endpoint{
		"a": {NodeID: "dev1::bridge100", ProxyAddr: "192.168.139.3:3129"},
		"b": {NodeID: "dev2::eth0", ProxyAddr: "192.168.10.5:3128"},
	}}
	rec := post(t, newHandler(f), `["a","b"]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	m := decodeMap(t, rec)
	if m["a"] != (resolvedNode{NodeID: "dev1", Address: "192.168.139.3:3129"}) {
		t.Errorf("a = %+v, want {dev1 192.168.139.3:3129}", m["a"])
	}
	if m["b"] != (resolvedNode{NodeID: "dev2", Address: "192.168.10.5:3128"}) {
		t.Errorf("b = %+v, want {dev2 192.168.10.5:3128}", m["b"])
	}
}

func TestResolveDedupAndDropEmpty(t *testing.T) {
	t.Parallel()
	rec := post(t, newHandler(&fakeResolver{n: 1}), `["a","a",""]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	m := decodeMap(t, rec)
	if len(m) != 1 {
		t.Errorf("len = %d, want 1 (dedup + drop empty): %+v", len(m), m)
	}
	if _, ok := m["a"]; !ok {
		t.Error(`missing key "a"`)
	}
	if _, ok := m[""]; ok {
		t.Error(`empty key must be dropped`)
	}
}

func TestResolveVerbatimNoTrim(t *testing.T) {
	t.Parallel()
	rec := post(t, newHandler(&fakeResolver{n: 1}), `["a"," a"]`)
	m := decodeMap(t, rec)
	if len(m) != 2 {
		t.Errorf("len = %d, want 2 (values hashed verbatim, no trim): %+v", len(m), m)
	}
	if _, ok := m[" a"]; !ok {
		t.Error(`" a" (leading space) must be a distinct key`)
	}
}

func TestResolveMalformedJSONReturns400(t *testing.T) {
	t.Parallel()
	for i, body := range []string{`{"not":"an array"}`, `not json`, `[1,2,3]`} {
		rec := post(t, newHandler(&fakeResolver{n: 1}), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if i == 0 {
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
		}
	}
}

func TestResolveTooManyIDsReturns400(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteByte('[')
	for i := range 10001 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote("id-" + strconv.Itoa(i)))
	}
	b.WriteByte(']')
	rec := post(t, newHandler(&fakeResolver{n: 1}), b.String())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for >10000 ids", rec.Code)
	}
}

func TestResolveEmptyListReturns200Empty(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`[]`, `[""]`} {
		rec := post(t, newHandler(&fakeResolver{n: 1}), body)
		if rec.Code != http.StatusOK {
			t.Errorf("body %q: status = %d, want 200", body, rec.Code)
		}
		if m := decodeMap(t, rec); len(m) != 0 {
			t.Errorf("body %q: want empty object, got %+v", body, m)
		}
	}
}
