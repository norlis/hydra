# `/tools`

Esta carpeta contiene herramientas de soporte para el proyecto, gestionadas como dependencias de módulo a través de Go. Esto nos permite fijar versiones específicas de cada herramienta para asegurar un entorno de desarrollo y CI/CD consistente y reproducible.

## ¿Cómo funciona?

El archivo `tools.go` importa los paquetes de las herramientas. La etiqueta de compilación `//go:build tools` previene que estas herramientas se incluyan en el binario final de nuestra aplicación. Sin embargo, al estar en el `import`, el gestor de módulos de Go (`go mod`) rastreará sus versiones en los archivos `go.mod` y `go.sum`.

## Instalación

Para instalar todas las herramientas definidas en `go.mod`, ejecuta el siguiente comando desde la raíz del proyecto. Esto instalará los binarios en tu `$GOPATH/bin`.

```bash
# Desde la raíz del proyecto (o dentro de la carpeta tools):
cd tools
go mod tidy
go list -e -f '{{range .Imports}}{{.}} {{end}}' -tags=tools tools.go | xargs go install -v
```