package proxy

import "net/http"

// MeshRouter es el punto de entrada HTTP. Evalúa si la petición
// se queda o salta a otro nodo.
type MeshRouter interface {
	// ServeHTTP implementa http.Handler de la librería estándar de Go.
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// Forwarder se encarga de mover los bytes de un lado a otro.
type Forwarder interface {
	// ForwardToPeer envía la petición internamente a otro Proxy del clúster.
	ForwardToPeer(targetAddress string, w http.ResponseWriter, r *http.Request)

	// ForwardToExternal envía la petición finalmente hacia internet (o el servicio final).
	ForwardToExternal(w http.ResponseWriter, r *http.Request)
}
