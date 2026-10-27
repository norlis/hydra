package conntrack_test

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/norlis/hydra/internal/proxy/conntrack"
)

func TestInstrumentedConn_Counters(t *testing.T) {
	t.Parallel()
	c1, c2 := net.Pipe()
	var got conntrack.Stats
	ic := conntrack.WrapWithIdle(c1, 0, func(s conntrack.Stats) { got = s })

	var wg sync.WaitGroup
	wg.Go(func() {
		buf := make([]byte, 5)
		_, _ = c2.Read(buf)
		_, _ = c2.Write([]byte("1234567"))
	})
	if _, err := ic.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := ic.Read(buf)
	if err != nil || n != 7 {
		t.Fatalf("Read = %d, %v", n, err)
	}
	wg.Wait()

	_ = ic.Close()
	if got.BytesOut != 5 || got.BytesIn != 7 || got.Duration <= 0 {
		t.Errorf("Stats = %+v", got)
	}
}

func TestInstrumentedConn_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	t.Run("two consecutive Close calls fire onClose once", func(t *testing.T) {
		t.Parallel()
		c1, _ := net.Pipe()
		var calls atomic.Int32
		ic := conntrack.WrapWithIdle(c1, 0, func(conntrack.Stats) { calls.Add(1) })
		_ = ic.Close()
		_ = ic.Close()
		if calls.Load() != 1 {
			t.Errorf("onClose calls = %d, want 1", calls.Load())
		}
	})

	t.Run("concurrent Close calls fire onClose once", func(t *testing.T) {
		t.Parallel()
		c1, _ := net.Pipe()
		var calls atomic.Int32
		ic := conntrack.WrapWithIdle(c1, 0, func(conntrack.Stats) { calls.Add(1) })
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() { _ = ic.Close() })
		}
		wg.Wait()
		if calls.Load() != 1 {
			t.Errorf("onClose calls = %d, want 1", calls.Load())
		}
	})
}

// startRead runs a blocking Read in the bubble and exposes its error.
func startRead(c net.Conn) *atomic.Pointer[error] {
	var res atomic.Pointer[error]
	go func() {
		_, err := c.Read(make([]byte, 1))
		res.Store(&err)
	}()
	return &res
}

func TestInstrumentedConn_Watchdog(t *testing.T) {
	t.Parallel()
	t.Run("expires the connection after the idle window", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c1, _ := net.Pipe()
			ic := conntrack.WrapWithIdle(c1, time.Second, nil)
			res := startRead(ic)
			time.Sleep(time.Second + time.Millisecond)
			synctest.Wait()
			if res.Load() == nil || !errors.Is(*res.Load(), os.ErrDeadlineExceeded) {
				t.Fatalf("read result = %v, want ErrDeadlineExceeded", res.Load())
			}
		})
	})

	t.Run("activity resets the idle timer", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c1, c2 := net.Pipe()
			ic := conntrack.WrapWithIdle(c1, time.Second, nil)
			res := startRead(ic)
			time.Sleep(600 * time.Millisecond)
			if _, err := c2.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if res.Load() == nil || *res.Load() != nil {
				t.Fatalf("first read = %v, want success", res.Load())
			}
			res = startRead(ic)
			time.Sleep(900 * time.Millisecond) // t=1.5s: 1s original deadline passed, reset at 0.6s
			synctest.Wait()
			if res.Load() != nil {
				t.Fatalf("read finished early with %v", *res.Load())
			}
			time.Sleep(200 * time.Millisecond) // t=1.7s > 0.6s + 1s
			synctest.Wait()
			if res.Load() == nil || !errors.Is(*res.Load(), os.ErrDeadlineExceeded) {
				t.Fatalf("read result = %v, want ErrDeadlineExceeded", res.Load())
			}
		})
	})

	t.Run("zero idle arms no timer", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c1, _ := net.Pipe()
			ic := conntrack.WrapWithIdle(c1, 0, nil)
			res := startRead(ic)
			time.Sleep(time.Hour)
			synctest.Wait()
			if res.Load() != nil {
				t.Fatalf("read finished with %v, want still blocked", *res.Load())
			}
			_ = ic.Close()
		})
	})
}
