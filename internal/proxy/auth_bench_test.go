package proxy_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/norlis/hydra/internal/proxy"
)

func BenchmarkBasicAuth(b *testing.B) {
	h, err := proxy.WrapAuth(proxy.AuthModeBasic, authUser, authPass, &recordingHandler{}, slog.New(slog.DiscardHandler))
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name   string
		header string
	}{
		{"valida", basicHeader(authUser, authPass)},
		{"rechaza", basicHeader(authUser, "nope")},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "http://example.com/", http.NoBody)
			req.Header.Set("Proxy-Authorization", tc.header)
			rr := httptest.NewRecorder()
			b.ReportAllocs()
			for b.Loop() {
				req.Header.Set("Proxy-Authorization", tc.header)
				rr.Body.Reset()
				h.ServeHTTP(rr, req)
			}
		})
	}
}
