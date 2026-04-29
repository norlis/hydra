package httpapi

import (
	"net/http"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/middleware"
	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	"github.com/norlis/httpgate/pkg/application/health"
	"github.com/norlis/httpgate/pkg/port"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Router               *http.ServeMux
	Logger               *zap.Logger
	Render               presenters.Presenters
	Status               *health.Status
	TopologyHandler      *handlers.TopologyHandler
	WebHandler           *handlers.WebHandler
	NodeReadinessChecker *cluster.NodeReadinessChecker
}

// NewHttpApi
// @title           hydra
// @version         1.0
// @description     Esta es una API generada automáticamente con Swaggo.
// @termsOfService  http://swagger.io/terms/
// @contact.name   Norlis Viamonte
// @contact.url    http://www.example.com/support
// @contact.email  norlis.viamonte@gmail.com
// @host
// @BasePath  /
// @openapi 3.0.0.
func NewHttpApi(params Params) {
	base := []middleware.Middleware{
		middleware.TraceId(middleware.WithHeaderName("x-request-id"), middleware.WithLogger(params.Logger)),
		middleware.APIErrorMiddleware(
			middleware.WithIntercept(http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusInternalServerError),
			middleware.WithCustomMessage(http.StatusNotFound, "resource not found"),
			middleware.WithCustomMessage(http.StatusMethodNotAllowed, "method is not allowed for this resource."),
		),
		middleware.Recover(params.Logger, params.Render),
		middleware.RequestLogger(params.Logger),
		middleware.AllowAll(params.Logger).Middleware,
	}

	use := middleware.Chain(base...)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/nodes", params.TopologyHandler.Nodes)
	api.HandleFunc("GET /api/proxies", params.TopologyHandler.Proxies)
	api.HandleFunc("GET /api/proxies/test", params.TopologyHandler.TestProxy)

	params.Router.Handle("/api/", use(api))

	// SSE: registered directly to bypass response-buffering middleware that
	// wraps ResponseWriter without implementing http.Flusher / Unwrap.
	params.Router.HandleFunc("GET /api/events", params.TopologyHandler.Events)

	// UI
	params.Router.HandleFunc("GET /events", params.WebHandler.EventsPage)
	params.Router.Handle("GET /assets/", http.StripPrefix("/assets/", params.WebHandler.ServeAssets()))

	// health
	params.Router.Handle("GET /health", use(health.NewProbe(map[string]port.Checker{
		"node": params.NodeReadinessChecker,
	})))
	params.Router.Handle("GET /live", use(params.Status))
}
