package httpapi

import "net/http"

// Route is implemented by domain handlers that self-register their /api/
// JSON routes onto the chained sub-mux, collected via the fx "routes" group.
type Route interface {
	Register(mux *http.ServeMux)
}
