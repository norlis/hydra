package proxy

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Auth modes accepted by HYDRA_PROXY_AUTH_MODE.
const (
	AuthModeNone  = "none"
	AuthModeBasic = "basic"
)

// proxyAuthRealm is what we put in Proxy-Authenticate. Generic on
// purpose — the realm is not used for routing, just to give clients a
// human-readable handle.
const proxyAuthRealm = "hydra"

// Sentinel errors returned by WrapAuth so callers can match with errors.Is.
var (
	ErrAuthMissingCredentials = errors.New("proxy: basic auth requires user and pass to be set")
	ErrUnknownAuthMode        = errors.New("proxy: unknown auth mode")
)

// WrapAuth applies the configured authentication policy in front of
// next. It returns next unchanged when mode is AuthModeNone so callers
// don't pay any per-request cost on a wide-open proxy.
//
// Peer hops are authenticated like any client: every node shares the
// same credentials by configuration and sends them on each hop (see
// BasicCredential), so a client cannot forge the hop header to bypass
// the check.
func WrapAuth(mode, user, pass string, next http.Handler, log *slog.Logger) (http.Handler, error) {
	switch mode {
	case "", AuthModeNone:
		return next, nil
	case AuthModeBasic:
		if user == "" || pass == "" {
			return nil, ErrAuthMissingCredentials
		}
		return &basicAuth{
			user:  user,
			pass:  pass,
			realm: proxyAuthRealm,
			next:  next,
			log:   log,
		}, nil
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownAuthMode, mode)
	}
}

// basicAuth enforces RFC 7617 Proxy-Authorization. On failure it
// answers 407 with a Proxy-Authenticate challenge so well-behaved
// clients (curl, browser, Go's http.ProxyConnectHeader) prompt for
// credentials and retry.
type basicAuth struct {
	user, pass string
	realm      string
	next       http.Handler
	log        *slog.Logger
}

func (b *basicAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := parseProxyBasicAuth(r.Header.Get("Proxy-Authorization"))
	if !ok || !credentialsMatch(b.user, b.pass, user, pass) {
		b.log.WarnContext(r.Context(), "proxy auth required",
			slog.String(keyServerAddress, r.Host),
			slog.String(keyErrorSource, errSourcePolicy),
			slog.String(keyDenyReason, denyReasonAuth))
		w.Header().Set("Proxy-Authenticate", `Basic realm="`+b.realm+`"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}

	// Strip the credential before handing off; stripControlHeaders
	// will also remove it on the external HTTP path (Proxy-Authorization
	// is hop-by-hop), but doing it here ensures CONNECT and peer-relay
	// paths also never see it.
	r.Header.Del("Proxy-Authorization")
	b.next.ServeHTTP(w, r)
}

// BasicCredential renders the Proxy-Authorization value for user/pass per
// RFC 7617, or "" when both are empty (no proxy authentication).
func BasicCredential(user, pass string) string {
	if user == "" && pass == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// parseProxyBasicAuth extracts the user/pass pair from a
// Proxy-Authorization header value. Returns ok=false on any decoding
// or format issue — the caller should treat that as "no auth".
// The scheme is case-insensitive and may be followed by any amount of
// whitespace (RFC 7235 §2.1).
func parseProxyBasicAuth(header string) (user, pass string, ok bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Basic") {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", "", false
	}
	s := string(raw)
	before, after, ok0 := strings.Cut(s, ":")
	if !ok0 {
		return "", "", false
	}
	return before, after, true
}

// credentialsMatch compares user/pass in constant time so a wrong
// guess can't be distinguished from a correct one by timing the
// response. Falling back to subtle.ConstantTimeCompare for both
// fields keeps the comparison data-independent.
func credentialsMatch(wantUser, wantPass, gotUser, gotPass string) bool {
	u := subtle.ConstantTimeCompare([]byte(wantUser), []byte(gotUser))
	p := subtle.ConstantTimeCompare([]byte(wantPass), []byte(gotPass))
	return u == 1 && p == 1
}
