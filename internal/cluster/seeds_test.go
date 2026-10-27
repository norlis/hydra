package cluster

import (
	"slices"
	"testing"
)

func TestMergeSeeds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		static     []string
		discovered []string
		want       []string
	}{
		{"unions and deduplicates", []string{"a:1", "b:1"}, []string{"b:1", "c:1"}, []string{"a:1", "b:1", "c:1"}},
		{"both empty yields an empty non-nil slice", nil, nil, []string{}},
		{"static only", []string{"a:1"}, nil, []string{"a:1"}},
		{"discovered only", nil, []string{"c:1"}, []string{"c:1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mergeSeeds(tc.static, tc.discovered)
			if got == nil {
				t.Fatal("mergeSeeds returned nil")
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("mergeSeeds = %v, want %v", got, tc.want)
			}
		})
	}
}
