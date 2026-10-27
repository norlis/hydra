package network_test

import (
	"testing"

	"github.com/norlis/hydra/internal/network"
)

func TestBuildServicePort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		base  int
		index int
		want  int
	}{
		{"base plus index", 4000, 2, 4002},
		{"zero base uses the default port", 0, 1, network.DefaultServicePort + 1},
		{"negative base uses the default port", -5, 0, network.DefaultServicePort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := network.BuildServicePort(tc.base, tc.index); got != tc.want {
				t.Errorf("BuildServicePort(%d,%d) = %d, want %d", tc.base, tc.index, got, tc.want)
			}
		})
	}
}
