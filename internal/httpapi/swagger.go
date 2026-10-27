package httpapi

import (
	"net/http"
	"sync"

	"github.com/norlis/hydra/docs"
	"github.com/norlis/hydra/internal/version"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

const (
	swaggerRoute    = "GET /swagger/"
	swaggerSpecPath = "/swagger/doc.json"
)

// swaggerSpec renders the spec once: swag's ReadDoc mutates the shared
// SwaggerInfo on every call, which races under concurrent requests. The
// version is only known at build time, so it is stamped here.
var swaggerSpec = sync.OnceValue(func() []byte {
	docs.SwaggerInfo.Version = version.GitHash
	return []byte(docs.SwaggerInfo.ReadDoc())
})

// swaggerHandler serves Swagger UI at /swagger/index.html and the spec at
// /swagger/doc.json.
func swaggerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+swaggerSpecPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(swaggerSpec())
	})
	mux.Handle(swaggerRoute, httpSwagger.Handler(httpSwagger.URL(swaggerSpecPath)))
	return mux
}
