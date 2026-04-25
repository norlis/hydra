package httpapi

import (
	"net/http"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/middleware"
	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Router          *http.ServeMux
	Logger          *zap.Logger
	Render          presenters.Presenters
	TopologyHandler *handlers.TopologyHandler
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

	params.Router.Handle("/api/", use(api))
	// params.Router.Handle("GET /status", use(params.Status))
	// params.Router.Handle("GET /health", use(health.NewProbe(nil)))
}
