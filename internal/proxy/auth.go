package proxy

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
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

// WrapAuth applies the configured authentication policy in front of
// next. It returns next unchanged when mode is AuthModeNone so callers
// don't pay any per-request cost on a wide-open proxy.
//
// Peer hops (HopHeader set) are exempt from the check: their
// authentication is implicit in the inter-node trust boundary, and
// requiring credentials on every relayed hop would force every node
// to share secrets that are already distributed by configuration.
func WrapAuth(mode, user, pass string, next http.Handler) (http.Handler, error) {
	switch mode {
	case "", AuthModeNone:
		return next, nil
	case AuthModeBasic:
		if user == "" || pass == "" {
			return nil, fmt.Errorf("proxy: basic auth requires user and pass to be set")
		}
		return &basicAuth{
			user:  user,
			pass:  pass,
			realm: proxyAuthRealm,
			next:  next,
		}, nil
	default:
		return nil, fmt.Errorf("proxy: unknown auth mode %q", mode)
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
}

func (b *basicAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(HopHeader) != "" {
		// Peer-relayed request: trust the upstream peer's decision.
		b.next.ServeHTTP(w, r)
		return
	}

	user, pass, ok := parseProxyBasicAuth(r.Header.Get("Proxy-Authorization"))
	if !ok || !credentialsMatch(b.user, b.pass, user, pass) {
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

// parseProxyBasicAuth extracts the user/pass pair from a
// Proxy-Authorization header value. Returns ok=false on any decoding
// or format issue — the caller should treat that as "no auth".
func parseProxyBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(header[len(prefix):])
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
