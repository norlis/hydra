package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/norlis/httpgate/health"
	"github.com/norlis/httpgate/middleware"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"github.com/norlis/hydra/pkg/logger"
	"go.uber.org/fx"
)

// logComponent tags every control-plane log record so it can be filtered
// apart from the data plane (see pkg/logger.WithComponent).
const logComponent = "control"

type Params struct {
	fx.In
	Router               *http.ServeMux
	Logger               *slog.Logger
	Status               *health.Status
	Readiness            *health.Readiness
	EventsHandler        *handlers.EventsHandler
	WebHandler           *handlers.WebHandler
	NodeReadinessChecker *cluster.NodeReadinessChecker
	Routes               []Route `group:"routes"`
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
	log := logger.WithComponent(params.Logger, logComponent)
	base := []middleware.Middleware{
		middleware.TraceContext(middleware.WithResponseHeader("X-Request-ID")),
		middleware.InterceptStatus(
			middleware.WithIntercept(http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusInternalServerError),
			middleware.WithMessage(http.StatusNotFound, "resource not found"),
			middleware.WithMessage(http.StatusMethodNotAllowed, "method is not allowed for this resource."),
		),
		middleware.Recover(log),
		middleware.RequestLogger(log, middleware.WithSkipPaths("/health", "/live", "/ready")),
		middleware.AllowAll(),
	}

	chain := middleware.New(base...)

	api := http.NewServeMux()
	for _, route := range params.Routes {
		route.Register(api)
	}
	params.Router.Handle("/api/", chain.Then(api))

	// SSE: registered directly to bypass response-buffering middleware that
	// wraps ResponseWriter without implementing http.Flusher / Unwrap.
	params.Router.HandleFunc("GET /api/events", params.EventsHandler.Events)

	// UI
	params.Router.HandleFunc("GET /events", params.WebHandler.EventsPage)
	params.Router.Handle("GET /assets/", http.StripPrefix("/assets/", params.WebHandler.ServeAssets()))

	// health / readiness / liveness
	params.Router.Handle("GET /health", chain.Then(health.NewProbe(map[string]health.Checker{
		"node": params.NodeReadinessChecker,
	})))
	params.Router.Handle("GET /ready", chain.Then(params.Readiness))
	params.Router.Handle("GET /live", chain.Then(params.Status))
}
