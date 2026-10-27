package main

import (
	"testing"

	"go.uber.org/fx"
)

// The dependency graph must resolve: every constructor's parameters are
// provided and annotations (fx.From, fx.As, groups) match. ValidateApp
// does not run constructors or lifecycle hooks, so no ports are bound.
func TestDependencyGraphResolves(t *testing.T) {
	t.Parallel()
	if err := fx.ValidateApp(options()); err != nil {
		t.Fatalf("fx graph: %v", err)
	}
}
