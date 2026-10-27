package proxy

import "net/http"

// Route is the router's verdict for one request. The forwarder logs the
// decision itself so the completion line states why the bytes left where
// they did, not a guess rebuilt from the peer address.
type Route struct {
	Peer     string // host:port of the hydra proxy to relay through; "" = egress here
	Decision string
}

// Forwarder moves the bytes of one request, either straight to the
// destination or through the hydra proxy named by the route.
type Forwarder interface {
	Serve(w http.ResponseWriter, r *http.Request, route Route)
}
