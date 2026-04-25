
# --- Configuración y Variables ---
APP_IMPORT_PATH := $(shell go list -m)
ALL_PKGS := $(sort $(shell go list ./...))

# Variables para inyectar en el build
GIT_SHA := $(shell git rev-parse --short HEAD)

DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -ldflags "-s -w \
	-X $(APP_IMPORT_PATH)/internal/version.GitHash=$(GIT_SHA) \
	-X $(APP_IMPORT_PATH)/internal/version.Date=$(DATE)"

# --- Herramientas y Módulos ---
TOOLS_BIN_DIR := $(abspath ./bin)
TOOLS_MOD_DIR := $(abspath ./tools)

export PATH := $(TOOLS_BIN_DIR):$(PATH)

# --- Comandos Base ---
GOBUILD := GO111MODULE=on CGO_ENABLED=0 go build -trimpath

# ====================================================================================
# Comandos Públicos
# ====================================================================================
.PHONY: help all build clean test lint format check-format tools

help:
	@echo "Uso: make [comando]"
	@echo ""
	@echo "## --- Ciclo de Vida del Build ---"
	@echo "  build            Construye los binarios para todas las plataformas."
	@echo "  amd64-build      Construye el binario para linux/amd64."
	@echo "  arm64-build      Construye el binario para linux/arm64."
	@echo "  clean            Elimina la carpeta de build."
	@echo ""
	@echo "## --- Calidad de Código ---"
	@echo "  lint             Ejecuta todos los linters (golangci-lint y staticcheck)."
	@echo "  format           Formatea automáticamente el código con goimports."
	@echo "  check-format     Verifica el formato sin modificar archivos (ideal para CI)."
	@echo "  test             Ejecuta las pruebas unitarias."
	@echo "  test-sonar       Ejecuta pruebas generando reportes para SonarQube."
	@echo ""
	@echo "## --- Gestión de Dependencias ---"
	@echo "  tools            Instala/actualiza las herramientas de desarrollo en ./bin."
	@echo "  mod-tidy         Ejecuta 'go mod tidy' en el módulo principal."
	@echo "  mod-vendor       Ejecuta 'go mod vendor'."


all: check-format lint test build


## ----------------------------------------
## Gestión de Herramientas
## ----------------------------------------
tools: $(TOOLS_BIN_DIR)

$(TOOLS_BIN_DIR): $(TOOLS_MOD_DIR)/tools.go $(TOOLS_MOD_DIR)/go.mod
	@echo "==> Instalando herramientas desde tools/go.mod..."
	@mkdir -p $(TOOLS_BIN_DIR)
	@cd $(TOOLS_MOD_DIR) && go mod tidy
	# Usamos go list para obtener las herramientas de forma robusta
	@cd $(TOOLS_MOD_DIR) && \
		go list -e -f '{{range .Imports}}{{.}} {{end}}' -tags=tools tools.go | \
		xargs -n1 env GOBIN=$(TOOLS_BIN_DIR) go install -v
	@touch $(TOOLS_BIN_DIR)
	@echo "==> Herramientas actualizadas."

.PHONY: tools-force
tools-force:
	@rm -rf $(TOOLS_BIN_DIR)
	@$(MAKE) tools


## ----------------------------------------
## Calidad de Código
## ----------------------------------------
lint: tools
	@echo "==> Ejecutando golangci-lint..."
	@$(TOOLS_BIN_DIR)/golangci-lint run --fix

vulncheck: tools
	@echo "==> Escaneando vulnerabilidades (govulncheck)..."
	@$(TOOLS_BIN_DIR)/govulncheck ./...

format: tools
	@echo "==> Formateando código..."
	@$(TOOLS_BIN_DIR)/gofumpt -l -w .

check-format: tools
	@echo "==> Verificando formato..."
	@if [ -n "$$($(TOOLS_BIN_DIR)/gofumpt -l .)" ]; then \
		echo "ERROR: El código no está formateado con gofumpt."; \
		exit 1; \
	fi


## ----------------------------------------
## Pruebas y SonarQube
## ----------------------------------------
test:
	@echo "==> Ejecutando pruebas unitarias..."
	@go test ./... --cover
	@#go test -race ./...  --cover

test-sonar:
	@echo "==> Generando reportes para SonarQube..."
	@go test -covermode=atomic -coverprofile=coverage.out ./...
	@go test -json ./... > report.json

run-local-sonar:
	docker run -d --name sonarqube -p 9000:9000 sonarqube


## ----------------------------------------
## Build y Clean
## ----------------------------------------
build: clean
	@echo "==> Construyendo binarios..."
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ./build/darwin/amd64/hydra ./cmd/hydra
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o ./build/darwin/arm64/hydra ./cmd/hydra
	GOOS=linux  GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ./build/linux/amd64/hydra ./cmd/hydra
	GOOS=linux  GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o ./build/linux/arm64/hydra ./cmd/hydra

amd64-build: clean
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ./build/linux/amd64/hydra ./cmd/hydra
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ./build/darwin/amd64/hydra ./cmd/hydra


arm64-build: clean
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o ./build/linux/arm64/hydra ./cmd/hydra
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o ./build/darwin/arm64/hydra ./cmd/hydra

clean:
	@echo "==> Limpiando builds anteriores..."
	@rm -rf ./build


## ----------------------------------------
## Gestión de Módulos
## ----------------------------------------
mod-tidy:
	go mod tidy

mod-vendor:
	go mod vendor


## ----------------------------------------
## otras
## ----------------------------------------
.PHONY: docs
docs:
	$(TOOLS_BIN_DIR)/swag init -g internal/entrypoint/httpapi/api.go --parseDependency
