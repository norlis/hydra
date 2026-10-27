package proxy

import (
	"net/http"
	"time"
)

// SetHandshakeTimeout overrides the peer handshake deadline of one forwarder.
// Call it before the forwarder serves any request.
func SetHandshakeTimeout(f *DualForwarder, d time.Duration) { f.handshakeTimeout = d }

// RouteFor builds the route the router would pass for a request already
// resolved to peer, so forwarder tests can drive Serve directly.
func RouteFor(r *http.Request, peer string) Route {
	switch {
	case r.Header.Get(HopHeader) != "":
		return Route{Decision: decisionHopLocal}
	case peer != "":
		return Route{Peer: peer, Decision: decisionPeer}
	default:
		return Route{Decision: decisionLocal}
	}
}
