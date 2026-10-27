package proxy_test

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/norlis/hydra/internal/proxy"
	"github.com/norlis/hydra/internal/testlog"
)

const (
	authUser = "alice"
	authPass = "s3cret"
)

// recordingHandler is the handler behind the auth wrapper. It records
// whether it ran and which Proxy-Authorization value reached it.
type recordingHandler struct {
	called    bool
	proxyAuth string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	h.proxyAuth = r.Header.Get("Proxy-Authorization")
	w.WriteHeader(http.StatusOK)
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestWrapAuth_Config(t *testing.T) {
	t.Parallel()
	discard := slog.New(slog.DiscardHandler)

	t.Run("mode none returns the handler unwrapped", func(t *testing.T) {
		t.Parallel()
		next := &recordingHandler{}
		h, err := proxy.WrapAuth(proxy.AuthModeNone, "", "", next, discard)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
		req.Header.Set("Proxy-Authorization", "whatever")
		h.ServeHTTP(httptest.NewRecorder(), req)
		if !next.called || next.proxyAuth != "whatever" {
			t.Errorf("next called=%v proxyAuth=%q; want called with header intact", next.called, next.proxyAuth)
		}
	})

	t.Run("empty mode equals none", func(t *testing.T) {
		t.Parallel()
		next := &recordingHandler{}
		h, err := proxy.WrapAuth("", "", "", next, discard)
		if err != nil {
			t.Fatal(err)
		}
		if h != next {
			t.Errorf("WrapAuth(\"\") returned a wrapper; want next itself")
		}
	})

	tests := []struct {
		name    string
		user    string
		pass    string
		mode    string
		wantErr error
	}{
		{"basic without user returns ErrAuthMissingCredentials", "", authPass, proxy.AuthModeBasic, proxy.ErrAuthMissingCredentials},
		{"basic without password returns ErrAuthMissingCredentials", authUser, "", proxy.AuthModeBasic, proxy.ErrAuthMissingCredentials},
		{"unknown mode returns ErrUnknownAuthMode", authUser, authPass, "digest", proxy.ErrUnknownAuthMode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := proxy.WrapAuth(tc.mode, tc.user, tc.pass, &recordingHandler{}, discard)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestBasicAuth_ServeHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		hop        bool
		wantStatus int
		wantNext   bool
	}{
		{"valid credentials pass and the header is removed before next", basicHeader(authUser, authPass), false, http.StatusOK, true},
		{"missing header is rejected with 407", "", false, http.StatusProxyAuthRequired, false},
		{"wrong password is rejected with 407", basicHeader(authUser, "nope"), false, http.StatusProxyAuthRequired, false},
		{"wrong user is rejected with 407", basicHeader("bob", authPass), false, http.StatusProxyAuthRequired, false},
		{"invalid base64 is rejected with 407", "Basic %%%", false, http.StatusProxyAuthRequired, false},
		{"missing colon separator is rejected with 407", "Basic " + base64.StdEncoding.EncodeToString([]byte(authUser)), false, http.StatusProxyAuthRequired, false},
		{"scheme is matched case-insensitively", "basic " + base64.StdEncoding.EncodeToString([]byte(authUser+":"+authPass)), false, http.StatusOK, true},
		{"extra whitespace between scheme and token is tolerated", "Basic   " + base64.StdEncoding.EncodeToString([]byte(authUser+":"+authPass)), false, http.StatusOK, true},
		{"scheme other than Basic is rejected with 407", "Bearer " + base64.StdEncoding.EncodeToString([]byte(authUser+":"+authPass)), false, http.StatusProxyAuthRequired, false},
		{"password is compared whole even when it contains a colon", basicHeader(authUser, authPass+":x"), false, http.StatusProxyAuthRequired, false},
		{"hop header without credentials is rejected with 407", "", true, http.StatusProxyAuthRequired, false},
		{"hop header with valid credentials passes", basicHeader(authUser, authPass), true, http.StatusOK, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := &recordingHandler{}
			h, err := proxy.WrapAuth(proxy.AuthModeBasic, authUser, authPass, next, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
			if tc.header != "" {
				req.Header.Set("Proxy-Authorization", tc.header)
			}
			if tc.hop {
				req.Header.Set(proxy.HopHeader, "1")
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if next.called != tc.wantNext {
				t.Errorf("next called = %v, want %v", next.called, tc.wantNext)
			}
			if tc.wantNext && next.proxyAuth != "" {
				t.Errorf("Proxy-Authorization reached next: %q", next.proxyAuth)
			}
			if tc.wantStatus == http.StatusProxyAuthRequired {
				if got := rr.Header().Get("Proxy-Authenticate"); got != `Basic realm="hydra"` {
					t.Errorf("Proxy-Authenticate = %q", got)
				}
			}
		})
	}
}

func TestBasicCredential(t *testing.T) {
	t.Parallel()
	got := proxy.BasicCredential(authUser, authPass)
	if got != basicHeader(authUser, authPass) {
		t.Errorf("BasicCredential = %q, want %q", got, basicHeader(authUser, authPass))
	}
	if proxy.BasicCredential("", "") != "" {
		t.Error("BasicCredential with empty user and password should be empty")
	}
}

func TestBasicAuth_Logging(t *testing.T) {
	t.Parallel()

	serve := func(t *testing.T, header string) *testlog.Recorder {
		t.Helper()
		rec := testlog.New()
		h, err := proxy.WrapAuth(proxy.AuthModeBasic, authUser, authPass, &recordingHandler{}, rec.Logger())
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
		if header != "" {
			req.Header.Set("Proxy-Authorization", header)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return rec
	}

	t.Run("a 407 logs exactly one structured warning without credentials", func(t *testing.T) {
		t.Parallel()
		wrong := basicHeader(authUser, "wrong-pass")
		entries := serve(t, wrong).Entries()
		if len(entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(entries))
		}
		e := entries[0]
		if e.Level != slog.LevelWarn {
			t.Errorf("level = %v, want WARN", e.Level)
		}
		if e.String("error_source") != "policy" || e.String("deny_reason") != "auth_required" {
			t.Errorf("attrs = %v", e.Attrs)
		}
		for k, v := range e.Attrs {
			s := v.String()
			if strings.Contains(s, authPass) || strings.Contains(s, "wrong-pass") || strings.Contains(s, wrong) {
				t.Errorf("attr %s leaks credential material: %q", k, s)
			}
		}
	})

	t.Run("a successful request logs nothing", func(t *testing.T) {
		t.Parallel()
		if got := len(serve(t, basicHeader(authUser, authPass)).Entries()); got != 0 {
			t.Errorf("entries = %d, want 0", got)
		}
	})
}
