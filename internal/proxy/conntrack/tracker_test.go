package conntrack_test

import (
	"net"
	"testing"

	"github.com/norlis/hydra/internal/proxy/conntrack"
)

func TestTracker(t *testing.T) {
	t.Parallel()

	t.Run("Add chains the previous onClose and forgets the conn on close", func(t *testing.T) {
		t.Parallel()
		tr := conntrack.NewTracker()
		c1, _ := net.Pipe()
		prev := 0
		ic := conntrack.WrapWithIdle(c1, 0, func(conntrack.Stats) { prev++ })
		tr.Add(ic)
		if tr.Count() != 1 {
			t.Fatalf("Count = %d, want 1", tr.Count())
		}
		_ = ic.Close()
		if prev != 1 || tr.Count() != 0 {
			t.Errorf("prev=%d Count=%d, want 1/0", prev, tr.Count())
		}
	})

	t.Run("CloseAll closes every conn and returns the count", func(t *testing.T) {
		t.Parallel()
		tr := conntrack.NewTracker()
		closed := 0
		for range 3 {
			c1, _ := net.Pipe()
			tr.Add(conntrack.WrapWithIdle(c1, 0, func(conntrack.Stats) { closed++ }))
		}
		if n := tr.CloseAll(); n != 3 {
			t.Errorf("CloseAll = %d, want 3", n)
		}
		if closed != 3 || tr.Count() != 0 {
			t.Errorf("closed=%d Count=%d", closed, tr.Count())
		}
		if n := tr.CloseAll(); n != 0 {
			t.Errorf("second CloseAll = %d, want 0", n)
		}
	})
}
