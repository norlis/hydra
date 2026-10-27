package limiter_test

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/norlis/hydra/internal/proxy/limiter"
)

func TestTunnel(t *testing.T) {
	t.Parallel()

	t.Run("admits up to the limit and rejects the next", func(t *testing.T) {
		t.Parallel()
		l := limiter.New(3)
		for i := range 3 {
			if !l.Acquire() {
				t.Fatalf("Acquire #%d = false, want true", i+1)
			}
		}
		if l.Acquire() {
			t.Error("4th Acquire = true, want false")
		}
		if got := l.Active(); got != 3 {
			t.Errorf("Active = %d, want 3", got)
		}
	})

	t.Run("a rejection does not consume a slot", func(t *testing.T) {
		t.Parallel()
		l := limiter.New(1)
		l.Acquire()
		l.Acquire() // rejected
		l.Release()
		if !l.Acquire() {
			t.Error("Acquire after Release = false, want true")
		}
	})

	for _, limit := range []int{0, -5} {
		t.Run("limit "+strconv.Itoa(limit)+" means unlimited and does not count", func(t *testing.T) {
			t.Parallel()
			l := limiter.New(limit)
			for range 1000 {
				if !l.Acquire() {
					t.Fatal("Acquire = false with unlimited limiter")
				}
			}
			if l.Active() != 0 {
				t.Errorf("Active = %d, want 0 (unlimited limiter does not count)", l.Active())
			}
		})
	}

	t.Run("Release without Acquire drives the counter negative", func(t *testing.T) {
		t.Parallel()
		l := limiter.New(5)
		l.Release()
		if got := l.Active(); got != -1 {
			t.Errorf("Active = %d, want -1", got)
		}
	})
}

func TestTunnel_Concurrent(t *testing.T) {
	t.Parallel()
	const limit = 10
	l := limiter.New(limit)
	var maxSeen atomic.Int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if !l.Acquire() {
				return
			}
			for {
				cur, act := maxSeen.Load(), l.Active()
				if act <= cur || maxSeen.CompareAndSwap(cur, act) {
					break
				}
			}
			l.Release()
		})
	}
	wg.Wait()
	if maxSeen.Load() > limit {
		t.Errorf("Active peaked at %d, want <= %d", maxSeen.Load(), limit)
	}
	if l.Active() != 0 {
		t.Errorf("Active after all releases = %d, want 0", l.Active())
	}
}
