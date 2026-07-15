//go:build tools
// +build tools

// Este paquete importa dependencias de herramientas que se gestionan a través de go modules.
// De esta forma, podemos fijar las versiones de las herramientas de compilación
// en el archivo go.mod, asegurando compilaciones reproducibles.
// Para más información: https://github.com/go-modules-by-example/index/blob/master/010_tools/README.md

package tools

import (
	// Linters y formateadores
	_ "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	_ "golang.org/x/tools/cmd/goimports"
	_ "mvdan.cc/gofumpt"
	_ "mvdan.cc/sh/v3/cmd/shfmt"

	_ "golang.org/x/vuln/cmd/govulncheck"
	// --- Análisis y Seguridad ---
	_ "honnef.co/go/tools/cmd/staticcheck"

	// Herramientas de desarrollo y testing
	_ "github.com/go-delve/delve/cmd/dlv"
	_ "golang.org/x/tools/gopls"
	_ "gotest.tools/gotestsum"

	// open api
	_ "github.com/swaggo/swag/cmd/swag"
	// validations
	//_ "cuelang.org/go/cmd/cue"
)
